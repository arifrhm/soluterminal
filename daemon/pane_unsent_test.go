package main

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestUnsentMessageRecoveryChecksIdentityAndDraft(t *testing.T) {
	for _, tc := range []struct {
		name, action, draft, id string
		edited                  bool
		status                  int
		keys                    string
	}{
		{"send once", "send", "Please review this", "receipt", false, 200, "\r"},
		{"clear exact", "clear", "Please review this", "receipt", false, 200, "\x01\x0b"},
		{"different text", "clear", "My own draft", "receipt", false, 409, ""},
		{"extra space", "clear", "Please  review this", "receipt", false, 409, ""},
		{"extra suffix", "clear", "Please review this and more", "receipt", false, 409, ""},
		{"paste hidden", "clear", "[Pasted Content 1024 chars]", "receipt", false, 409, ""},
		{"edited", "clear", "Please review this", "receipt", true, 409, ""},
		{"old receipt", "send", "Please review this", "old", false, 409, ""},
		{"empty box", "send", "", "receipt", false, 409, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, frames := newTypingHoldPane(t, "unsent")
			p.Agent = agentCodex
			p.unsentMessage = &unsentPaneMessage{ID: "receipt", Sender: "#5 Claude", body: "Please review this", connection: p.agentConn}
			if tc.edited {
				p.inputRevision.Add(1)
			}
			var screen atomic.Value
			screen.Store(deliveryCodexScreen(tc.draft))
			stubDeliveryScreen(t, &screen)
			a := &apiServer{mgr: testManagerWithPane(p)}
			req := httptest.NewRequest("POST", "/api/panes/unsent/unsent-message", strings.NewReader(fmt.Sprintf(`{"id":%q,"action":%q}`, tc.id, tc.action)))
			req.SetPathValue("id", p.ID)
			rec := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { a.actOnUnsentMessage(rec, req); close(done) }()
			if tc.keys != "" {
				expectFrame(t, frames, "recovery keys", tc.keys)
				screen.Store(deliveryCodexScreen(""))
			}
			<-done
			if rec.Code != tc.status {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if tc.status == 200 && tc.action == "clear" && !strings.Contains(rec.Body.String(), `"outcome":"cleared"`) {
				t.Fatal(rec.Body.String())
			}
			if tc.status == 200 && p.Snapshot().UnsentMessage != nil {
				t.Fatal("confirmed recovery retained chip")
			}
			if tc.status != 200 && p.unsentMessage == nil {
				t.Fatal("refused action lost receipt")
			}
			expectNoFrame(t, frames, "no retries or deletion on conflict")
		})
	}
}

func TestUnsentMessageSendStaysRecordedWhenStillInComposer(t *testing.T) {
	p, frames := newTypingHoldPane(t, "stuck-recovery")
	p.Agent = agentCodex
	p.unsentMessage = &unsentPaneMessage{ID: "receipt", Sender: "#2 Codex", body: "Still in composer", connection: p.agentConn}
	var screen atomic.Value
	screen.Store(deliveryCodexScreen("Still in composer"))
	stubDeliveryScreen(t, &screen)
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"id":"receipt","action":"send"}`))
	req.SetPathValue("id", p.ID)
	rec := httptest.NewRecorder()
	(&apiServer{mgr: testManagerWithPane(p)}).actOnUnsentMessage(rec, req)
	expectFrame(t, frames, "one Enter", "\r")
	expectNoFrame(t, frames, "never auto retry")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "left_in_composer") || p.Snapshot().UnsentMessage.revision != p.inputRevision.Load() {
		t.Fatalf("receipt: %s %+v", rec.Body.String(), p.Snapshot().UnsentMessage)
	}
}

func TestExactComposerLineBothProviders(t *testing.T) {
	if !composerShowsExactLine(strings.Replace(deliveryClaudeScreen("Exact input text"), "❯ ", "❯\u00a0", 1), false, "Exact input text") {
		t.Fatal("Claude NBSP separator missed")
	}
	for _, codex := range []bool{false, true} {
		screen := deliveryClaudeScreen("Exact input text")
		if codex {
			screen = deliveryCodexScreen("Exact input text")
		}
		if !composerShowsExactLine(screen, codex, "Exact input text") {
			t.Fatalf("exact text not recognized, codex=%v", codex)
		}
		for _, body := range []string{"Exactinputtext", "Exact input", "Exact input text ", "Exact input\ntext"} {
			if composerShowsExactLine(screen, codex, body) {
				t.Fatalf("unsafe match %q", body)
			}
		}
	}
}

func TestManualInputSupersedesUnsentReceiptWithoutClearingText(t *testing.T) {
	p, frames := newTypingHoldPane(t, "manual-recovery")
	p.unsentMessage = &unsentPaneMessage{ID: "receipt", body: "Old message", connection: p.agentConn}
	if p.Snapshot().UnsentMessage == nil {
		t.Fatal("receipt missing")
	}
	if _, err := p.Write([]byte("my own draft")); err != nil {
		t.Fatal(err)
	}
	expectFrame(t, frames, "manual text", "my own draft")
	if p.Snapshot().UnsentMessage != nil {
		t.Fatal("stale chip survived manual input")
	}
	expectNoFrame(t, frames, "no automatic clearing")
}

func TestUnsentReceiptDoesNotSurviveExitOrConnectionReplacement(t *testing.T) {
	p, _ := newTypingHoldPane(t, "replaced-receipt")
	p.unsentMessage = &unsentPaneMessage{ID: "receipt", connection: p.agentConn}
	p.Status = PaneExited
	if p.Snapshot().UnsentMessage != nil {
		t.Fatal("chip survived exit")
	}
	p.Status = PaneRunning
	p.agentConn = nil
	if p.Snapshot().UnsentMessage != nil {
		t.Fatal("chip survived connection replacement")
	}
}
