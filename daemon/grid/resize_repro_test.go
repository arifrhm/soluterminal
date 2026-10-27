package grid

import (
	"strings"
	"testing"
)

// Reproduces the "narrow-then-widen leaves ghost cells" bug the user is
// seeing in grid-mode. This test treats the emulator as the authoritative
// model and simulates ratatui's resize behavior: reset back-buffer to
// blank, then emit diffs only for non-default cells in the new layout.
//
// If the fix (inject clear on resize in alt-screen) works, the emulator's
// final snapshot at the widened size should match what ratatui intended to
// paint — not retain cells from the pre-resize size.

// cellChar extracts the rendered char at (x,y) — ' ' if empty/default.
func cellChar(s Snapshot, x, y int) string {
	if x < 0 || x >= s.Cols || y < 0 || y >= s.Rows {
		return "?"
	}
	c := s.Cells[y*s.Cols+x]
	if c.Content == "" {
		return " "
	}
	return c.Content
}

func rowAt(s Snapshot, y int) string {
	var b strings.Builder
	for x := 0; x < s.Cols; x++ {
		b.WriteString(cellChar(s, x, y))
	}
	return strings.TrimRight(b.String(), " ")
}

// Write a string at (x,y) — simulates one ratatui "emit diff" operation.
func writeAt(e *Emulator, x, y int, s string) {
	// CSI y+1 ; x+1 H  positions cursor (1-based)
	pos := []byte{0x1b, '['}
	pos = append(pos, []byte(rowCol(y+1, x+1))...)
	pos = append(pos, 'H')
	e.Feed(pos)
	e.Feed([]byte(s))
}

func rowCol(r, c int) string {
	return itoa(r) + ";" + itoa(c)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}

// Full scenario — emulator at 120, narrow to 60, widen to 120.
// At 120 initially: "AAA" at (0,5), "BBB" at (50,5), "CCC" at (100,5).
// At 60  we simulate ratatui's response: re-lays-out, emits only "XXX" at (10,5).
//                  Old "AAA","BBB","CCC" should be GONE from this snapshot.
// At 120 again we simulate ratatui's response: re-lays-out, emits only "YYY" at (80,5).
//                  Old "XXX" should be GONE. And there should be nothing else.
//
// The bug manifests if our emulator retains "XXX" at (10,5) after the final
// widen — or "AAA" leftovers if the narrow didn't clear properly.
func TestResizeGhostCellsAltScreen(t *testing.T) {
	t.Parallel()
	e := New(120, 30)
	defer e.Close()
	e.Feed([]byte("\x1b[?1049h"))
	if !e.AltScreen() {
		t.Fatal("alt-screen did not flip on ?1049h")
	}
	runResizeCycle(t, e)
}

// Repro of the real-world bug: codex on Linux doesn't enter alt-screen —
// it uses DECSTBM + sync-update in the normal buffer. If ResizeGrid gates
// its clear-inject on AltScreen() (what we had first), this test catches it.
func TestResizeGhostCellsNoAltScreen(t *testing.T) {
	t.Parallel()
	e := New(120, 30)
	defer e.Close()
	if e.AltScreen() {
		t.Fatal("fresh emulator should not be on alt-screen")
	}
	runResizeCycle(t, e)
}

func runResizeCycle(t *testing.T, e *Emulator) {
	t.Helper()

	writeAt(e, 0, 5, "AAA")
	writeAt(e, 50, 5, "BBB")
	writeAt(e, 100, 5, "CCC")

	s := e.Snapshot()
	if got := rowAt(s, 5); !strings.Contains(got, "AAA") || !strings.Contains(got, "BBB") || !strings.Contains(got, "CCC") {
		t.Fatalf("initial row 5 missing expected content: %q", got)
	}

	// Narrow. ResizeGrid's behavior: resize + UNCONDITIONAL clear.
	e.Resize(60, 30)
	e.Feed([]byte("\x1b[2J\x1b[H"))

	// Ratatui redraws — diff-only against its reset back-buffer.
	writeAt(e, 10, 5, "XXX")

	s = e.Snapshot()
	got := rowAt(s, 5)
	if !strings.Contains(got, "XXX") {
		t.Fatalf("after narrow: XXX missing from row 5: %q", got)
	}
	if strings.Contains(got, "AAA") {
		t.Fatalf("after narrow: AAA should be gone from row 5: %q", got)
	}

	// Widen — same fix pattern.
	e.Resize(120, 30)
	e.Feed([]byte("\x1b[2J\x1b[H"))
	writeAt(e, 80, 5, "YYY")

	s = e.Snapshot()
	got = rowAt(s, 5)
	if !strings.Contains(got, "YYY") {
		t.Fatalf("after widen: YYY missing from row 5: %q", got)
	}
	if strings.Contains(got, "XXX") {
		t.Fatalf("GHOST CELLS: XXX should be gone after widen but row 5 = %q", got)
	}
}
