package main

import (
	"context"
	"testing"
	"time"
)

// productionAutoFirePacing holds the package defaults captured by TestMain
// before it disables pacing for ordinary fixtures.
var productionAutoFirePacing autoFirePacing

func pacedAria() *Aria {
	cfg := productionAutoFirePacing
	return &Aria{autoFirePacingOverride: &cfg}
}

func TestAutoFireHoldRules(t *testing.T) {
	cfg := productionAutoFirePacing
	if cfg.minAge == 0 || cfg.minGap == 0 || cfg.settle == 0 {
		t.Fatalf("production pacing not captured: %+v", cfg)
	}
	a := pacedAria()
	t0 := time.Now()

	// A newly rendered permission waits out its minimum on-screen age.
	if got := a.autoFireHold("p", 1, t0); got != cfg.minAge {
		t.Fatalf("new permission hold = %v, want %v", got, cfg.minAge)
	}
	if got := a.autoFireHold("p", 1, t0.Add(cfg.minAge)); got != 0 {
		t.Fatalf("aged permission hold = %v, want 0", got)
	}
	fired := t0.Add(cfg.minAge)
	a.markAutoFirePace("p", 1, fired)

	// The answered permission, or an unreadable screen, waits out the settle
	// window: right after Enter the raw tail can still hold the old menu.
	if got := a.autoFireHold("p", 1, fired.Add(50*time.Millisecond)); got != cfg.settle-50*time.Millisecond {
		t.Fatalf("same permission hold = %v", got)
	}
	// A menu only the raw tail still shows (nothing rendered) is the answered
	// one: hold it well past the settle window.
	if got := a.autoFireHold("p", 0, fired.Add(50*time.Millisecond)); got != cfg.unrendered-50*time.Millisecond {
		t.Fatalf("unrendered menu hold = %v", got)
	}

	// The next queued permission waits for both the gap after the last Enter
	// and its own minimum age, whichever ends later.
	next := fired.Add(100 * time.Millisecond)
	if got, want := a.autoFireHold("p", 2, next), max(cfg.minGap-100*time.Millisecond, cfg.minAge); got != want {
		t.Fatalf("queued permission hold = %v, want %v", got, want)
	}
	if got := a.autoFireHold("p", 2, fired.Add(cfg.minGap-time.Millisecond)); got != time.Millisecond {
		t.Fatalf("queued permission gap hold = %v, want 1ms", got)
	}
	if got := a.autoFireHold("p", 2, fired.Add(cfg.minGap)); got != 0 {
		t.Fatalf("queued permission after gap = %v, want 0", got)
	}

	// Pacing is per pane.
	if got := a.autoFireHold("other", 1, fired.Add(time.Millisecond)); got != cfg.minAge {
		t.Fatalf("other pane hold = %v, want %v", got, cfg.minAge)
	}
}

// A held fire re-evaluates itself when the hold ends instead of waiting for
// the 5s sweeper, and the queue position counter is not prompt identity.
func TestAutoFireHoldRetriesWithoutSweeper(t *testing.T) {
	a, p, frames, _ := newDirectAutoRepeatHarness(t, "pane-paced-retry")
	cfg := productionAutoFirePacing
	a.autoFirePacingOverride = &cfg
	p.AutoMode = "always"
	start := time.Now()
	a.evaluateObservation(context.Background(), p.ID, p.Title, "")
	expectNoFrame(t, frames, "fire before minimum age")
	expectFrame(t, frames, "paced retry", "\r")
	if elapsed := time.Since(start); elapsed < cfg.minAge || elapsed > cfg.minAge+700*time.Millisecond {
		t.Fatalf("paced fire after %v, want about %v", elapsed, cfg.minAge)
	}
}

func TestAutoFireSettleKeyIgnoresQueuePosition(t *testing.T) {
	dialog := func(position, command string) string {
		return "✻ Waiting for 3 background agents to finish\n\n" +
			"────────────────────────────────────────────────────────────\n" +
			" Bash command · from the general-purpose agent          " + position + "\n" +
			" " + command + "\n" +
			" Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel · Tab to amend"
	}
	key := func(text string) uint64 {
		p := testPaneWithTail(t, "pane-settle-key", "Settle key", "")
		p.ensureGrid()
		seedGridMenu(t, p, text)
		return autoFireSettleKey(p)
	}
	first := key(dialog("1 of 2", "printf a1 > /tmp/a1"))
	if first == 0 {
		t.Fatal("rendered Claude dialog has no settle key")
	}
	if got := key(dialog("1 of 3", "printf a1 > /tmp/a1")); got != first {
		t.Fatal("queue position growth changed the permission identity")
	}
	if got := key(dialog("1 of 2", "printf c1 > /tmp/c1")); got == first {
		t.Fatal("a different queued permission shared the identity")
	}
}
