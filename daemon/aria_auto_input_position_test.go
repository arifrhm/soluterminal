package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A quiet modal can be answered before another PTY redraw arrives. The grid
// admission uses an offset just beyond the current output, so an input marker
// based only on output length fails to acknowledge that answer.
func TestCodexApprovalWithoutInterveningOutput(t *testing.T) {
	for _, answer := range []string{"auto", "1"} {
		t.Run(answer, func(t *testing.T) {
			p, frames := newTypingHoldPane(t, "quiet-modal-"+answer)
			p.Cols, p.Rows = 47, 56
			a := testAriaWithPane(t, p)
			a.sweepStreaks = map[string]*sweepStreak{}
			a.evalLog = newAriaEvalLog()
			menu := strings.Replace(codexNarrowCommandMenu("bash scripts/solu-test status"),
				"run the following command?", "run the following\ncommand?", 1)
			seedGridMenu(t, p, menu)
			assertDirectAutoPromptReady(t, p, "quiet permission")
			if answer == "auto" {
				a.directAutoFire(context.Background(), p.ID, p.Title)
				expectFrame(t, frames, "auto answer", "\r")
			} else {
				if _, err := p.Write([]byte(answer)); err != nil {
					t.Fatal(err)
				}
				expectFrame(t, frames, "manual answer", answer)
			}
			if input, ack := atomic.LoadInt64(&p.LastInputStrippedPos), atomic.LoadInt64(&p.LastAckedChoicePromptPos); input <= ack {
				t.Fatalf("successful answer was not acknowledged: input=%d ack=%d", input, ack)
			}
			if state := p.computeState(); state == StateChoicePrompt {
				t.Fatal("answered continuously visible menu was rearmed")
			}
			if pending := p.composerPendingLen(); pending != 0 {
				t.Fatalf("permission answer became a draft: %d bytes", pending)
			}
			atomic.StoreInt64(&p.LastInputMs, time.Now().Add(-ariaAutoTypingSettle-time.Second).UnixMilli())
			// An identical modal after a committed working frame is a new
			// request, even if a poll never observed that intermediate frame.
			for i := 0; i < 3; i++ {
				seedGridMenu(t, p, "• Working (esc to interrupt)")
				seedGridMenu(t, p, menu)
				assertDirectAutoPromptReady(t, p, "next identical permission")
				a.directAutoFire(context.Background(), p.ID, p.Title)
				expectFrame(t, frames, "next automatic answer", "\r")
				if state := p.computeState(); state == StateChoicePrompt {
					t.Fatal("identical answered menu was rearmed")
				}
			}
		})
	}
}

func TestQuietApprovalIgnoresTerminalReplies(t *testing.T) {
	p, frames := newTypingHoldPane(t, "quiet-modal-protocol")
	seedGridMenu(t, p, codexNarrowCommandMenu("bash scripts/solu-test status"))
	assertDirectAutoPromptReady(t, p, "permission before focus report")
	before := atomic.LoadInt64(&p.LastInputStrippedPos)
	if _, err := p.Write([]byte("\x1b[I")); err != nil {
		t.Fatal(err)
	}
	expectFrame(t, frames, "focus report", "\x1b[I")
	if after := atomic.LoadInt64(&p.LastInputStrippedPos); after != before {
		t.Fatalf("terminal reply acknowledged the menu: before=%d after=%d", before, after)
	}
	if state := p.computeState(); state != StateChoicePrompt {
		t.Fatalf("terminal reply resolved the permission: %s", state)
	}
}

// Two queued tool calls can replace one modal with the next in a single
// committed frame. Here the same command checks two different worktrees;
// only the displayed reason distinguishes the requests.
func TestCodexQueuedSameCommandApprovals(t *testing.T) {
	p, frames := newTypingHoldPane(t, "queued-status-approvals")
	p.Cols, p.Rows = 47, 56
	a := testAriaWithPane(t, p)
	a.sweepStreaks = map[string]*sweepStreak{}
	a.evalLog = newAriaEvalLog()
	first := strings.Replace(codexNarrowCommandMenu("bash scripts/solu-test status"),
		"Run the release checks?", "Confirm pane #2's verification\nrunner is stopped.", 1)
	first = strings.Replace(first, "run the following command?", "run the following\ncommand?", 1)
	second := strings.Replace(first, "pane #2", "pane #1", 1)
	for _, menu := range []string{first, second} {
		seedGridMenu(t, p, menu)
		assertDirectAutoPromptReady(t, p, "queued permission")
		a.directAutoFire(context.Background(), p.ID, p.Title)
		expectFrame(t, frames, "queued automatic answer", "\r")
		seedGridMenu(t, p, menu)
		if state := p.computeState(); state == StateChoicePrompt {
			t.Fatal("continuously visible answered request was rearmed")
		}
	}
}

func TestCodexCommandReasonIdentityIgnoresWrapping(t *testing.T) {
	menu := codexNarrowCommandMenu("bash scripts/solu-test status")
	menu = strings.Replace(menu, "Run the release checks?", "Confirm pane #1's verification runner is stopped.", 1)
	wrapped := strings.Replace(menu, "verification runner", "verification\n    runner", 1)
	if a, b := approvalPromptFingerprint(menu, true), approvalPromptFingerprint(wrapped, true); a == 0 || a != b {
		t.Fatalf("reason wrapping changed identity: %x != %x", a, b)
	}
}
