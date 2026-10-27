package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"soluterminal-shared/typesafe"
)

func TestAutoModeChangeRearmsDeclinedPermission(t *testing.T) {
	a, p, frames, _ := newDirectAutoRepeatHarness(t, "pane-mode-recovery")
	a.metrics = NewMetrics()
	a.inFlightEvals = map[string]bool{}
	p.SetAutoMode("cautious")
	assertDirectAutoPromptReady(t, p, "pending permission")
	pos := atomic.LoadInt64(&p.LastAckedChoicePromptPos)
	for range ariaSweeperSuspendThreshold {
		a.recordAutoEvalOutcome(p.ID, false)
	}
	if !a.shouldSkipSweeper(p.ID, pos) {
		t.Fatal("fixture must be suspended")
	}
	p.SetAutoDeclined("cautious policy declined")
	a.sweepStreaks["other"] = &sweepStreak{consecutive: 3, suspendedAtPos: 10}
	a.autoFireTimes = []time.Time{time.Now().Add(-time.Minute)}
	a.autoFireRepeats = map[string]*autoFireRepeat{p.ID: {hash: 123, fires: 2}}
	server := &apiServer{mgr: a.mgr, aria: a}
	req := httptest.NewRequest("POST", "/api/panes/"+p.ID+"/auto-mode", strings.NewReader(`{"autoMode":"always"}`))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	server.setPaneAutoMode(w, req)
	if w.Code != 200 {
		t.Fatalf("mode change: %d %s", w.Code, w.Body.String())
	}
	if a.shouldSkipSweeper(p.ID, pos) || a.BackoffStreak(p.ID) != 0 {
		t.Fatal("full mode inherited the cautious-mode suspension")
	}
	if got := p.autoDeclineFor(pos); got != "" {
		t.Fatalf("old decline still controls attention: %q", got)
	}
	if a.BackoffStreak("other") != 3 || len(a.autoFireTimes) != 1 {
		t.Fatal("mode change reset another pane or the global fire budget")
	}
	if a.autoFireRepeats[p.ID].fires != 2 {
		t.Fatal("mode change erased the unchanged-screen write limit")
	}
	a.evaluateObservation(context.Background(), p.ID, p.Title, "")
	expectFrame(t, frames, "full-mode recovery", "\r")
	if got := a.metrics.AriaEvalFiredTotal.Load(); got != 1 {
		t.Fatalf("direct fire metric = %d, want 1", got)
	}
}

func TestAutoModeUnchangedPreservesSuspension(t *testing.T) {
	a, p, _, _ := newDirectAutoRepeatHarness(t, "pane-same-mode")
	a.sweepStreaks[p.ID] = &sweepStreak{consecutive: 3, suspendedAtPos: 10}
	server := &apiServer{mgr: a.mgr, aria: a}
	req := httptest.NewRequest("POST", "/api/panes/"+p.ID+"/auto-mode", strings.NewReader(`{"autoMode":"always"}`))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	server.setPaneAutoMode(w, req)
	if w.Code != 200 || a.BackoffStreak(p.ID) != 3 {
		t.Fatal("repeating the same setting must not reset suspension")
	}
}

