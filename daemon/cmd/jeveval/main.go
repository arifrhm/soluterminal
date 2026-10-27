// Command jeveval reports observed classifier outcomes without model calls.
// Attribution follows the accepted classifier artifact to its successful producer.
// It reports snapshot outcomes, not causal accuracy or proof of optimisation.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"soluterminal-shared/typesafe"
)

var confidenceRe = regexp.MustCompile(`confidence=([0-9.]+)`)
var tierOrder = []string{"small", "standard", "complex", "critical"}

const jevRuntimeID = "typesafe-api"

type invocation struct {
	ID               string   `json:"id"`
	Runtime          string   `json:"runtime"`
	Role             string   `json:"role"`
	Stage            string   `json:"stage"`
	Model            string   `json:"model"`
	Cohort           string   `json:"cohort"`
	Outcome          string   `json:"outcome"`
	OutputArtifacts  []string `json:"outputArtifacts"`
	EstimatedCostUSD *float64 `json:"estimatedCostUsd"`
	TokensIn         *int64   `json:"tokensIn"`
	DurationMs       *int64   `json:"durationMs"`
}
type artifact struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Path                 string `json:"path"`
	ProducerInvocationID string `json:"producerInvocationId"`
}
type taskSnapshot struct {
	ID                   string       `json:"id"`
	Status               string       `json:"status"`
	RepoPath             string       `json:"repoPath"`
	Deliverable          string       `json:"deliverable"`
	ResolvedTier         string       `json:"resolvedTier"`
	ClassifierTier       string       `json:"classifierTier"`
	ClassifierArtifactID string       `json:"classifierArtifactId"`
	RoutingSource        string       `json:"routingSource"`
	RepairCount          int          `json:"repairCount"`
	CreatedAt            string       `json:"createdAt"`
	TerminalAt           string       `json:"terminalAt"`
	Invocations          []invocation `json:"invocations"`
	Artifacts            []artifact   `json:"artifacts"`
}
type classification struct {
	Tier      string `json:"tier"`
	Reasoning string `json:"reasoning"`
}
type Sample struct {
	TaskID               string   `json:"taskId"`
	Source               string   `json:"source"` // jev | incumbent | unknown
	Attribution          string   `json:"attribution"`
	Repo                 string   `json:"repo"`
	Deliverable          string   `json:"deliverable"`
	CreatedAt            string   `json:"createdAt"`
	ClassifierModel      string   `json:"classifierModel"`
	Cohort               string   `json:"cohort"`
	ClassifierDurationMs *int64   `json:"classifierDurationMs"`
	Tier                 string   `json:"tier"`
	Confidence           *float64 `json:"confidence"`
	Resolved             string   `json:"resolvedTier"`
	Status               string   `json:"status"`
	Invocations          int      `json:"invocations"`
	Repairs              int      `json:"repairs"`
	Failed               bool     `json:"failed"`
	Mature               bool     `json:"mature"`
	CostUSD              float64  `json:"knownCostUsd"`
	CostComplete         bool     `json:"costComplete"`
	DurationMin          *float64 `json:"durationMin"`
}
type TierStats struct {
	Tier            string   `json:"tier"`
	Tasks           int      `json:"tasks"`
	Mature          int      `json:"mature"`
	Completed       int      `json:"completed"`
	Failed          int      `json:"failed"`
	Cancelled       int      `json:"cancelled"`
	Pending         int      `json:"pending"`
	CostSamples     int      `json:"costSamples"`
	DurationSamples int      `json:"durationSamples"`
	AvgInvocations  float64  `json:"avgInvocations"`
	AvgRepairs      float64  `json:"avgRepairs"`
	FailurePct      *float64 `json:"failurePct"`
	AvgCostUSD      *float64 `json:"avgCostUsd"`
	AvgDurationMin  *float64 `json:"avgDurationMin"`
}
type Report struct {
	DataDir        string      `json:"dataDir"`
	ObservedAt     string      `json:"observedAt"`
	TasksScanned   int         `json:"tasksScanned"`
	Unreadable     int         `json:"unreadable"`
	Classified     int         `json:"classified"`
	JevCount       int         `json:"jevCount"`
	IncumbentCount int         `json:"incumbentCount"`
	UnknownCount   int         `json:"unknownCount"`
	Overall        []TierStats `json:"overallByTier"`
	Jev            []TierStats `json:"jevByTier"`
	Incumbent      []TierStats `json:"incumbentByTier"`
	Samples        []Sample    `json:"samples"`
	// Triage shadow: the read-only Jev-vs-incumbent comparison the engine
	// records as a TriageShadow artifact when the shadow is enabled.
	Shadow          []ShadowSample            `json:"shadow"`
	ShadowTotal     int                       `json:"shadowTotal"`
	ShadowAgree     int                       `json:"shadowAgree"`
	ShadowAgreement float64                   `json:"shadowAgreement"`
	ShadowMatrix    map[string]map[string]int `json:"shadowMatrix"`
}

