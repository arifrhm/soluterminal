package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Reproduce a manually answered Codex modal followed by another command.
// The first key is consumed without Enter; it must not become a phantom draft.
func TestTerminalPermissionShortcutDoesNotHoldNextApproval(t *testing.T) {
	for _, key := range []string{"y", "Y", "1", "2", "3", "p"} {
		t.Run(key, func(t *testing.T) {
			p, frames := newTypingHoldPane(t, "manual-shortcut-"+key)
			a := testAriaWithPane(t, p)
			a.sweepStreaks = map[string]*sweepStreak{}
			a.evalLog = newAriaEvalLog()
			seedGridMenu(t, p, codexNarrowCommandMenu("cat first.js"))
			if _, err := p.Write([]byte(key)); err != nil {
				t.Fatal(err)
			}
			expectFrame(t, frames, "manual menu key", key)
			if pending := p.composerPendingLen(); pending != 0 {
				t.Fatalf("manual key became a draft: %q", p.promptRec.buf)
			}
			// Let only the short typing protection settle, not the two-minute
			// draft expiry. A genuinely new command must then progress.
			atomic.StoreInt64(&p.LastInputMs, time.Now().Add(-ariaAutoTypingSettle-time.Second).UnixMilli())
			seedGridMenu(t, p, codexNarrowCommandMenu("cat second.js"))
			a.directAutoFire(context.Background(), p.ID, p.Title)
			expectFrame(t, frames, "next auto approval", "\r")
		})
	}
}

func TestTerminalPermissionShortcutPreservesRealDraft(t *testing.T) {
	p, frames := newTypingHoldPane(t, "real-draft-menu")
	if _, err := p.Write([]byte("actual unsent draft")); err != nil {
		t.Fatal(err)
	}
	expectFrame(t, frames, "draft", "actual unsent draft")
	_, recordedAt := p.composerPendingSnapshot()
	seedGridMenu(t, p, codexNarrowCommandMenu("cat first.js"))
	if _, err := p.Write([]byte("y")); err != nil {
		t.Fatal(err)
	}
	expectFrame(t, frames, "menu key", "y")
	if string(p.promptRec.buf) != "actual unsent draft" || p.promptRec.lastRecordedInputMs != recordedAt {
		t.Fatalf("menu key altered real draft: %+v", p.promptRec)
	}
	if held, reason := autoFireUserHold(p, time.Now().Add(ariaAutoTypingSettle+time.Second)); !held || !strings.HasPrefix(reason, "composer has text (") {
		t.Fatalf("real draft lost protection: held=%v reason=%q", held, reason)
	}
}

func TestTerminalPermissionShortcutKeepsOrdinaryTyping(t *testing.T) {
	for _, key := range []string{"y", "1", "p", "yes", "9", "real text"} {
		p, frames := newTypingHoldPane(t, "ordinary-"+key)
		if _, err := p.Write([]byte(key)); err != nil {
			t.Fatal(err)
		}
		expectFrame(t, frames, "ordinary typing", key)
		if string(p.promptRec.buf) != key {
			t.Fatalf("ordinary typing %q lost: %q", key, p.promptRec.buf)
		}
	}
}

func TestTerminalPermissionShortcutDoesNotConsumeQuestionInput(t *testing.T) {
	p, frames := newTypingHoldPane(t, "question-input")
	seedGridMenu(t, p, "Choose a deployment target\n› 1. Development\n  2. Production\n  Enter to select")
	if _, err := p.Write([]byte("1")); err != nil {
		t.Fatal(err)
	}
	expectFrame(t, frames, "question key", "1")
	if string(p.promptRec.buf) != "1" {
		t.Fatal("nonpermission menu was treated as an approval")
	}
}

func TestAttentionRefreshShowsAndClearsTerminalInputHold(t *testing.T) {
	p, frames := newTypingHoldPane(t, "hold-attention")
	a := testAriaWithPane(t, p)
	e := newAttentionEngine(nil)
	e.SetAria(a)
	if _, err := p.Write([]byte("real draft")); err != nil {
		t.Fatal(err)
	}
	expectFrame(t, frames, "draft", "real draft")
	atomic.StoreInt64(&p.LastInputMs, time.Now().Add(-ariaAutoTypingSettle-time.Second).UnixMilli())
	seedGridMenu(t, p, codexNarrowCommandMenu("cat first.js"))
	e.Refresh(p)
	if state, reason := p.AttentionNow(); state != AttentionAutoPending || !strings.HasPrefix(reason, "composer has text (") {
		t.Fatalf("hold not exposed: %s / %q", state, reason)
	}
	// Resolve only the draft, leaving the same menu waiting. Refresh must use
	// current input rather than carrying a historic held evaluation forward.
	p.agentWriteMu.Lock()
	p.promptRec.observe([]byte{0x15})
	p.agentWriteMu.Unlock()
	e.Refresh(p)
	if state, reason := p.AttentionNow(); state != AttentionAutoPending || reason != "" {
		t.Fatalf("old input hold persisted: %s / %q", state, reason)
	}
}

func TestTerminalPermissionShortcutPreservesSplitControl(t *testing.T) {
	p, frames := newTypingHoldPane(t, "split-control-menu")
	seedGridMenu(t, p, codexNarrowCommandMenu("cat first.js"))
	for _, chunk := range []string{"\x1b]10;", "y", "\x07", "real draft"} {
		if _, err := p.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
		expectFrame(t, frames, "split protocol", chunk)
	}
	if string(p.promptRec.buf) != "real draft" || p.promptRec.inStringControl {
		t.Fatalf("split control corrupted recorder: %+v", p.promptRec)
	}
}
