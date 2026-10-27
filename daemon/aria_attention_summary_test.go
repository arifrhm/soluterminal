package main

import (
	"strings"
	"testing"
)

func TestParseAttentionSummary(t *testing.T) {
	t.Parallel()
	rec := parseAttentionSummary("```json\n{\"summary\":\"Wants to run a prod DB migration\",\"risk\":\"HIGH\",\"action\":\"Review the migration first\"}\n```")
	if rec == nil {
		t.Fatal("fenced JSON not parsed")
	}
	if rec.Summary != "Wants to run a prod DB migration" || rec.Risk != "high" || rec.Action != "Review the migration first" {
		t.Fatalf("rec = %+v", rec)
	}

	if parseAttentionSummary("no json here") != nil {
		t.Fatal("prose without JSON should return nil")
	}
	if parseAttentionSummary(`{"risk":"low"}`) != nil {
		t.Fatal("empty summary should return nil")
	}
	if got := parseAttentionSummary(`{"summary":"x","risk":"whatever"}`); got == nil || got.Risk != "" {
		t.Fatalf("unknown risk should normalize to empty, got %+v", got)
	}
	long := parseAttentionSummary(`{"summary":"` + strings.Repeat("a", 500) + `"}`)
	if long == nil || len(long.Summary) > 170 {
		t.Fatalf("summary not clamped: %d chars", len(long.Summary))
	}
}

func TestMaybeSummarizeAttentionGates(t *testing.T) {
	t.Parallel()
	p := testPaneWithTail(t, "pane_sum", "sum", "Do you want to proceed?\n❯ 1. Yes\n  2. No\n")
	a := testAriaWithPane(t, p)
	a.attnSum = newAttentionSummarizer()

	// No cloud creds → never claims an attempt.
	a.maybeSummarizeAttention(p, "")
	if len(a.attnSum.fires) != 0 || len(a.attnSum.inFlight) != 0 {
		t.Fatal("summarizer claimed an attempt with no cloud creds")
	}

	// Paused → never claims an attempt, even with creds.
	a.setCloudCreds("http://cloud.invalid", "tok")
	a.mu.Lock()
	a.paused = true
	a.mu.Unlock()
	a.maybeSummarizeAttention(p, "")
	if len(a.attnSum.fires) != 0 {
		t.Fatal("summarizer claimed an attempt while paused")
	}
	a.mu.Lock()
	a.paused = false
	a.mu.Unlock()

	// done for the current key → skip without a new claim. Build the key
	// exactly the way maybeSummarizeAttention does (state first — computing
	// it acks the choice prompt and advances the pos).
	state := p.computeStateCached()
	key := attnSummaryKey{pos: p.LastAckedChoicePromptPos, state: state}
	a.attnSum.done[p.ID] = key
	a.maybeSummarizeAttention(p, "")
	if len(a.attnSum.fires) != 0 {
		t.Fatal("summarizer re-claimed an already-summarized prompt")
	}
}