// ShadowSample is one read-only Jev-vs-incumbent triage comparison.
type ShadowSample struct {
	TaskID          string  `json:"taskId"`
	IncumbentAction string  `json:"incumbentAction"`
	JevAction       string  `json:"jevAction"`
	JevConfidence   float64 `json:"jevConfidence"`
	Agree           bool    `json:"agree"`
}

// collectShadows reads TriageShadow artifacts. Any other artifact shape is
// ignored: the comparison is identified by its two action fields, not by name.
func collectShadows(taskID, dir string) []ShadowSample {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []ShadowSample
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			continue
		}
		var s ShadowSample
		if json.Unmarshal(raw, &s) != nil {
			continue
		}
		if strings.TrimSpace(s.IncumbentAction) == "" || strings.TrimSpace(s.JevAction) == "" {
			continue
		}
		s.TaskID = taskID
		out = append(out, s)
	}
	return out
}

// printShadow prints the comparison action-by-action: agreement by itself is
// not quality, so a single rate would hide where the two disagree.
func printShadow(rep Report) {
	if rep.ShadowTotal == 0 {
		fmt.Println("\nJev triage shadow: none recorded (set SOLU_JEV_TRIAGE_SHADOW=1 to measure)")
		return
	}
	fmt.Printf("\nJev triage shadow: %d comparison(s), agreement %.0f%%\n", rep.ShadowTotal, rep.ShadowAgreement*100)
	incumbents := make([]string, 0, len(rep.ShadowMatrix))
	for from := range rep.ShadowMatrix {
		incumbents = append(incumbents, from)
	}
	sort.Strings(incumbents)
	for _, from := range incumbents {
		jevs := make([]string, 0, len(rep.ShadowMatrix[from]))
		for to := range rep.ShadowMatrix[from] {
			jevs = append(jevs, to)
		}
		sort.Strings(jevs)
		for _, to := range jevs {
			fmt.Printf("  incumbent %-18s -> jev %-18s %d\n", from, to, rep.ShadowMatrix[from][to])
		}
	}
}

