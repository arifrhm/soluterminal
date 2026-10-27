package main

import (
	"testing"
	"time"
)

// native_backlog_test.go locks in the update-handoff backlog replay semantics
// (nativeChunk.replay). After a daemon restart / self-update, the successor
// reattaches and the agent replays its stdout ring as FrameReplay frames; the
// parser processes those lines AS LIVE STATE — that is what reconstructs a
// still-open approval (so a turn blocked on a permission request can complete
// after the update) and re-lights nativeInTurn — while suppressing
// child-writing side effects (the allowlist auto-allow), and clearing the
// ghost of any approval the PREDECESSOR daemon already answered (its
// tool_result appears later in the same backlog).

const (
	// An assistant text_delta — lights nativeInTurn.
	bkTextDelta = `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"working on it"}},"session_id":"sess-1","parent_tool_use_id":null,"uuid":"bk-u1"}`
	// The tool_result pairing fxNativeReq's tool_use_id (toolu_1): proof the
	// predecessor answered that approval and the tool ran.
	bkToolResult = `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_1","type":"tool_result","content":"done","is_error":false}]},"parent_tool_use_id":null,"session_id":"sess-1","uuid":"bk-u2"}`
)

// drainAgentFrames reads frames off the agent end until deadline, returning how
// many arrived. Used to assert NO control_response was written during a backlog
// replay (and to keep net.Pipe writes from blocking if one wrongly fires).
func drainAgentFrames(t *testing.T, agentEnd interface {
	SetReadDeadline(time.Time) error
	Read([]byte) (int, error)
}, wait time.Duration) int {
	t.Helper()
	n := 0
	_ = agentEnd.SetReadDeadline(time.Now().Add(wait))
	for {
		if _, _, err := ReadFrame(agentEnd); err != nil {
			return n
		}
		n++
	}
}

// TestBacklogReplay_ReParksOpenApproval is the update-safety cornerstone: a
// backlog whose tail is an UNANSWERED permission_request (claude is blocked on
// it across the daemon swap) must re-park the approval and re-light the
// in-turn flag on the successor — AND must NOT re-fire the allowlist
// auto-allow (the duplicate-control_response stdin hazard), even when the tool
// is allowlisted: the predecessor's original live pass already had its chance,
// and if the request is genuinely still open the USER (or ARIA) answers the
// re-parked card.
func TestBacklogReplay_ReParksOpenApproval(t *testing.T) {
	t.Parallel()
	p, agentEnd := nativePaneForApproval(t, "pane_backlog_repark")
	p.allowlistAddTool("Bash") // the suppression must beat even an allowlisted tool

	frameCount := make(chan int, 1)
	go func() { frameCount <- drainAgentFrames(t, agentEnd, 400*time.Millisecond) }()

	backlog := fxNativeInit + "\n" + bkTextDelta + "\n" + fxNativeReq + "\n"
	_ = p.consumeNativeChunk(nil, []byte(backlog), true)

	if got := <-frameCount; got != 0 {
		t.Fatalf("backlog replay wrote %d frame(s) to the child's stdin; auto-allow must be suppressed on backlog", got)
	}
	ap := p.peekNativePending("req-1")
	if ap == nil {
		t.Fatal("backlog permission_request was NOT re-parked — a turn blocked on approval would stall forever after an update")
	}
	if ap.ToolUseID != "toolu_1" || ap.ToolName != "Bash" {
		t.Fatalf("re-parked approval = %+v, want toolu_1/Bash", ap)
	}
	if !p.nativeInTurnNow() {
		t.Fatal("backlog assistant streaming did not re-light nativeInTurn — the pane would read idle mid-turn after an update")
	}
}

// TestBacklogReplay_ToolResultClearsResolvedGhost covers the answered-before-
// the-swap case: the predecessor daemon resolved the approval and the tool ran,
// so the backlog contains the permission_request FOLLOWED by its tool_result.
// The replayed request re-parks (indistinguishable at that point from a
// still-open one), and the replayed tool_result must then clear the ghost — no
// phantom approval card whose click would write a stale-RequestID response.
func TestBacklogReplay_ToolResultClearsResolvedGhost(t *testing.T) {
	t.Parallel()
	p, _ := nativePaneForApproval(t, "pane_backlog_ghost")

	backlog := fxNativeInit + "\n" + fxNativeReq + "\n" + bkToolResult + "\n"
	_ = p.consumeNativeChunk(nil, []byte(backlog), true)

	if p.hasNativePending() {
		t.Fatalf("resolved approval survived the backlog replay as a ghost: %+v", p.peekNativePending(""))
	}
	if got := p.nativePendingCount.Load(); got != 0 {
		t.Fatalf("nativePendingCount = %d, want 0 after ghost clear", got)
	}
}

// TestLiveToolResultClearsAnsweredElsewherePending is the same reconciliation
// on the LIVE path: during the ~600ms two-daemon update overlap (or with a
// second client), an approval this daemon parked can be answered elsewhere; the
// tool_result streaming by is authoritative proof and must clear our stale card.
func TestLiveToolResultClearsAnsweredElsewherePending(t *testing.T) {
	t.Parallel()
	p, _ := nativePaneForApproval(t, "pane_live_answered_elsewhere")

	p.processNativeLine([]byte(fxNativeReq), false, false)
	if !p.hasNativePending() {
		t.Fatal("setup: live permission_request did not park")
	}
	p.processNativeLine([]byte(bkToolResult), false, false)
	if p.hasNativePending() {
		t.Fatal("live tool_result for the parked tool_use did not clear the stale approval card")
	}
}

// TestBacklogReplay_SettledTurnReadsIdle: a backlog whose tail is a result
// (turn_end) — the turn finished before the swap — must leave the successor
// idle: in-turn false, nothing pending. The re-park + re-light are transient
// within the replay and the settle edge closes them.
func TestBacklogReplay_SettledTurnReadsIdle(t *testing.T) {
	t.Parallel()
	p, _ := nativePaneForApproval(t, "pane_backlog_settled")

	backlog := fxNativeInit + "\n" + bkTextDelta + "\n" + fxNativeReq + "\n" + bkToolResult + "\n" + fxNativeDone + "\n"
	_ = p.consumeNativeChunk(nil, []byte(backlog), true)

	if p.nativeInTurnNow() {
		t.Fatal("settled backlog left nativeInTurn true — the pane would read working forever after reattach")
	}
	if p.hasNativePending() {
		t.Fatal("settled backlog left a pending approval ghost")
	}
}
