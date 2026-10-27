package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"soluterminal/grid"
)

func appendDirectAutoMenuRedraw(t *testing.T, p *Pane, menu string) {
	t.Helper()
	redraw := "\n" + menu + "\n"
	if _, err := p.buffer.Write([]byte(redraw)); err != nil {
		t.Fatalf("buffer.Write redraw: %v", err)
	}
	atomic.AddInt64(&p.TotalStrippedChars, int64(len(stripANSI([]byte(redraw)))))
}

func assertDirectAutoPromptReady(t *testing.T, p *Pane, step string) {
	t.Helper()
	if got := p.computeState(); got != StateChoicePrompt {
		t.Fatalf("%s computeState = %q, want %q", step, got, StateChoicePrompt)
	}
	state, pos, fp, ok := currentChoicePromptIdentity(p, "")
	if !ok || state != StateChoicePrompt || pos == 0 || fp == 0 {
		t.Fatalf("%s identity = (state=%q pos=%d fp=%d ok=%v), want stable choice prompt", step, state, pos, fp, ok)
	}
}

func newDirectAutoRepeatHarness(t *testing.T, id string) (*Aria, *Pane, <-chan directAutoFrameRead, string) {
	t.Helper()
	p, frames := newTypingHoldPane(t, id)
	a := testAriaWithPane(t, p)
	a.sweepStreaks = map[string]*sweepStreak{}
	a.evalLog = newAriaEvalLog()
	menu := strings.Join([]string{
		"Do you want to proceed?",
		"❯ 1. Yes",
		"  2. Yes, and don't ask again for git commit commands",
		"  3. No, and tell Codex what to do differently (esc)",
	}, "\n")
	seedGridMenu(t, p, menu)
	return a, p, frames, menu
}

func TestDirectAutoFireBoundsRepeatOnUnchangedObservation(t *testing.T) {
	t.Parallel()
	if ariaAutoUnchangedFireLimit != 2 {
		t.Fatalf("ariaAutoUnchangedFireLimit = %d, want 2", ariaAutoUnchangedFireLimit)
	}
	a, p, frames, menu := newDirectAutoRepeatHarness(t, "pane-direct-repeat-bound")
	attention := newAttentionEngine(a.mgr)
	attention.SetAria(a)
	a.attention = attention

	for i := 1; i <= ariaAutoUnchangedFireLimit; i++ {
		if i > 1 {
			// Production redraws append a fresh raw-buffer menu instance while the
			// reconstructed visible screen remains byte-for-byte unchanged.
			appendDirectAutoMenuRedraw(t, p, menu)
		}
		step := "unchanged fire " + string(rune('0'+i))
		assertDirectAutoPromptReady(t, p, step)
		a.directAutoFire(context.Background(), p.ID, p.Title)
		expectFrame(t, frames, step, "\r")
	}

	appendDirectAutoMenuRedraw(t, p, menu)
	assertDirectAutoPromptReady(t, p, "suppressed unchanged fire")
	allowed, reason := a.autoAllowed()
	firesBefore := len(a.autoFireTimes)
	if !allowed {
		t.Fatalf("autoAllowed before repeat suppression = false (%s)", reason)
	}
	eventsBefore := len(a.uiBus.Recent())
	a.directAutoFire(context.Background(), p.ID, p.Title)
	expectNoFrame(t, frames, "suppressed unchanged fire")
	allowed, reason = a.autoAllowed()
	if !allowed {
		t.Fatalf("autoAllowed after repeat suppression = false (%s)", reason)
	}
	if firesAfter := len(a.autoFireTimes); firesAfter != firesBefore {
		t.Fatalf("fire log changed across suppression: before=%d after=%d", firesBefore, firesAfter)
	}
	if got := a.BackoffStreak(p.ID); got != 0 {
		t.Fatalf("BackoffStreak after repeat suppression = %d, want 0", got)
	}
	for _, event := range a.uiBus.Recent()[eventsBefore:] {
		if event.Type == "aria-auto-suppressed" {
			t.Fatal("repeat bound published aria-auto-suppressed UI event")
		}
	}
	if att, reason := p.AttentionNow(); att != AttentionNeedsUser || reason == "" {
		t.Fatalf("attention after repeat exhaustion = %q/%q, want needs-user with reason", att, reason)
	}
	awaitingEvents := 0
	for _, event := range a.uiBus.Recent()[eventsBefore:] {
		if event.Type == "pane.awaiting_approval" {
			awaitingEvents++
		}
	}
	if awaitingEvents != 1 {
		t.Fatalf("pane.awaiting_approval events after repeat exhaustion = %d, want 1", awaitingEvents)
	}

	// Keep reproducing the production redraw loop after exhaustion. The raw
	// position advances again, invalidating ordinary position-scoped declines;
	// the stable observation bound must still surface the pane and must not spam
	// another awaiting-approval event.
	exhaustedPos := atomic.LoadInt64(&p.LastAckedChoicePromptPos)
	appendDirectAutoMenuRedraw(t, p, menu)
	assertDirectAutoPromptReady(t, p, "suppressed redraw after escalation")
	if pos := atomic.LoadInt64(&p.LastAckedChoicePromptPos); pos <= exhaustedPos {
		t.Fatalf("redraw position = %d, want > exhausted position %d", pos, exhaustedPos)
	}
	attention.Refresh(p) // production's 2s ticker can run between sweeps
	a.directAutoFire(context.Background(), p.ID, p.Title)
	expectNoFrame(t, frames, "suppressed redraw after escalation")
	if att, reason := p.AttentionNow(); att != AttentionNeedsUser || reason == "" {
		t.Fatalf("attention after advanced-position suppression = %q/%q, want needs-user with reason", att, reason)
	}
	awaitingEvents = 0
	for _, event := range a.uiBus.Recent()[eventsBefore:] {
		if event.Type == "pane.awaiting_approval" {
			awaitingEvents++
		}
	}
	if awaitingEvents != 1 {
		t.Fatalf("pane.awaiting_approval events across repeated suppressed redraws = %d, want 1", awaitingEvents)
	}
}

