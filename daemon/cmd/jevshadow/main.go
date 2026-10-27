// jevshadow runs opt-in typed-decision experiments on caller-supplied redacted
// cases. Default is offline validation. It never reads or mutates the task store.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"soluterminal-shared/typesafe"
	"soluterminal/jev"
)

type row struct {
	ID         string              `json:"id"`
	Mode       string              `json:"mode"`
	StateHash  string              `json:"stateHash"`
	Decision   *jev.ShadowDecision `json:"decision,omitempty"`
	Response   *typesafe.Response  `json:"response,omitempty"`
	DurationMs *int64              `json:"durationMs,omitempty"`
	Error      string              `json:"error,omitempty"`
}
type report struct {
	Mode              string  `json:"mode"`
	AdvisoryOnly      bool    `json:"advisoryOnly"`
	Threshold         float64 `json:"threshold"`
	Cases             int     `json:"cases"`
	Evaluated         int     `json:"evaluated"`
	Abstained         int     `json:"abstained"`
	LabelledDecisions int     `json:"labelledDecisions"`
	Correct           int     `json:"correct"`
	Rows              []row   `json:"rows"`
}

func readJSON(path string, out any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4*1024*1024+1))
	if err != nil {
		return err
	}
	if len(b) > 4*1024*1024 {
		return fmt.Errorf("input exceeds 4 MiB")
	}
	return json.Unmarshal(b, out)
}
func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "jevshadow:", err)
		os.Exit(1)
	}
}
func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("jevshadow", flag.ContinueOnError)
	input := flags.String("input", "", "JSON array of redacted {id, mode, state, candidates?, expected?} cases")
	replay := flags.String("responses", "", "replay a prior jevshadow JSON report offline")
	execute := flags.Bool("execute", false, "send supplied cases to TypeSafe; uses TYPESAFE_API_KEY and incurs API charges")
	model := flags.String("model", "jev-1.13.0", "pinned version to evaluate; required to match actual response in live mode")
	dataDir := flags.String("data", defaultDataDir(), "credential store directory; used only with -execute when TYPESAFE_API_KEY is unset")
	threshold := flags.Float64("threshold", 0.8, "experimental abstention threshold, not a calibrated guarantee")
	maxCalls := flags.Int("max-calls", 20, "maximum cases/HTTP calls (1–100), no transport retries")
	timeout := flags.Duration("timeout", 10*time.Second, "maximum time per request")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return fmt.Errorf("-input is required; default is offline validation")
	}
	if *execute && *replay != "" {
		return fmt.Errorf("-execute and -responses are mutually exclusive")
	}
	if *threshold <= 0 || *threshold > 1 || *threshold != *threshold || *maxCalls < 1 || *maxCalls > 100 || *timeout <= 0 || *timeout > time.Minute {
		return fmt.Errorf("invalid threshold, max-calls or timeout")
	}
	if strings.TrimSpace(*model) == "" || strings.Contains(strings.ToLower(*model), "latest") {
		return fmt.Errorf("use a pinned model version")
	}
	var cases []jev.ShadowCase
	if err := readJSON(*input, &cases); err != nil {
		return err
	}
	if len(cases) == 0 || len(cases) > *maxCalls {
		return fmt.Errorf("case count must be 1–%d", *maxCalls)
	}
	seen := map[string]bool{}
	// Validate the complete batch before resolving credentials or sending anything.
	for _, c := range cases {
		if seen[c.ID] {
			return fmt.Errorf("duplicate case id %q", c.ID)
		}
		seen[c.ID] = true
		if _, err := jev.ShadowRequest(c, *model); err != nil {
			return fmt.Errorf("case %s: %w", c.ID, err)
		}
	}
	cached := map[string]row{}
	out := report{Mode: "dry_run", AdvisoryOnly: true, Threshold: *threshold, Cases: len(cases)}
	if *replay != "" {
		out.Mode = "offline_replay"
		var prior report
		if err := readJSON(*replay, &prior); err != nil {
			return err
		}
		for _, r := range prior.Rows {
			if _, ok := cached[r.ID]; ok {
				return fmt.Errorf("duplicate replay id")
			}
			cached[r.ID] = r
		}
	}
	var client *typesafe.Client
	if *execute {
		out.Mode = "live_shadow"
		key, err := apiKey(*dataDir)
		if err != nil {
			return err
		}
		client = typesafe.New(key)
		client.Retry.MaxAttempts = 1
		if base := os.Getenv(typesafe.EnvBaseURL); base != "" {
			client.BaseURL = base
		}
	}
	for _, c := range cases {
		// Bind cached evidence to exact mode/state/candidates; expected labels are
		// excluded so independently corrected labels can be evaluated offline.
		unlabelled := c
		unlabelled.Expected = ""
		material, _ := json.Marshal(unlabelled)
		hash := fmt.Sprintf("%x", sha256.Sum256(material))
		r := row{ID: c.ID, Mode: c.Mode, StateHash: hash}
		var response *typesafe.Response
		if client != nil {
			req, _ := jev.ShadowRequest(c, *model)
			ctx, cancel := context.WithTimeout(context.Background(), *timeout)
			started := time.Now()
			v, err := client.SystemOneModel(ctx, req.Model, req.State, req.Questions)
			elapsed := time.Since(started).Milliseconds()
			cancel()
			r.DurationMs = &elapsed
			if err != nil {
				r.Error = "provider request failed; no decision recorded"
			} else if v.Model != *model {
				r.Error = "provider returned a different model; pin mismatch"
			} else {
				response = v
			}
		} else if *replay != "" {
			prior, ok := cached[c.ID]
			if !ok || prior.StateHash != hash || prior.Mode != c.Mode || prior.Response == nil {
				r.Error = "missing or mismatched replay evidence"
			} else {
				response = prior.Response
			}
		}
		if response != nil {
			d, err := jev.InterpretShadow(c, response, *threshold)
			if err != nil {
				r.Error = err.Error()
			} else {
				r.Decision = &d
				r.Response = response
				out.Evaluated++
				if d.Abstained {
					out.Abstained++
				}
				if d.Correct != nil {
					out.LabelledDecisions++
					if *d.Correct {
						out.Correct++
					}
				}
			}
		}
		out.Rows = append(out.Rows, r)
	}
	enc := json.NewEncoder(output)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	for _, r := range out.Rows {
		if r.Error != "" {
			return fmt.Errorf("one or more cases failed; inspect per-case errors")
		}
	}
	return nil
}