func Collect(dataDir string) (Report, error) { return collect(dataDir, "", "", "") }
func collect(dataDir, since, repo, model string) (Report, error) {
	root := filepath.Join(dataDir, "workflow", "tasks")
	entries, err := os.ReadDir(root)
	if err != nil {
		return Report{}, fmt.Errorf("read task store: %w", err)
	}
	rep := Report{DataDir: dataDir, ObservedAt: time.Now().UTC().Format(time.RFC3339)}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		taskDir := filepath.Join(root, ent.Name())
		raw, err := os.ReadFile(filepath.Join(taskDir, "task.json"))
		var t taskSnapshot
		if err != nil || json.Unmarshal(raw, &t) != nil {
			rep.Unreadable++
			continue
		}
		rep.TasksScanned++
		if since != "" {
			cutoff, _ := time.Parse(time.RFC3339, since)
			created, err := time.Parse(time.RFC3339Nano, t.CreatedAt)
			if err != nil || created.Before(cutoff) {
				continue
			}
		}
		if repo != "" && filepath.Clean(t.RepoPath) != filepath.Clean(repo) {
			continue
		}
		rep.Shadow = append(rep.Shadow, collectShadows(t.ID, filepath.Join(taskDir, "artifacts"))...)
		if t.ClassifierTier == "" && t.ClassifierArtifactID == "" {
			continue
		}
		s := Sample{TaskID: t.ID, Source: "unknown", Attribution: "accepted artifact producer unavailable", Repo: t.RepoPath, Deliverable: t.Deliverable,
			CreatedAt: t.CreatedAt, Tier: t.ClassifierTier, Resolved: t.ResolvedTier, Status: t.Status, Invocations: len(t.Invocations), Repairs: t.RepairCount,
			Failed: t.Status == "failed", Mature: t.Status == "completed" || t.Status == "failed", CostComplete: true}
		a, inv := acceptedProducer(t)
		if inv != nil {
			s.Source = "incumbent"
			if inv.Runtime == jevRuntimeID {
				s.Source = "jev"
			}
			s.Attribution = "accepted artifact producer"
			s.ClassifierModel, s.Cohort, s.ClassifierDurationMs = inv.Model, inv.Cohort, inv.DurationMs
		}
		if a != nil && filepath.IsLocal(a.Path) && strings.HasPrefix(filepath.ToSlash(a.Path), "artifacts/") {
			var c classification
			if b, err := os.ReadFile(filepath.Join(taskDir, a.Path)); err == nil && json.Unmarshal(b, &c) == nil {
				if s.Tier == "" {
					s.Tier = c.Tier
				}
				if s.Source == "jev" {
					if m := confidenceRe.FindStringSubmatch(c.Reasoning); len(m) == 2 {
						var conf float64
						if n, _ := fmt.Sscanf(m[1], "%f", &conf); n == 1 && conf >= 0 && conf <= 1 {
							s.Confidence = &conf
						}
					}
				}
			}
		}
		if model != "" && s.ClassifierModel != model {
			continue
		}
		if !validTier(s.Tier) {
			continue
		}
		for _, inv := range t.Invocations {
			switch {
			case inv.EstimatedCostUSD != nil && *inv.EstimatedCostUSD >= 0:
				s.CostUSD += *inv.EstimatedCostUSD
			case inv.Runtime == jevRuntimeID && inv.TokensIn != nil && *inv.TokensIn >= 0:
				s.CostUSD += float64(*inv.TokensIn) / 1e9 * typesafe.PricePerBillionInputTokensUSD
			default:
				s.CostComplete = false
			}
		}
		if s.Mature {
			s.DurationMin = durationMinutes(t.CreatedAt, t.TerminalAt)
		}
		rep.Classified++
		switch s.Source {
		case "jev":
			rep.JevCount++
		case "incumbent":
			rep.IncumbentCount++
		default:
			rep.UnknownCount++
		}
		rep.Samples = append(rep.Samples, s)
	}
	sort.Slice(rep.Samples, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339Nano, rep.Samples[i].CreatedAt)
		b, _ := time.Parse(time.RFC3339Nano, rep.Samples[j].CreatedAt)
		if a.Equal(b) {
			return rep.Samples[i].TaskID < rep.Samples[j].TaskID
		}
		return a.Before(b)
	})
	rep.Overall, rep.Jev, rep.Incumbent = summarize(rep.Samples, ""), summarize(rep.Samples, "jev"), summarize(rep.Samples, "incumbent")
	rep.ShadowTotal = len(rep.Shadow)
	rep.ShadowMatrix = map[string]map[string]int{}
	for _, sh := range rep.Shadow {
		if sh.Agree {
			rep.ShadowAgree++
		}
		if rep.ShadowMatrix[sh.IncumbentAction] == nil {
			rep.ShadowMatrix[sh.IncumbentAction] = map[string]int{}
		}
		rep.ShadowMatrix[sh.IncumbentAction][sh.JevAction]++
	}
	if rep.ShadowTotal > 0 {
		rep.ShadowAgreement = float64(rep.ShadowAgree) / float64(rep.ShadowTotal)
	}
	sort.Slice(rep.Shadow, func(i, j int) bool { return rep.Shadow[i].TaskID < rep.Shadow[j].TaskID })
	return rep, nil
}

