package main

import (
	"strings"
	"testing"
)

// A live `system/task_notification` (background-shell completion) was being
// dropped as an unknown system subtype, so the client never learned a
// run_in_background bash had exited and the "waiting on N background shell"
// footer stuck forever. It must be bridged to the `<task-notification>`
// user_text the client's bg-shell tracker (nativeTranscript.ts) correlates on.
func TestTaskNotificationBridgedToUserText(t *testing.T) {
	t.Parallel()
	a := NewClaudeAdapter()
	// Real wire shape (captured from `claude -p --output-format stream-json`).
	line := `{"type":"system","subtype":"task_notification","task_id":"boz147vdb","tool_use_id":"toolu_01LrgNdnuMSGbRqp8jTLTFx9","status":"completed","output_file":"/tmp/x/boz147vdb.output","summary":"Background command \"Sleep for 3 seconds in background\" completed (exit code 0)"}`

	got := a.Feed([]byte(line))
	if len(got) != 1 || got[0].Type != EvUserText {
		t.Fatalf("task_notification must bridge to one EvUserText, got %+v", got)
	}
	txt := got[0].Text
	// Must be the <task-notification> form the client hides from rows AND uses to
	// correlate the exit: it needs the task id (== the launch's bg ID), a terminal
	// <status>, and the "exit code N" the summary carries.
	for _, want := range []string{
		"<task-notification>",
		"<task-id>boz147vdb</task-id>",
		"<status>completed</status>",
		"exit code 0",
	} {
		if !strings.Contains(txt, want) {
			t.Fatalf("bridged user_text missing %q:\n%s", want, txt)
		}
	}
	// The client hides it via a leading-`<task-notification` match, so it must
	// start with that (no stray prefix).
	if !strings.HasPrefix(strings.TrimSpace(txt), "<task-notification") {
		t.Fatalf("bridged text must start with <task-notification so the client hides it:\n%s", txt)
	}
}

// A malformed/empty task_notification must not panic or emit a bogus event.
func TestTaskNotificationNoIDDropped(t *testing.T) {
	t.Parallel()
	a := NewClaudeAdapter()
	if got := a.Feed([]byte(`{"type":"system","subtype":"task_notification"}`)); got != nil {
		t.Fatalf("task_notification with no task_id must drop, got %+v", got)
	}
}

// The MID-TURN delivery shape. system/task_notification only covers a PARKED
// agent; when a turn is in flight the harness injects the completion as a text
// block inside the next list-shaped user message, prefixed with a
// "[SYSTEM NOTIFICATION…]" preamble. mapUserToolResults used to keep only
// tool_result blocks, so these completions vanished and the "waiting on N
// background shells" footer stayed stale until a reconnect (observed live
// 2026-07-11: three of five shells' notifications missing from the pane ring —
// exactly the mid-turn ones). The text block must bridge to a user_text
// STARTING at <task-notification> (the client's hide-filter anchor), alongside
// the tool_result events, and unrelated text blocks must stay dropped.
func TestTaskNotificationMidTurnTextBlockBridged(t *testing.T) {
	t.Parallel()
	a := NewClaudeAdapter()
	line := `{"type":"user","message":{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"toolu_01AAA","content":"ok"},` +
		`{"type":"text","text":"[SYSTEM NOTIFICATION - NOT USER INPUT]\nThis is an automated background-task event.\n\n<task-notification>\n<task-id>bf6jor6si</task-id>\n<tool-use-id>toolu_01BBB</tool-use-id>\n<output-file>/tmp/x/bf6jor6si.output</output-file>\n<status>failed</status>\n<summary>Background command \"publish\" failed with exit code 1</summary>\n</task-notification>"},` +
		`{"type":"text","text":"unrelated reminder text — must not become a row"}]}}`

	got := a.Feed([]byte(line))
	if len(got) != 2 {
		t.Fatalf("want tool_result + bridged user_text, got %d: %+v", len(got), got)
	}
	var notif *NativeEvent
	var res *NativeEvent
	for i := range got {
		switch got[i].Type {
		case EvUserText:
			notif = &got[i]
		case EvToolResult:
			res = &got[i]
		}
	}
	if res == nil || res.CallID != "toolu_01AAA" {
		t.Fatalf("tool_result block must still map, got %+v", got)
	}
	if notif == nil {
		t.Fatalf("mid-turn <task-notification> text block must bridge to user_text, got %+v", got)
	}
	if !strings.HasPrefix(strings.TrimSpace(notif.Text), "<task-notification") {
		t.Fatalf("bridged text must start at <task-notification (preamble stripped) so the client hides it:\n%s", notif.Text)
	}
	for _, want := range []string{"<task-id>bf6jor6si</task-id>", "<status>failed</status>", "exit code 1"} {
		if !strings.Contains(notif.Text, want) {
			t.Fatalf("bridged user_text missing %q:\n%s", want, notif.Text)
		}
	}
}
