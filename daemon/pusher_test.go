package main

import (
	"testing"
	"time"
)

// drive feeds a sequence of (offset-from-start, cur-state) ticks to a
// graceTracker for one pane and returns the offsets (seconds) at which it said
// "push". prev is threaded automatically from the previous tick.
func drive(g *graceTracker, base time.Time, pane string, ticks []struct {
	sec int
	st  PaneRuntimeState
}) []int {
	var fired []int
	prev := StateIdle
	for _, tk := range ticks {
		now := base.Add(time.Duration(tk.sec) * time.Second)
		if g.eval(pane, tk.st, prev, now) == pushFire {
			fired = append(fired, tk.sec)
		}
		prev = tk.st
	}
	return fired
}

type tick = struct {
	sec int
	st  PaneRuntimeState
}

func TestGraceTracker(t *testing.T) {
	t.Parallel()
	const grace = 15 * time.Second
	base := time.Unix(1_700_000_000, 0) // fixed; startedAt = base

	cases := []struct {
		name  string
		ticks []tick
		want  []int // offsets where push should fire
	}{
		{
			// Auto-approval clears the prompt at 14s — inside the grace. Silent.
			name: "cleared within grace never fires",
			ticks: []tick{
				{10, StateChoicePrompt}, {12, StateChoicePrompt},
				{14, StateBusy}, {16, StateIdle},
			},
			want: nil,
		},
		{
			// Sustained wait past the grace fires exactly once.
			name: "sustained dwell fires once",
			ticks: []tick{
				{10, StateChoicePrompt}, {12, StateChoicePrompt},
				{24, StateChoicePrompt}, // dwell 14s — still within grace
				{26, StateChoicePrompt}, // dwell 16s — fire
				{28, StateChoicePrompt}, // already fired — silent
				{30, StateChoicePrompt},
			},
			want: []int{26},
		},
		{
			// Flicker out-and-back restarts the grace: the 13s re-entry means it
			// can't fire until ~28s, not 25s.
			name: "flicker restarts grace",
			ticks: []tick{
				{10, StateChoicePrompt},
				{12, StateBusy},         // left — reset
				{13, StateChoicePrompt}, // fresh entry, grace restarts here
				{25, StateChoicePrompt}, // dwell 12s — still within grace
				{29, StateChoicePrompt}, // dwell 16s — fire
			},
			want: []int{29},
		},
		{
			// awaiting-input (free-text wait) is gated the same way.
			name: "awaiting-input also gated",
			ticks: []tick{
				{10, StateAwaitingInput}, {26, StateAwaitingInput},
			},
			want: []int{26},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newGraceTracker(base, grace)
			got := drive(g, base, "pane_x", tc.ticks)
			if !equalInts(got, tc.want) {
				t.Fatalf("fired at %v, want %v", got, tc.want)
			}
		})
	}
}

// TestGraceTracker_StartupSuppression: a prompt already standing at daemon boot
// (within the 5s startup window) must stay silent forever — until it leaves and
// genuinely re-enters, which then fires normally.
func TestGraceTracker_StartupSuppression(t *testing.T) {
	t.Parallel()
	const grace = 15 * time.Second
	base := time.Unix(1_700_000_000, 0)
	g := newGraceTracker(base, grace)

	// Pre-existing at boot (t=2s, within 5s), held a long time (suppressed),
	// then clears and a genuine NEW prompt appears post-startup (fires).
	got := drive(g, base, "pane_boot", []tick{
		{2, StateChoicePrompt}, {4, StateChoicePrompt}, // within 5s startup window
		{20, StateChoicePrompt}, {40, StateChoicePrompt}, // dwell >15s but suppressed
		{42, StateIdle},         // leaves → suppression reset
		{44, StateChoicePrompt}, // fresh post-startup entry, grace starts here
		{60, StateChoicePrompt}, // dwell 16s → fire
	})
	if !equalInts(got, []int{60}) {
		t.Fatalf("startup-suppression sequence fired at %v, want [60]", got)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// driveFinish feeds a tick sequence to a finishTracker for one pane and returns
// the offsets (seconds) at which it said "finished". An optional engageAt set
// marks ticks where we also call markEngaged (an attention push fired).
func driveFinish(f *finishTracker, base time.Time, pane string, ticks []tick, engageAt map[int]bool) []int {
	var fired []int
	prev := StateIdle
	for _, tk := range ticks {
		now := base.Add(time.Duration(tk.sec) * time.Second)
		if f.eval(pane, tk.st, prev, now) {
			fired = append(fired, tk.sec)
		}
		if engageAt[tk.sec] {
			f.markEngaged(pane, now)
		}
		prev = tk.st
	}
	return fired
}

func TestFinishTracker(t *testing.T) {
	t.Parallel()
	const thresh = 5 * time.Second
	base := time.Unix(1_700_000_000, 0)

	t.Run("engaged pane fires on busy->idle past threshold", func(t *testing.T) {
		f := newFinishTracker(thresh)
		// pinged for attention at t=10, then busy 20→40, recent→idle at 42.
		got := driveFinish(f, base, "p", []tick{
			{10, StateChoicePrompt}, // attention (engage happens via engageAt)
			{20, StateBusy}, {30, StateBusy},
			{40, StateRecent}, {42, StateIdle}, // finished, busy ~20s ≥ 5s
		}, map[int]bool{10: true})
		if !equalInts(got, []int{42}) {
			t.Fatalf("fired at %v, want [42]", got)
		}
	})

	t.Run("NON-engaged pane never fires (background session)", func(t *testing.T) {
		f := newFinishTracker(thresh)
		got := driveFinish(f, base, "p", []tick{
			{20, StateBusy}, {30, StateBusy}, {40, StateRecent}, {42, StateIdle},
		}, nil) // never engaged
		if got != nil {
			t.Fatalf("background pane fired at %v, want never", got)
		}
	})

	t.Run("engaged but sub-threshold turn does not fire", func(t *testing.T) {
		f := newFinishTracker(thresh)
		got := driveFinish(f, base, "p", []tick{
			{10, StateChoicePrompt},
			{20, StateBusy}, {22, StateRecent}, {23, StateIdle}, // busy only 3s < 5s
		}, map[int]bool{10: true})
		if got != nil {
			t.Fatalf("sub-threshold fired at %v, want never", got)
		}
	})

	t.Run("fires once per engagement", func(t *testing.T) {
		f := newFinishTracker(thresh)
		got := driveFinish(f, base, "p", []tick{
			{10, StateChoicePrompt},
			{20, StateBusy}, {40, StateRecent}, {42, StateIdle}, // fire
			{50, StateBusy}, {70, StateRecent}, {72, StateIdle}, // not engaged again → silent
		}, map[int]bool{10: true})
		if !equalInts(got, []int{42}) {
			t.Fatalf("fired at %v, want [42] (one finish per engagement)", got)
		}
	})

	t.Run("expire drops stale engagement", func(t *testing.T) {
		f := newFinishTracker(thresh)
		f.markEngaged("p", base)
		f.expire(base.Add(2*time.Hour), time.Hour)
		// after expiry, a finish must NOT fire
		got := driveFinish(f, base.Add(2*time.Hour), "p", []tick{
			{20, StateBusy}, {40, StateRecent}, {42, StateIdle},
		}, nil)
		if got != nil {
			t.Fatalf("fired after engagement expired: %v", got)
		}
	})
}
