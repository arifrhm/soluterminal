// Command jevcheck runs the System One tier classifier outside the daemon.
//
// It is a diagnostic and dogfood tool: point it at a task prompt (and
// optionally a repository to survey) and it prints the verdict Jev would have
// produced, plus the confidence, probability distribution, path scores, and
// token usage. With TYPESAFE_BASE_URL set it targets any HTTP mock.
//
//	export TYPESAFE_API_KEY=...
//	go run ./cmd/jevcheck -repo /path/to/repo -prompt-file /tmp/task.txt
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"soluterminal-shared/typesafe"
	"soluterminal/jev"
)

func main() {
	var (
		prompt      = flag.String("prompt", "", "task prompt text")
		promptFile  = flag.String("prompt-file", "", "read the task prompt from this file ('-' for stdin)")
		repo        = flag.String("repo", "", "repository path to survey (top-level entries + README head)")
		surveyFile  = flag.String("survey-file", "", "read the survey text from this file")
		deliverable = flag.String("deliverable", "change", "change|plan|report")
		risk        = flag.String("risk", "", "risk-keyword advisory the engine would pass")
		model       = flag.String("model", "", "model override (default TYPESAFE_MODEL or jev-latest)")
		minConf     = flag.Float64("min-confidence", 0, "tier confidence floor (default 0.45)")
		fileThresh  = flag.Float64("file-threshold", 0, "path probability threshold (default 0.50)")
		timeout     = flag.Duration("timeout", 0, "request timeout (default 10s)")
		listModels  = flag.Bool("list-models", false, "list available models and exit")
		asJSON      = flag.Bool("json", true, "print the full JSON result (set -json=false for a one-line summary)")
		dataDir     = flag.String("data", defaultJevDataDir(), "soluterminal data dir; its workflow_secrets.json is read when TYPESAFE_API_KEY is unset")
	)
	flag.Parse()

	key, keySource, err := resolveAPIKey(*dataDir)
	if err != nil {
		fatal(err)
	}
	client := typesafe.New(key)
	if base := strings.TrimSpace(os.Getenv(typesafe.EnvBaseURL)); base != "" {
		client.BaseURL = base
	}

	if *listModels {
		models, err := client.ListModels(context.Background())
		if err != nil {
			fatal(err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(models)
		if !*asJSON {
			fmt.Fprintf(os.Stderr, "key source: %s\n", keySource)
		}
		return
	}

	promptText, err := readText(*prompt, *promptFile, "prompt")
	if err != nil {
		fatal(err)
	}
	if promptText == "" && *repo == "" && *surveyFile == "" {
		fatal(fmt.Errorf("provide at least one of -prompt/-prompt-file, -repo, or -survey-file"))
	}

	survey := ""
	switch {
	case *surveyFile != "":
		if survey, err = readText("", *surveyFile, "survey"); err != nil {
			fatal(err)
		}
	case *repo != "":
		survey, err = repoSurvey(*repo)
		if err != nil {
			fatal(err)
		}
	}

	cfg := jev.ConfigFromEnv()
	cfg.Enabled = true
	if *model != "" {
		cfg.Model = *model
	}
	if *minConf > 0 {
		cfg.MinTierConfidence = *minConf
	}
	if *fileThresh > 0 {
		cfg.FileThreshold = *fileThresh
	}
	if *timeout > 0 {
		cfg.Timeout = *timeout
	}

	clf := jev.New(client, cfg)
	verdict, meta, err := clf.ClassifyTier(context.Background(), jev.Input{
		Prompt:       promptText,
		Deliverable:  *deliverable,
		RiskAdvisory: *risk,
		Survey:       survey,
	})
	if err != nil && !errors.Is(err, jev.ErrLowConfidence) {
		fatal(err)
	}
	lowConfidence := errors.Is(err, jev.ErrLowConfidence)
	if lowConfidence {
		// The verdict is still useful: the runtime path escalates a
		// low-confidence small to standard. Report it and exit 2.
		fmt.Fprintf(os.Stderr, "warning: low tier confidence: %v\n", err)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(map[string]any{"verdict": verdict, "meta": meta, "lowConfidence": lowConfidence}); err != nil {
			fatal(err)
		}
		if lowConfidence {
			os.Exit(2)
		}
		return
	}
	fmt.Printf("tier=%s confidence=%.2f risk=%v files=%v cost=$%.6f input_tokens=%d\n",
		verdict.Tier, meta.TierConfidence, verdict.RiskBehaviorChange, verdict.FilesLikelyTouched,
		meta.EstimatedCostUSD, meta.Usage.InputTokens)
	if lowConfidence {
		os.Exit(2)
	}
}

func readText(inline, file, name string) (string, error) {
	if inline != "" {
		return inline, nil
	}
	if file == "" {
		return "", nil
	}
	var data []byte
	var err error
	if file == "-" {
		data, err = readAllStdin()
	} else {
		data, err = os.ReadFile(file)
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", name, err)
	}
	return strings.TrimSpace(string(data)), nil
}

func readAllStdin() ([]byte, error) {
	return io.ReadAll(os.Stdin)
}

// repoSurvey mirrors the daemon's shallow repository survey so the CLI sees the
// same candidates the engine would send.
func repoSurvey(repoPath string) (string, error) {
	entries, err := os.ReadDir(repoPath)
	if err != nil {
		return "", err
	}
	const maxEntries = 60
	var names []string
	total := 0
	for _, ent := range entries {
		if strings.HasPrefix(ent.Name(), ".") {
			continue
		}
		total++
		if len(names) >= maxEntries {
			continue
		}
		name := ent.Name()
		if ent.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	line := "Top-level entries: " + strings.Join(names, ", ")
	if total > len(names) {
		line += fmt.Sprintf(", …(+%d more)", total-len(names))
	}
	for _, name := range []string{"README.md", "README", "README.rst", "readme.md"} {
		data, err := os.ReadFile(filepath.Join(repoPath, name))
		if err != nil {
			continue
		}
		if len(data) > 2048 {
			data = data[:2048]
			if i := strings.LastIndexByte(string(data), '\n'); i >= 0 {
				data = data[:i+1]
			}
		}
		if head := strings.TrimRight(string(data), "\n"); head != "" {
			line += "\n\nREADME (head):\n" + head
		}
		break
	}
	return line, nil
}

func defaultJevDataDir() string {
	if d := strings.TrimSpace(os.Getenv("SOLU_DATA_DIR")); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".solu"
	}
	return filepath.Join(home, ".solu")
}

// resolveAPIKey prefers the environment, then the daemon's workflow secret
// store, so a key saved once in the Credentials UI works here too. The value is
// never printed; only its source is.
func resolveAPIKey(dataDir string) (key, source string, err error) {
	if k := strings.TrimSpace(os.Getenv(typesafe.EnvAPIKey)); k != "" {
		return k, "environment", nil
	}
	path := filepath.Join(dataDir, "workflow_secrets.json")
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		return "", "", fmt.Errorf("set %s or save the key in the Credentials UI: %w", typesafe.EnvAPIKey, rerr)
	}
	var store struct {
		Runtimes map[string]map[string]string `json:"runtimes"`
	}
	if uerr := json.Unmarshal(data, &store); uerr != nil {
		return "", "", fmt.Errorf("parse %s: %w", path, uerr)
	}
	if k := strings.TrimSpace(store.Runtimes["typesafe-api"]["TYPESAFE_API_KEY"]); k != "" {
		return k, path, nil
	}
	return "", "", fmt.Errorf("no TYPESAFE_API_KEY for runtime typesafe-api in %s; save it in the Credentials UI", path)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "jevcheck:", err)
	os.Exit(1)
}
