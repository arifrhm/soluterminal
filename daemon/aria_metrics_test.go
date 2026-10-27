package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRecordAutoEvalOutcomeLabelsApprovedAndDeferred(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	a := &Aria{
		metrics:      m,
		sweepStreaks: map[string]*sweepStreak{},
	}

	a.recordAutoEvalOutcome("pane_1", true)
	a.recordAutoEvalOutcome("pane_1", false)

	var buf bytes.Buffer
	m.WriteText(&buf, 0)
	out := buf.String()
	for _, want := range []string{
		`solud_aria_eval_outcome_total{outcome="approved"} 1`,
		`solud_aria_eval_outcome_total{outcome="deferred"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("metrics output missing %q\n--- output ---\n%s", want, out)
		}
	}
}

// A non-fire because the menu wasn't in the tail the model saw (buried by
// a multi-subagent burst) must NOT bump the sweeper-suspend streak —
// otherwise a transient flood parks the pane. Genuine declines around it
// still accumulate normally.
func TestRecordAutoEvalNoSurfaceDoesNotBumpStreak(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	a := &Aria{
		metrics:      m,
		sweepStreaks: map[string]*sweepStreak{},
	}

	// Two genuine declines → streak 2.
	a.recordAutoEvalOutcome("pane_1", false)
	a.recordAutoEvalOutcome("pane_1", false)
	if got := a.BackoffStreak("pane_1"); got != 2 {
		t.Fatalf("after 2 declines BackoffStreak = %d, want 2", got)
	}

	// A "menu not surfaced" non-fire must leave the streak untouched (not
	// 3, so the pane is NOT suspended by this).
	a.recordAutoEvalNoSurface("pane_1")
	if got := a.BackoffStreak("pane_1"); got != 2 {
		t.Fatalf("no-surface bumped the streak to %d, want it to stay 2", got)
	}

	// A subsequent genuine decline still counts → suspend threshold.
	a.recordAutoEvalOutcome("pane_1", false)
	if got := a.BackoffStreak("pane_1"); got != ariaSweeperSuspendThreshold {
		t.Fatalf("genuine decline after no-surface = %d, want %d", got, ariaSweeperSuspendThreshold)
	}

	var buf bytes.Buffer
	m.WriteText(&buf, 0)
	if out := buf.String(); !strings.Contains(out, `solud_aria_eval_outcome_total{outcome="no_menu"} 1`) {
		t.Fatalf("metrics missing no_menu label\n--- output ---\n%s", out)
	}
}
