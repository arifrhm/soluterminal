package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestMetricsWriteTextEmitsPrometheusFormat(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.UpdaterChecksTotal.Add(7)
	m.PushDeliveriesSum.Add(3)
	m.AriaEvalsTotal.Add(5)
	m.AriaEvalFiredTotal.Add(2)
	m.IncLabelled("solud_aria_eval_outcome_total", map[string]string{"outcome": "approved"})
	m.IncLabelled("solud_aria_eval_outcome_total", map[string]string{"outcome": "approved"})
	m.IncLabelled("solud_aria_eval_outcome_total", map[string]string{"outcome": "deferred"})

	var buf bytes.Buffer
	m.WriteText(&buf, 4)
	out := buf.String()

	// Each well-known counter must have its HELP, TYPE, and value line.
	wantSubstrings := []string{
		"# HELP solud_updater_checks_total",
		"# TYPE solud_updater_checks_total counter",
		"solud_updater_checks_total 7",
		"solud_push_deliveries_sum 3",
		"solud_aria_evals_total 5",
		"solud_aria_eval_fired_total 2",
		"solud_pane_running 4",
		`solud_aria_eval_outcome_total{outcome="approved"} 2`,
		`solud_aria_eval_outcome_total{outcome="deferred"} 1`,
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(out, s) {
			t.Errorf("metrics output missing %q\n--- output ---\n%s", s, out)
		}
	}
}

func TestMetricsLabelKeyOrderingIsStable(t *testing.T) {
	t.Parallel()
	// Same labels in different insertion orders must produce the same
	// series key — otherwise scrapes would inflate cardinality every
	// time call sites varied the literal map order.
	a := formatSeriesKey("foo", map[string]string{"b": "2", "a": "1"})
	b := formatSeriesKey("foo", map[string]string{"a": "1", "b": "2"})
	if a != b {
		t.Fatalf("series keys differ: %q vs %q", a, b)
	}
}

func TestMetricsLabelEscaping(t *testing.T) {
	t.Parallel()
	got := formatSeriesKey("foo", map[string]string{"k": `quote " and backslash \ here`})
	want := `foo{k="quote \" and backslash \\ here"}`
	if got != want {
		t.Fatalf("escape\n got: %s\nwant: %s", got, want)
	}
}
