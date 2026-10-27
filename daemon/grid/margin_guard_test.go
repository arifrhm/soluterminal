package grid

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// These tests pin the fix for the post-update "terminal rendering glitch":
// the vt library stores DECSTBM/DECSLRM margins UNCLAMPED, so a pane's
// ring-buffer history recorded while the PTY was taller (margins for 39 rows)
// replayed into a smaller emulator (30 rows, stale panes.json dims at
// AttachExisting) panicked InsertLineArea on the first reverse-index —
// "index out of range [38] with length 30", recovered but leaving the region
// poisoned: every later Feed re-panicked and Snapshot served the garbled
// screen to every reattaching client. See registerMarginGuards.

// countPanics swaps in an OnPanic counter for the duration of the test.
func countPanics(t *testing.T) *int {
	t.Helper()
	n := 0
	prev := OnPanic
	OnPanic = func(string, any) { n++ }
	t.Cleanup(func() { OnPanic = prev })
	return &n
}

// TestUpstreamDECSTBMUnclampedStillPanics is the CANARY against the raw
// upstream emulator (no guards): margins beyond the screen height followed by
// a reverse-index at the top of the region must still panic. When a dependency
// bump makes this test FAIL, upstream fixed the clamp and registerMarginGuards
// (plus this canary) can be retired.
func TestUpstreamDECSTBMUnclampedStillPanics(t *testing.T) {
	t.Parallel()
	raw := vt.NewEmulator(80, 30)
	panicked := false
	func() {
		defer func() { panicked = recover() != nil }()
		// Margins for a 39-row screen on a 30-row emulator, then ESC M at the
		// top of the region → ScrollDown → InsertLineArea walks Lines[38].
		_, _ = raw.Write([]byte("\x1b[1;39r\x1bM"))
	}()
	if !panicked {
		t.Fatal("upstream vt no longer panics on unclamped DECSTBM — " +
			"the clamp was fixed upstream; retire registerMarginGuards and this canary")
	}
}

// TestDECSTBMBeyondHeightIsIgnored: the exact production stream shape must not
// panic through our guarded emulator, and the emulator must stay functional.
func TestDECSTBMBeyondHeightIsIgnored(t *testing.T) {
	panics := countPanics(t)
	e := New(80, 30)
	defer e.Close()

	// The production shape: taller-epoch margins + reverse index, then app
	// output continues.
	e.Feed([]byte("\x1b[1;39r\x1bM"))
	e.Feed([]byte("\x1b[2J\x1b[HX"))

	if *panics != 0 {
		t.Fatalf("guarded emulator panicked %d times on out-of-range DECSTBM", *panics)
	}
	if got := cellChar(e.Snapshot(), 0, 0); got != "X" {
		t.Fatalf("emulator not functional after ignored DECSTBM: cell(0,0)=%q, want X", got)
	}
}

// TestDECSTBMValidStillDelegates: an in-range DECSTBM must reach the built-in
// handler — observable via its cursor-home side effect and confined scrolling.
func TestDECSTBMValidStillDelegates(t *testing.T) {
	panics := countPanics(t)
	e := New(80, 30)
	defer e.Close()

	// Park the cursor away from home, then set valid margins: the built-in
	// homes the cursor; the guard (had it swallowed) would leave it parked.
	e.Feed([]byte("\x1b[10;10H\x1b[5;20r"))
	snap := e.Snapshot()
	if snap.CursorX != 0 || snap.CursorY != 0 {
		t.Fatalf("valid DECSTBM did not reach the built-in handler: cursor=(%d,%d), want (0,0)",
			snap.CursorX, snap.CursorY)
	}
	// And omitted-bottom form (CSI r — reset margins) must also delegate.
	e.Feed([]byte("\x1b[3;3H\x1b[r"))
	snap = e.Snapshot()
	if snap.CursorX != 0 || snap.CursorY != 0 {
		t.Fatalf("CSI r (reset margins) did not delegate: cursor=(%d,%d)", snap.CursorX, snap.CursorY)
	}
	if *panics != 0 {
		t.Fatalf("unexpected panics: %d", *panics)
	}
}

// TestDECSLRMBeyondWidthIsIgnored: same class, horizontal margins (needs
// ModeLeftRightMargin), and the param-less SCOSC save/restore must keep
// working — the guard keys off the explicit out-of-range second param.
func TestDECSLRMBeyondWidthIsIgnored(t *testing.T) {
	panics := countPanics(t)
	e := New(80, 24)
	defer e.Close()

	// Enable DECSLRM mode, set margins beyond the width, then scroll — the
	// unguarded library would store right=200 and walk cells out of range.
	e.Feed([]byte("\x1b[?69h\x1b[1;200s\x1bM"))
	if *panics != 0 {
		t.Fatalf("guarded emulator panicked %d times on out-of-range DECSLRM", *panics)
	}

	// Param-less 's' (SCOSC when the mode is off) must be TRANSPARENT: the
	// guarded emulator must end in exactly the state the raw upstream
	// emulator ends in for the same bytes (we pin equivalence, not upstream's
	// particular save/restore semantics).
	scosc := []byte("\x1b[?69l\x1b[6;6H\x1b[s\x1b[1;1H\x1b[u")
	raw := vt.NewEmulator(80, 24)
	_, _ = raw.Write(scosc)
	e.Feed(scosc)
	snap := e.Snapshot()
	if want := raw.CursorPosition(); snap.CursorX != want.X || snap.CursorY != want.Y {
		t.Fatalf("guard changed param-less 's' semantics: cursor=(%d,%d), raw upstream=(%d,%d)",
			snap.CursorX, snap.CursorY, want.X, want.Y)
	}
}

// TestFeedPanicSelfHeals: ANY recovered vt.Write panic must leave a coherent,
// usable emulator — not the poisoned state that re-panics on every subsequent
// Feed and serves a garbled Snapshot to reattaching clients. A synthetic
// always-panic CSI handler stands in for whatever the library's next sharp
// edge is.
func TestFeedPanicSelfHeals(t *testing.T) {
	panics := countPanics(t)
	e := New(80, 24)
	defer e.Close()

	e.vt.RegisterCsiHandler('z', func(ansi.Params) bool { panic("synthetic vt bug") })

	// Enter the alt screen, then hit the panic: the self-heal RIS must land
	// us back on a coherent primary screen with the cached alt flag realigned.
	e.Feed([]byte("\x1b[?1049h"))
	if !e.AltScreen() {
		t.Fatal("test setup: alt screen not engaged")
	}
	e.Feed([]byte("\x1b[z"))
	if *panics != 1 {
		t.Fatalf("expected exactly 1 recovered panic, got %d", *panics)
	}
	if e.AltScreen() {
		t.Fatal("alt flag stale after self-heal RIS (Snapshot would mislead subscribers)")
	}

	// The emulator must be fully usable afterwards.
	e.Feed([]byte("ok"))
	snap := e.Snapshot()
	if got := cellChar(snap, 0, 0) + cellChar(snap, 1, 0); got != "ok" {
		t.Fatalf("emulator unusable after self-heal: row0 starts %q, want \"ok\"", got)
	}
	if *panics != 1 {
		t.Fatalf("later Feeds re-panicked after self-heal: %d total", *panics)
	}
}