// A failed attempt or an unrelated Jev invocation cannot own a routing decision.
// Never guess from random artifact filenames or from the last provider attempted.
func acceptedProducer(t taskSnapshot) (*artifact, *invocation) {
	if t.ClassifierArtifactID == "" {
		return nil, nil
	}
	for _, a := range t.Artifacts {
		if a.ID != t.ClassifierArtifactID || a.Type != "TierClassification" {
			continue
		}
		for _, inv := range t.Invocations {
			if inv.Role != "classifier" || inv.Stage != "classify" || inv.Outcome != "ok" {
				continue
			}
			matches := a.ProducerInvocationID != "" && inv.ID == a.ProducerInvocationID
			if a.ProducerInvocationID == "" {
				for _, id := range inv.OutputArtifacts {
					if id == a.ID {
						matches = true
					}
				}
			}
			if matches {
				return &a, &inv
			}
		}
		return &a, nil
	}
	return nil, nil
}
func validTier(s string) bool {
	for _, t := range tierOrder {
		if s == t {
			return true
		}
	}
	return false
}
func summarize(samples []Sample, source string) []TierStats {
	var out []TierStats
	for _, tier := range tierOrder {
		st := TierStats{Tier: tier}
		var cost, duration float64
		for _, s := range samples {
			if s.Tier != tier || (source != "" && s.Source != source) {
				continue
			}
			st.Tasks++
			switch s.Status {
			case "completed":
				st.Completed++
			case "failed":
				st.Failed++
			case "cancelled":
				st.Cancelled++
			default:
				st.Pending++
			}
			if !s.Mature {
				continue
			}
			st.Mature++
			st.AvgInvocations += float64(s.Invocations)
			st.AvgRepairs += float64(s.Repairs)
			if s.CostComplete {
				st.CostSamples++
				cost += s.CostUSD
			}
			if s.DurationMin != nil {
				st.DurationSamples++
				duration += *s.DurationMin
			}
		}
		if st.Tasks == 0 {
			continue
		}
		if st.Mature > 0 {
			st.AvgInvocations /= float64(st.Mature)
			st.AvgRepairs /= float64(st.Mature)
			p := float64(st.Failed) * 100 / float64(st.Mature)
			st.FailurePct = &p
		}
		if st.CostSamples > 0 {
			v := cost / float64(st.CostSamples)
			st.AvgCostUSD = &v
		}
		if st.DurationSamples > 0 {
			v := duration / float64(st.DurationSamples)
			st.AvgDurationMin = &v
		}
		out = append(out, st)
	}
	return out
}
func durationMinutes(created, ended string) *float64 {
	c, ec := time.Parse(time.RFC3339Nano, created)
	e, ee := time.Parse(time.RFC3339Nano, ended)
	if ec != nil || ee != nil || e.Before(c) {
		return nil
	}
	v := e.Sub(c).Minutes()
	return &v
}
func main() {
	dataDir := flag.String("data", defaultDataDir(), "soluterminal data directory")
	asJSON := flag.Bool("json", false, "emit JSON, including per-task model, cohort, resolved tier and missing-cost evidence")
	since := flag.String("since", "", "include tasks created at or after this RFC3339 timestamp")
	repo := flag.String("repo", "", "filter by exact repository path")
	model := flag.String("model", "", "filter by actual accepted classifier model")
	flag.Parse()
	if *since != "" {
		if _, err := time.Parse(time.RFC3339, *since); err != nil {
			fmt.Fprintln(os.Stderr, "invalid -since:", err)
			os.Exit(2)
		}
	}
	rep, err := collect(*dataDir, *since, *repo, *model)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jeveval:", err)
		os.Exit(1)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	fmt.Printf("Jev classifier snapshot — %s\ntasks scanned: %d | classified: %d (jev %d / incumbent %d / unknown %d) | unreadable: %d\n", rep.ObservedAt, rep.TasksScanned, rep.Classified, rep.JevCount, rep.IncumbentCount, rep.UnknownCount, rep.Unreadable)
	fmt.Println("Outcome denominator: completed + failed snapshots only; cancelled and pending are separate. Completed does not prove merged/released. Costs exclude incomplete records; elapsed time includes queue/approval waits. These are unmatched observations, not accuracy or causal improvements.")
	printTable("All accepted classifications", rep.Overall)
	printTable("Jev", rep.Jev)
	printTable("Incumbent", rep.Incumbent)
	printShadow(rep)
}
func number(v *float64) string {
	if v == nil {
		return "unknown"
	}
	return fmt.Sprintf("%.2f", *v)
}
func printTable(title string, rows []TierStats) {
	fmt.Printf("\n%s\n  tier      total mature cancel pending fail%% cost$ (n) elapsedMin (n)\n", title)
	for _, r := range rows {
		fmt.Printf("  %-9s %5d %6d %6d %7d %s %s (%d) %s (%d)\n", r.Tier, r.Tasks, r.Mature, r.Cancelled, r.Pending, number(r.FailurePct), number(r.AvgCostUSD), r.CostSamples, number(r.AvgDurationMin), r.DurationSamples)
	}
}
func defaultDataDir() string {
	if d := strings.TrimSpace(os.Getenv("SOLU_DATA_DIR")); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".solu"
	}
	return filepath.Join(home, ".solu")
}