func TestAutoModeCautiousPersistsAndReturnsMode(t *testing.T) {
	a, p, _, _ := newDirectAutoRepeatHarness(t, "pane-cautious-persistence")
	dataDir := t.TempDir()
	store, err := NewPaneStore(dataDir)
	if err != nil {
		t.Fatalf("NewPaneStore: %v", err)
	}
	server := &apiServer{mgr: a.mgr, aria: a, panes: store}
	req := httptest.NewRequest("POST", "/api/panes/"+p.ID+"/auto-mode", strings.NewReader(`{"autoMode":"cautious"}`))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	server.setPaneAutoMode(w, req)
	if w.Code != 200 {
		t.Fatalf("mode change: %d %s", w.Code, w.Body.String())
	}
	var dto struct {
		AutoMode string `json:"autoMode"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode mode response: %v", err)
	}
	if dto.AutoMode != "cautious" {
		t.Fatalf("response mode = %q, want cautious", dto.AutoMode)
	}
	if got := p.Snapshot().AutoMode; got != "cautious" {
		t.Fatalf("live mode = %q, want cautious", got)
	}
	var saved *PersistedPane
	for _, entry := range store.List() {
		if entry.ID == p.ID {
			entryCopy := entry
			saved = &entryCopy
			break
		}
	}
	if saved == nil || saved.AutoMode != "cautious" {
		t.Fatalf("persisted mode = %+v, want cautious", saved)
	}
	reloaded, err := NewPaneStore(dataDir)
	if err != nil {
		t.Fatalf("reload pane store: %v", err)
	}
	entries := reloaded.List()
	if len(entries) != 1 || entries[0].AutoMode != "cautious" {
		t.Fatalf("reloaded panes = %+v, want one cautious pane", entries)
	}
}

func TestAutoModeFailedPersistencePreservesSuspension(t *testing.T) {
	a, p, _, _ := newDirectAutoRepeatHarness(t, "pane-mode-save-failed")
	p.SetAutoMode("cautious")
	a.sweepStreaks[p.ID] = &sweepStreak{consecutive: 3, suspendedAtPos: 10}
	p.SetAutoDeclined("cautious decline")
	server := &apiServer{mgr: a.mgr, aria: a, panes: &failingPaneStore{putErr: errors.New("disk full")}}
	req := httptest.NewRequest("POST", "/api/panes/"+p.ID+"/auto-mode", strings.NewReader(`{"autoMode":"always"}`))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	server.setPaneAutoMode(w, req)
	if w.Code != 500 || p.Snapshot().AutoMode != "cautious" || a.BackoffStreak(p.ID) != 3 {
		t.Fatal("failed mode change must preserve the previous policy and suspension")
	}
	if p.autoDeclineFor(atomic.LoadInt64(&p.LastAckedChoicePromptPos)) != "cautious decline" {
		t.Fatal("failed persistence cleared the previous decline")
	}
}

func TestJevTransientFallbackRearmsSuspendedSweeperAfterDeadline(t *testing.T) {
	for _, result := range []string{"fallback", "negative", "terminal"} {
		t.Run(result, func(t *testing.T) {
			a := &Aria{
				sweepStreaks: map[string]*sweepStreak{"pane": {consecutive: 3, suspendedAtPos: 10}},
				jevDetectCache: map[string]jevDetectCacheEntry{"pane": {
					pos: 10, result: result, expires: time.Now().Add(time.Minute),
				}},
			}
			if !a.shouldSkipSweeper("pane", 10) {
				t.Fatal("retry must respect provider backoff")
			}
			entry := a.jevDetectCache["pane"]
			entry.expires = time.Now().Add(-time.Second)
			a.jevDetectCache["pane"] = entry
			if got := a.shouldSkipSweeper("pane", 10); got != (result != "fallback") {
				t.Fatalf("expired %s: skip=%v", result, got)
			}
		})
	}
}

func TestAutoFireIdentityPreservesActionNumbers(t *testing.T) {
	p, _ := newTypingHoldPane(t, "pane-numbered-actions")
	menu := "Bash command\n  cat reports/2026-10-01.txt\nDo you want to proceed?\n❯ 1. Yes\n  2. No"
	paintReplayScreen(t, p, menu)
	first := autoFireObservationIdentity(p, "")
	paintReplayScreen(t, p, strings.Replace(menu, "2026-10-01", "2026-10-02", 1))
	if autoFireObservationIdentity(p, "") == first {
		t.Fatal("different numbered action targets share a repeat budget")
	}
}

func TestAutoFireIdentityIgnoresNumericStatusRows(t *testing.T) {
	p, _ := newTypingHoldPane(t, "pane-status-numbers")
	menu := "Bash command\n  cat reports/2026-10-01.txt\nDo you want to proceed?\n❯ 1. Yes\n  2. No"
	paintReplayScreen(t, p, menu+"\nContext 89% used · GPT-6\n465.9k tokens processed\n10% context left")
	first := autoFireObservationIdentity(p, "")
	paintReplayScreen(t, p, menu+"\nContext 90% used · GPT-6\n466.0k tokens processed\n9% context left")
	if got := autoFireObservationIdentity(p, ""); got != first {
		t.Fatal("numeric status redraw rearmed the unchanged-screen write limit")
	}
}

func TestDirectAutoFireRearmsForDifferentNumberedAction(t *testing.T) {
	a, p, frames, _ := newDirectAutoRepeatHarness(t, "pane-numbered-action-recovery")
	menu := "Bash command\n  cat reports/2026-10-01.txt\nDo you want to proceed?\n❯ 1. Yes\n  2. No"
	seedGridMenu(t, p, menu)
	for i := 0; i < ariaAutoUnchangedFireLimit; i++ {
		if i > 0 {
			appendDirectAutoMenuRedraw(t, p, menu)
		}
		a.directAutoFire(context.Background(), p.ID, p.Title)
		expectFrame(t, frames, "first numbered action", "\r")
	}
	next := strings.Replace(menu, "2026-10-01", "2026-10-02", 1)
	seedGridMenu(t, p, next)
	appendDirectAutoMenuRedraw(t, p, next)
	a.directAutoFire(context.Background(), p.ID, p.Title)
	expectFrame(t, frames, "different numbered action", "\r")
}

func TestJevTransientFailureRecoversSamePendingPermission(t *testing.T) {
	a, p, frames := newJevDetectHarness(t, "always")
	a.metrics = NewMetrics()
	a.inFlightEvals = map[string]bool{}
	calls := 0
	a.jevClientFactory = func(string) jevSystemOneClient {
		return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("temporary provider failure")
			}
			return jevDetectYesResponse(false), nil
		}}
	}
	for range ariaSweeperSuspendThreshold {
		a.evaluateObservation(context.Background(), p.ID, p.Title, "")
	}
	expectNoFrame(t, frames, "provider failed")
	pos := atomic.LoadInt64(&p.LastAckedChoicePromptPos)
	if calls != 1 || !a.shouldSkipSweeper(p.ID, pos) {
		t.Fatalf("provider backoff not respected: calls=%d", calls)
	}
	a.mu.Lock()
	entry := a.jevDetectCache[p.ID]
	entry.expires = time.Now().Add(-time.Second)
	a.jevDetectCache[p.ID] = entry
	a.mu.Unlock()
	if a.shouldSkipSweeper(p.ID, pos) {
		t.Fatal("temporary failure permanently parked the same permission")
	}
	a.evaluateObservation(context.Background(), p.ID, p.Title, "")
	expectFrame(t, frames, "Jev provider recovered", "\r")
	if calls != 2 || a.metrics.AriaEvalFiredTotal.Load() != 1 {
		t.Fatalf("recovery calls=%d fires=%d", calls, a.metrics.AriaEvalFiredTotal.Load())
	}
	outcomes := a.recentEvalOutcomes(p.ID)
	if len(outcomes) == 0 || outcomes[0].Kind != "fired" || outcomes[0].Model != ariaJevModel {
		t.Fatalf("Jev fire attribution missing: %+v", outcomes)
	}
}
