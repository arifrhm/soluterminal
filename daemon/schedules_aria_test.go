package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestComposeAriaReport(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 18, 7, 0, 0, 0, time.Local)
	run := ScheduleRun{At: at, Summary: "summary line"}

	short := composeAriaReport("Nightly triage", run, "## Needs you\nnothing today")
	if !strings.HasPrefix(short, "📋 Nightly triage — ") || !strings.Contains(short, "nothing today") {
		t.Fatalf("short report = %q", short)
	}

	// Oversized log → truncated under the gateway cap, with the note.
	long := composeAriaReport("Nightly triage", run, strings.Repeat("x", ariaSendMaxChars*2))
	if len(long) > ariaSendMaxChars {
		t.Fatalf("len = %d, want ≤ %d", len(long), ariaSendMaxChars)
	}
	if !strings.Contains(long, "truncated — the full report is in soluterminal") {
		t.Fatalf("missing truncation note: %q", long[len(long)-120:])
	}

	// No log → summary fallback; nothing at all → explicit placeholder.
	if got := composeAriaReport("N", run, ""); !strings.Contains(got, "summary line") {
		t.Fatalf("summary fallback missing: %q", got)
	}
	if got := composeAriaReport("N", ScheduleRun{At: at}, ""); !strings.Contains(got, "no output") {
		t.Fatalf("empty placeholder missing: %q", got)
	}

	// A pathological schedule name (unbounded server-side) must not drive a
	// negative slice / panic — it clamps to the cap.
	huge := composeAriaReport(strings.Repeat("N", ariaSendMaxChars+1000), run, "body")
	if len(huge) > ariaSendMaxChars {
		t.Fatalf("huge-name report len = %d, want ≤ %d", len(huge), ariaSendMaxChars)
	}
}

func TestDeliverChatValidation(t *testing.T) {
	t.Parallel()
	future := time.Now().Add(time.Hour)
	base := ScheduleInput{Name: "x", RunAt: &future, Kind: "task", Prompt: "p", Cwd: t.TempDir(), Permission: "plan"}

	ok := base
	ok.DeliverChat = &AriaChatRef{Source: "wa", ChatID: "c1"}
	if err := ok.validate(); err != nil {
		t.Fatalf("valid deliverChat rejected: %v", err)
	}

	missing := base
	missing.DeliverChat = &AriaChatRef{Source: "wa"}
	if err := missing.validate(); err == nil {
		t.Fatal("missing chatId accepted")
	}

	wrongKind := ScheduleInput{Name: "x", RunAt: &future, Kind: "prompt", PaneID: "p1", Prompt: "p"}
	wrongKind.DeliverChat = &AriaChatRef{Source: "wa", ChatID: "c1"}
	if err := wrongKind.validate(); err == nil {
		t.Fatal("deliverChat on prompt-kind accepted")
	}
}

func testScheduleStoreWithRun(t *testing.T, run ScheduleRun) (*ScheduleStore, string) {
	t.Helper()
	s, err := NewScheduleStore(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sched := &Schedule{ID: "sch_test", Name: "Nightly triage", Kind: "task",
		DeliverChat: &AriaChatRef{Source: "wa", ChatID: "c1", ChatName: "family"},
		Runs:        []ScheduleRun{run}}
	s.mu.Lock()
	s.items[sched.ID] = sched
	s.order = append(s.order, sched.ID)
	s.mu.Unlock()
	return s, sched.ID
}

func TestDeliverRunToAriaChat(t *testing.T) {
	var sent struct {
		Source string `json:"source"`
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	}
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/send" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testAgentKey {
			t.Errorf("auth = %q", got)
		}
		_ = json.NewDecoder(r.Body).Decode(&sent)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer gw.Close()

	run := ScheduleRun{At: time.Now().Round(time.Second), Status: "fired", Summary: "all green", Detail: "ran"}
	s, schedID := testScheduleStoreWithRun(t, run)
	sup := testSupervisor(t)
	sup.httpc = gw.Client()
	_ = sup.store.set(ariaResponderConfig{Agents: []ariaAgentConfig{
		{ID: ariaDefaultAgentID, AgentKey: testAgentKey, GatewayURL: gw.URL},
	}})
	s.SetAriaResponder(sup)

	s.deliverRunToAriaChat(schedID, run, "Nightly triage", AriaChatRef{Source: "wa", ChatID: "c1", ChatName: "family"})
	if sent.Source != "wa" || sent.ChatID != "c1" || !strings.Contains(sent.Text, "all green") {
		t.Fatalf("sent = %+v", sent)
	}
	got, _ := s.Get(schedID)
	if !strings.Contains(got.Runs[0].Detail, "chat ✓ wa/family") {
		t.Fatalf("run detail = %q", got.Runs[0].Detail)
	}
}

func TestDeliverRunToAriaChatFailureStamps(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"ok":false,"error":"daily cap"}`, http.StatusTooManyRequests)
	}))
	defer gw.Close()

	run := ScheduleRun{At: time.Now().Round(time.Second), Status: "fired", Summary: "s"}
	s, schedID := testScheduleStoreWithRun(t, run)
	sup := testSupervisor(t)
	sup.httpc = gw.Client()
	_ = sup.store.set(ariaResponderConfig{Agents: []ariaAgentConfig{
		{ID: ariaDefaultAgentID, AgentKey: testAgentKey, GatewayURL: gw.URL},
	}})
	s.SetAriaResponder(sup)

	s.deliverRunToAriaChat(schedID, run, "N", AriaChatRef{Source: "wa", ChatID: "c1"})
	got, _ := s.Get(schedID)
	if !strings.Contains(got.Runs[0].Detail, "chat ✗ wa/c1") {
		t.Fatalf("run detail = %q", got.Runs[0].Detail)
	}

	// No agent key at all → same loud stamp, no panic.
	_ = sup.store.set(ariaResponderConfig{})
	s.deliverRunToAriaChat(schedID, run, "N", AriaChatRef{Source: "wa", ChatID: "c1"})
	got, _ = s.Get(schedID)
	if !strings.Contains(got.Runs[0].Detail, "chat ✗") {
		t.Fatalf("run detail after keyless = %q", got.Runs[0].Detail)
	}
}
