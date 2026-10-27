package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAgentMessageReceiptShowsOnThePane(t *testing.T) {
	p := &Pane{ID: "pane-b", buffer: NewRingBuffer(64)}
	p.recordAgentMessage("pane-a", "#3 Codex", deliveryLeftInComposer, time.UnixMilli(5))
	if p.Snapshot().LastAgentMessage != nil {
		t.Fatal("a message left in the input box is the unsent chip's, not a receipt")
	}
	p.recordAgentMessage("pane-a", "", deliverySubmitted, time.UnixMilli(5))
	if p.Snapshot().LastAgentMessage != nil {
		t.Fatal("only a message from an agent pane (with a sender) is recorded")
	}
	p.recordAgentMessage("pane-a", "#3 Codex", deliverySubmitted, time.UnixMilli(7))
	got := p.Snapshot().LastAgentMessage
	if got == nil || got.FromPaneID != "pane-a" || got.FromLabel != "#3 Codex" || got.At != 7 || got.Outcome != "submitted" {
		t.Fatalf("receipt = %+v", got)
	}
	got.FromLabel = "mutated"
	if p.Snapshot().LastAgentMessage.FromLabel != "#3 Codex" {
		t.Fatal("snapshots must hold a copy")
	}
	raw, _ := json.Marshal(makePaneDTO(p, nil, nil))
	if !strings.Contains(string(raw), `"lastAgentMessage":{"fromPaneId":"pane-a","fromLabel":"#3 Codex"`) {
		t.Fatalf("pane DTO = %s", raw)
	}
	if persisted, _ := json.Marshal(persistedFromSnapshot(p.Snapshot())); strings.Contains(string(persisted), "lastAgentMessage") {
		t.Fatal("receipts are recent activity, never saved with the pane")
	}
}
