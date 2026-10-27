package main

import (
	"strings"
	"testing"
	"time"
)

// An agent message typed into a pane showing an approval menu is read as menu
// keystrokes (it can select options, including persistent grants) and holds
// auto-approval as "composer has text". send_input refuses it unsent; a one-
// or two-key deliberate answer still goes through.
func TestSendInputRefusesMessagesIntoApprovalMenus(t *testing.T) {
	msg := "[from Codex] please review the delta\n"
	if reason := sendInputMenuCollisionFor("pane-menu", StateChoicePrompt, msg); !strings.Contains(reason, "NOT sent") {
		t.Fatalf("message into an approval menu was not refused: %q", reason)
	}
	for _, answer := range []string{"1", "1\n", "y\n", "2", "\n"} {
		if reason := sendInputMenuCollisionFor("pane-menu", StateChoicePrompt, answer); reason != "" {
			t.Errorf("deliberate menu answer %q refused: %s", answer, reason)
		}
	}
	for _, state := range []PaneRuntimeState{StateIdle, StateBusy, StateAwaitingInput} {
		if reason := sendInputMenuCollisionFor("pane-x", state, msg); reason != "" {
			t.Errorf("message to a %s pane refused: %s", state, reason)
		}
	}
}

func TestAutoApprovalDraftHoldNamesWhenItLifts(t *testing.T) {
	now := time.Now()
	draft := &Pane{ID: "draft", subs: map[int64]*subEntry{}}
	draft.promptRec.buf = []byte("half typed")
	draft.promptRec.lastRecordedInputMs = now.Add(-30 * time.Second).UnixMilli()
	hold, reason := autoFireUserHold(draft, now)
	want := now.Add(-30 * time.Second).Add(ariaAutoDraftStaleAfter).Local().Format("15:04")
	if !hold || !strings.Contains(reason, "waits until "+want) {
		t.Fatalf("draft hold should name when it lifts (%s…): hold=%v reason=%q", want, hold, reason)
	}
}
