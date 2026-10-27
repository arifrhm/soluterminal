package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeTask(t *testing.T, root string, snap taskSnapshot, c *classification) {
	t.Helper()
	dir := filepath.Join(root, "workflow", "tasks", snap.ID)
	if err := os.MkdirAll(filepath.Join(dir, "artifacts"), 0755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(snap)
	if err := os.WriteFile(filepath.Join(dir, "task.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if c != nil {
		b, _ = json.Marshal(c)
		if err := os.WriteFile(filepath.Join(dir, "artifacts", "accepted.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
func fixture(id, runtime, status string) taskSnapshot {
	cost := 0.5
	duration := int64(150)
	return taskSnapshot{ID: id, Status: status, RepoPath: "/repo", Deliverable: "change", ClassifierTier: "complex", ResolvedTier: "complex", ClassifierArtifactID: "accepted", CreatedAt: "2026-09-19T08:00:00Z", TerminalAt: "2026-09-19T09:00:00Z",
		Artifacts:   []artifact{{ID: "accepted", Type: "TierClassification", Path: "artifacts/accepted.json", ProducerInvocationID: "winner"}},
		Invocations: []invocation{{ID: "winner", Runtime: runtime, Role: "classifier", Stage: "classify", Outcome: "ok", Model: "pinned-model", OutputArtifacts: []string{"accepted"}, EstimatedCostUSD: &cost, DurationMs: &duration}}}
}
func TestCollectUsesAcceptedProducerNotEarlierJevAttempt(t *testing.T) {
	root := t.TempDir()
	s := fixture("fallback", "codex-headless", "completed")
	s.Invocations = append([]invocation{{ID: "failed-jev", Runtime: jevRuntimeID, Role: "classifier", Stage: "classify", Outcome: "error"}}, s.Invocations...)
	writeTask(t, root, s, &classification{Tier: "complex", Reasoning: "not Jev"})
	rep, err := Collect(root)
	if err != nil {
		t.Fatal(err)
	}
	if rep.JevCount != 0 || rep.IncumbentCount != 1 || rep.Samples[0].CostComplete {
		t.Fatalf("report: %+v", rep)
	}
	if rep.Samples[0].ClassifierModel != "pinned-model" || rep.Samples[0].Confidence != nil {
		t.Fatal("wrong provenance")
	}
	if rep.Overall[0].AvgCostUSD != nil {
		t.Fatal("incomplete cost became total cost")
	}
}
func TestOutcomesExcludeCancelledAndPending(t *testing.T) {
	root := t.TempDir()
	for _, status := range []string{"completed", "failed", "cancelled", "running", "awaiting_approval"} {
		writeTask(t, root, fixture(status, jevRuntimeID, status), &classification{Tier: "complex", Reasoning: "jev: confidence=0.92"})
	}
	rep, err := Collect(root)
	if err != nil {
		t.Fatal(err)
	}
	st := rep.Jev[0]
	if st.Tasks != 5 || st.Mature != 2 || st.Cancelled != 1 || st.Pending != 2 || st.FailurePct == nil || *st.FailurePct != 50 || st.CostSamples != 2 || st.DurationSamples != 2 {
		t.Fatalf("stats: %+v", st)
	}
	if st.AvgDurationMin == nil || *st.AvgDurationMin != 60 {
		t.Fatal("duration")
	}
	if rep.Samples[0].Confidence == nil || *rep.Samples[0].Confidence != 0.92 {
		t.Fatal("confidence")
	}
}
func TestUnknownAttributionAndMissingDurationStayUnknown(t *testing.T) {
	root := t.TempDir()
	s := fixture("old", jevRuntimeID, "completed")
	s.ClassifierArtifactID = ""
	s.TerminalAt = ""
	writeTask(t, root, s, &classification{Tier: "small", Reasoning: "jev confidence=0.9"})
	rep, err := Collect(root)
	if err != nil {
		t.Fatal(err)
	}
	if rep.UnknownCount != 1 || rep.JevCount != 0 || rep.Samples[0].Confidence != nil || rep.Samples[0].Tier != "complex" || rep.Overall[0].AvgDurationMin != nil {
		t.Fatalf("report: %+v", rep)
	}
}
func TestAttributionRejectsFailedWrongRoleAndWrongStage(t *testing.T) {
	for _, kind := range []string{"failed", "role", "stage", "producer"} {
		s := fixture("x", jevRuntimeID, "completed")
		switch kind {
		case "failed":
			s.Invocations[0].Outcome = "error"
		case "role":
			s.Invocations[0].Role = "executor"
		case "stage":
			s.Invocations[0].Stage = "implement"
		case "producer":
			s.Artifacts[0].ProducerInvocationID = "missing"
		}
		if _, inv := acceptedProducer(s); inv != nil {
			t.Fatalf("accepted %s", kind)
		}
	}
}
func TestCollectFiltersAndChronologicalSort(t *testing.T) {
	root := t.TempDir()
	early := fixture("z", jevRuntimeID, "completed")
	later := fixture("a", jevRuntimeID, "completed")
	later.CreatedAt = "2026-09-19T08:30:00Z"
	writeTask(t, root, early, nil)
	writeTask(t, root, later, nil)
	r, err := collect(root, "2026-09-19T08:20:00Z", "/repo", "pinned-model")
	if err != nil || r.Classified != 1 || r.Samples[0].TaskID != "a" {
		t.Fatalf("filter: %+v %v", r, err)
	}
	r, err = Collect(root)
	if err != nil || r.Samples[0].TaskID != "z" {
		t.Fatal("not chronological")
	}
}
func TestPendingOnlyHasNoFailureRate(t *testing.T) {
	rows := summarize([]Sample{{Tier: "small", Status: "running"}}, "")
	if rows[0].FailurePct != nil || rows[0].AvgCostUSD != nil {
		t.Fatal("missing observations became zeros")
	}
}

func TestCollectShadowsReadsOnlyComparisonArtifacts(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, body map[string]any) {
		t.Helper()
		b, _ := json.Marshal(body)
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("shadow.json", map[string]any{"schemaVersion": 1, "incumbentAction": "human", "jevAction": "resume", "jevConfidence": 0.62, "agree": false})
	write("tier.json", map[string]any{"tier": "standard", "reasoning": "x"})
	write("notes.txt", map[string]any{"incumbentAction": "human", "jevAction": "resume"})

	got := collectShadows("wft_x", dir)
	if len(got) != 1 {
		t.Fatalf("shadows = %d, want 1 (%#v)", len(got), got)
	}
	if got[0].TaskID != "wft_x" || got[0].IncumbentAction != "human" || got[0].JevAction != "resume" || got[0].Agree {
		t.Fatalf("shadow = %#v", got[0])
	}
	if got := collectShadows("wft_y", filepath.Join(t.TempDir(), "missing")); got != nil {
		t.Fatalf("missing dir = %#v, want nil", got)
	}
}
