package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestToolSendInputAutonomousHoldsWhileUserIsTyping is the cautious/LLM-path
// twin of TestDirectAutoFireHoldsWhileUserIsTyping. The state and
// prompt-identity guards both pass here, so only the typing hold can stop the
// write. The pane has no agent connection, so a hold that failed to trigger
// surfaces as a write error rather than a pass.
func TestToolSendInputAutonomousHoldsWhileUserIsTyping(t *testing.T) {
	t.Parallel()
	p := testPaneWithTail(t, "pane_hold", "Pane hold", strings.Join([]string{
		"Would you like to proceed?",
		"› 1. Yes",
		"  2. No",
	}, "\n"))
	if got := p.computeState(); got != StateChoicePrompt {
		t.Fatalf("computeState = %s, want choice-prompt", got)
	}
	currentPos := atomic.LoadInt64(&p.LastAckedChoicePromptPos)
	if currentPos == 0 {
		t.Fatal("LastAckedChoicePromptPos was not set")
	}
	// The user is mid-line. Stamped directly: this pane has no agent
	// connection, so Pane.Write cannot be used to fill the recorder here.
	atomic.StoreInt64(&p.LastInputMs, time.Now().UnixMilli())

	a := &Aria{mgr: testManagerWithPane(p), uiBus: newUIBus(), confirms: map[string]*ariaPendingConfirm{}}
	events := make(chan map[string]any, 8)
	done := make(chan string, 1)
	go func() {
		done <- a.toolSendInput(
			context.Background(),
			"tu_hold",
			p.ID,
			"1\n",
			currentPos,
			choicePromptFingerprint(tailOfPane(p, 30)),
			false,
			func(evt map[string]any) { events <- evt },
		)
	}()
	confirmID := waitNeedsConfirm(t, events, "send_input")
	if !a.signalConfirm(confirmID, true, false) {
		t.Fatal("signalConfirm returned false")
	}
	got := <-done
	if !strings.Contains(got, "the user is typing") {
		t.Fatalf("toolSendInput = %q, want a typing hold", got)
	}

	var sawHold bool
	for len(events) > 0 {
		evt := <-events
		if t_, _ := evt["type"].(string); t_ == "auto-hold" {
			sawHold = true
			if reason, _ := evt["reason"].(string); reason != "user typing" {
				t.Fatalf("auto-hold reason = %q, want \"user typing\"", reason)
			}
		}
	}
	if !sawHold {
		t.Fatal("no auto-hold event; evaluateObservation would count this toward the sweeper-suspend streak")
	}
}

// TestToolSendInputQuietPaneIsNotHeld is the negative case: same pane, same
// choice prompt, no recent input. It must reach the write (and fail there, the
// pane has no agent connection) rather than being deferred. Without it the
// hold test above would pass just as happily against a hold that never
// releases.
func TestToolSendInputQuietPaneIsNotHeld(t *testing.T) {
	t.Parallel()
	p := testPaneWithTail(t, "pane_quiet", "Pane quiet", strings.Join([]string{
		"Would you like to proceed?",
		"› 1. Yes",
		"  2. No",
	}, "\n"))
	if got := p.computeState(); got != StateChoicePrompt {
		t.Fatalf("computeState = %s, want choice-prompt", got)
	}
	currentPos := atomic.LoadInt64(&p.LastAckedChoicePromptPos)
	atomic.StoreInt64(&p.LastInputMs, time.Now().Add(-time.Hour).UnixMilli())

	a := &Aria{mgr: testManagerWithPane(p), uiBus: newUIBus(), confirms: map[string]*ariaPendingConfirm{}}
	events := make(chan map[string]any, 8)
	done := make(chan string, 1)
	go func() {
		done <- a.toolSendInput(
			context.Background(),
			"tu_quiet",
			p.ID,
			"1\n",
			currentPos,
			choicePromptFingerprint(tailOfPane(p, 30)),
			false,
			func(evt map[string]any) { events <- evt },
		)
	}()
	confirmID := waitNeedsConfirm(t, events, "send_input")
	if !a.signalConfirm(confirmID, true, false) {
		t.Fatal("signalConfirm returned false")
	}
	got := <-done
	if strings.Contains(got, "the user is typing") {
		t.Fatalf("quiet pane was held: %q", got)
	}
	if !strings.Contains(got, "not connected") {
		t.Fatalf("toolSendInput = %q, want it to reach the write", got)
	}
}
