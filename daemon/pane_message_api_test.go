package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBroadcastEndpointVerifiesLongAndMultilineAgentDelivery(t *testing.T) {
	for _, body := range []string{strings.Repeat("long broadcast ", 80), "First line\nSecond line"} {
		p, frames := newTypingHoldPane(t, "broadcast")
		p.Agent = agentCodex
		var screen atomic.Value
		screen.Store(deliveryCodexScreen("[Pasted Content 1200 chars]"))
		stubDeliveryScreen(t, &screen)
		data, _ := json.Marshal(map[string]string{"text": body})
		req := httptest.NewRequest("POST", "/", strings.NewReader(string(data)))
		req.SetPathValue("id", p.ID)
		rec := httptest.NewRecorder()
		done := make(chan struct{})
		go func() { (&apiServer{mgr: testManagerWithPane(p)}).submitPaneMessage(rec, req); close(done) }()
		expectFrame(t, frames, "bracketed body", "\x1b[200~"+body+"\x1b[201~")
		expectFrame(t, frames, "first Enter", "\r")
		expectFrame(t, frames, "retry Enter", "\r")
		screen.Store(deliveryCodexScreen("Ask Codex to do anything"))
		<-done
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"outcome":"submitted"`) {
			t.Fatalf("response %d %s", rec.Code, rec.Body.String())
		}
		expectNoFrame(t, frames, "never retype")
	}
}

func TestBroadcastEndpointReportsUnsentAndPreservesRawInject(t *testing.T) {
	p, frames := newTypingHoldPane(t, "broadcast-stuck")
	p.Agent = agentCodex
	var screen atomic.Value
	screen.Store(deliveryCodexScreen("Broadcast stays here"))
	stubDeliveryScreen(t, &screen)
	a := &apiServer{mgr: testManagerWithPane(p)}
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"text":"Broadcast stays here"}`))
	req.SetPathValue("id", p.ID)
	rec := httptest.NewRecorder()
	a.submitPaneMessage(rec, req)
	expectFrame(t, frames, "body", "\x1b[200~Broadcast stays here\x1b[201~")
	expectFrame(t, frames, "Enter", "\r")
	expectFrame(t, frames, "second Enter", "\r")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "left_in_composer") || p.Snapshot().UnsentMessage.Sender != "you" {
		t.Fatalf("response %s", rec.Body.String())
	}

	req = httptest.NewRequest("POST", "/", strings.NewReader(`{"text":"raw\nbytes\r"}`))
	req.SetPathValue("id", p.ID)
	a.injectPane(httptest.NewRecorder(), req)
	expectFrame(t, frames, "raw inject stays raw", "raw\nbytes\r")
	expectNoFrame(t, frames, "no inject submit helper")
}

func TestBroadcastEndpointRejectsRawControlInput(t *testing.T) {
	p, frames := newTypingHoldPane(t, "bad-broadcast")
	p.Agent = agentCodex
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"text":"hello\u0003"}`))
	req.SetPathValue("id", p.ID)
	rec := httptest.NewRecorder()
	(&apiServer{mgr: testManagerWithPane(p)}).submitPaneMessage(rec, req)
	if rec.Code != 400 {
		t.Fatal(rec.Code)
	}
	expectNoFrame(t, frames, "control bytes refused")
}