func TestDirectAutoFireRearmsOnNewObservation(t *testing.T) {
	t.Parallel()
	a, p, frames, menu := newDirectAutoRepeatHarness(t, "pane-direct-repeat-rearm")

	for i := 1; i <= ariaAutoUnchangedFireLimit; i++ {
		if i > 1 {
			appendDirectAutoMenuRedraw(t, p, menu)
		}
		assertDirectAutoPromptReady(t, p, "unchanged prompt")
		a.directAutoFire(context.Background(), p.ID, p.Title)
		expectFrame(t, frames, "unchanged prompt", "\r")
	}

	newMenu := strings.Join([]string{
		"Do you want to make this edit?",
		"❯ 1. Yes",
		"  2. Cancel the edit",
		"  3. Show diff",
	}, "\n")
	seedGridMenu(t, p, newMenu)
	assertDirectAutoPromptReady(t, p, "new observation")
	a.directAutoFire(context.Background(), p.ID, p.Title)
	expectFrame(t, frames, "new observation", "\r")
}

func paintReplayScreen(t *testing.T, p *Pane, screen string) {
	t.Helper()
	p.subsMu.Lock()
	if p.replayEmu == nil {
		p.replayEmu = grid.New(120, 30)
	}
	p.replayEmu.Feed([]byte("\x1b[2J\x1b[H" + strings.ReplaceAll(screen, "\n", "\r\n")))
	p.subsMu.Unlock()
}

// The direct-fire guard uses replayEmu in production, ahead of the optional
// ARIA grid. Pin both that source precedence and its normalization: rotating
// Claude spinner prose, timers, and token counters are chrome, while the
// actual approval question remains identity-bearing.
func TestAutoFireObservationIdentityUsesReplayScreen(t *testing.T) {
	t.Parallel()
	p, _ := newTypingHoldPane(t, "pane-direct-repeat-screen-source")

	menu := "Do you want to proceed?\n❯ 1. Yes\n  2. No, keep working"
	paintReplayScreen(t, p, menu+"\n✢ Investigating dependency graph… (12s · ↓ 465.9k tokens)")
	seedGridMenu(t, p, "Grid decoy one?\n❯ 1. Alpha\n  2. Beta")
	if _, ok := p.screenVisibleText(); !ok {
		t.Fatal("replay screen unavailable; test would exercise the grid fallback")
	}
	first := autoFireObservationIdentity(p, "tail decoy one")
	if first == 0 {
		t.Fatal("first replay-screen identity is zero")
	}

	paintReplayScreen(t, p, menu+"\n✽ Recalculating dependency graph… (13s · ↓ 466.0k tokens)")
	seedGridMenu(t, p, "Grid decoy two?\n❯ 1. Gamma\n  2. Delta")
	second := autoFireObservationIdentity(p, "tail decoy two")
	if second != first {
		t.Fatalf("replay identity changed across spinner/timer/token chrome: first=%d second=%d", first, second)
	}

	paintReplayScreen(t, p, "Do you want to delete the branch?\n❯ 1. Yes, delete it\n  2. No\n✽ Recalculating dependency graph… (14s · ↓ 467.1k tokens)")
	third := autoFireObservationIdentity(p, "tail decoy three")
	if third == second {
		t.Fatalf("replay identity did not change with real prompt content: both=%d", third)
	}
}
