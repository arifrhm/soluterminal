package main

import (
	"strings"
	"testing"

	"github.com/hinshun/vt10x"

	"soluterminal/grid"
)

// TestVTClearNormal — does \x1b[2J\x1b[H actually reset cells to
// default in vt10x's normal buffer? If this fails, my whole
// "clear vt on resize" approach from v0.0.177 is architecturally
// wrong, and 3+ releases were based on false assumption.
func TestVTClearNormal(t *testing.T) {
	t.Parallel()
	vt := vt10x.New(vt10x.WithSize(40, 10))
	// Some content.
	if _, err := vt.Write([]byte("Hello World")); err != nil {
		t.Fatalf("write: %v", err)
	}
	vt.Lock()
	if got := vt.Cell(0, 0).Char; got != 'H' {
		vt.Unlock()
		t.Fatalf("pre-clear (0,0) char = %q, want 'H'", got)
	}
	vt.Unlock()

	if _, err := vt.Write([]byte("\x1b[2J\x1b[H")); err != nil {
		t.Fatalf("clear write: %v", err)
	}
	vt.Lock()
	defer vt.Unlock()
	for y := 0; y < 10; y++ {
		for x := 0; x < 40; x++ {
			g := vt.Cell(x, y)
			if g.Char != ' ' && g.Char != 0 {
				t.Errorf("(%d,%d) after clear = %q (0x%x), want space/zero",
					x, y, g.Char, g.Char)
			}
		}
	}
}

// TestVTClearAltScreen — same check but after entering alt-screen
// via \x1b[?1049h. Codex lives here; if this differs from normal,
// vt10x's alt-screen clear path has its own story.
func TestVTClearAltScreen(t *testing.T) {
	t.Parallel()
	vt := vt10x.New(vt10x.WithSize(40, 10))
	if _, err := vt.Write([]byte("\x1b[?1049h")); err != nil {
		t.Fatalf("enter alt: %v", err)
	}
	if _, err := vt.Write([]byte("AltContent")); err != nil {
		t.Fatalf("write alt: %v", err)
	}
	vt.Lock()
	if got := vt.Cell(0, 0).Char; got != 'A' {
		vt.Unlock()
		t.Fatalf("pre-clear alt (0,0) char = %q, want 'A'", got)
	}
	vt.Unlock()

	if _, err := vt.Write([]byte("\x1b[2J\x1b[H")); err != nil {
		t.Fatalf("clear write: %v", err)
	}
	vt.Lock()
	defer vt.Unlock()
	for y := 0; y < 10; y++ {
		for x := 0; x < 40; x++ {
			g := vt.Cell(x, y)
			if g.Char != ' ' && g.Char != 0 {
				t.Errorf("alt (%d,%d) after clear = %q (0x%x), want space/zero",
					x, y, g.Char, g.Char)
			}
		}
	}
}

// TestVTResizeThenClear — sequence we actually run in pane.go
// Resize: alt-screen entered, content written, resize, then clear.
// This is the exact code path a user's stress-drag hits.
func TestVTResizeThenClear(t *testing.T) {
	t.Parallel()
	vt := vt10x.New(vt10x.WithSize(120, 30))
	// Enter alt-screen and paint widget-like content at various
	// positions including the "gray input row" area (bottom).
	seq := []byte("\x1b[?1049h" +
		"\x1b[5;10HTopContent" +
		"\x1b[28;5HBottomInputWidget" +
		"\x1b[28;40HMoreBottomStuff")
	if _, err := vt.Write(seq); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	// Sanity: content is there.
	vt.Lock()
	if got := vt.Cell(9, 4).Char; got != 'T' {
		vt.Unlock()
		t.Fatalf("pre-resize top content (9,4) = %q, want 'T'", got)
	}
	if got := vt.Cell(4, 27).Char; got != 'B' {
		vt.Unlock()
		t.Fatalf("pre-resize bottom content (4,27) = %q, want 'B'", got)
	}
	vt.Unlock()

	// Resize (mirrors what Pane.Resize does).
	vt.Resize(100, 30)
	// Then inject the clear we rely on.
	if _, err := vt.Write([]byte("\x1b[2J\x1b[H")); err != nil {
		t.Fatalf("clear: %v", err)
	}

	vt.Lock()
	defer vt.Unlock()
	// Every cell in the NEW grid should be default. If any cell
	// holds a non-space glyph, the clear isn't wiping properly and
	// codex's subsequent diff-emission would land on a dirty
	// grid — reproducing the ghost we've been chasing.
	cols, rows := vt.Size()
	if cols != 100 || rows != 30 {
		t.Fatalf("resize produced %dx%d, want 100x30", cols, rows)
	}
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			g := vt.Cell(x, y)
			if g.Char != ' ' && g.Char != 0 {
				t.Errorf("(%d,%d) after resize+clear = %q (0x%x)",
					x, y, g.Char, g.Char)
			}
		}
	}
}

// TestVTDiffAfterClear — the full end-to-end assumption: after we
// clear vt, subsequent writes at specific positions leave OTHER
// positions as default. This is what Ratatui's "emit only non-
// default cells after a resize" relies on.
func TestVTDiffAfterClear(t *testing.T) {
	t.Parallel()
	vt := vt10x.New(vt10x.WithSize(40, 10))
	vt.Write([]byte("\x1b[?1049h"))
	// Pre-resize state: some content all over the grid.
	vt.Write([]byte("\x1b[1;1HOld1\x1b[9;30HOld2"))
	// Our fix path: clear.
	vt.Write([]byte("\x1b[2J\x1b[H"))
	// Simulate codex's post-SIGWINCH diff emission: writes at
	// NEW positions only.
	vt.Write([]byte("\x1b[1;1HNew"))

	vt.Lock()
	defer vt.Unlock()
	// Did "Old1" at (0,0) get overwritten by "New"? Expected.
	if got := vt.Cell(0, 0).Char; got != 'N' {
		t.Errorf("(0,0) = %q, want 'N'", got)
	}
	// Did "Old2" at (29,8) get cleared? This is the critical
	// assertion — if it's still 'O', the ghost cell persists.
	if got := vt.Cell(29, 8).Char; got != ' ' && got != 0 {
		t.Errorf("ghost cell at (29,8) = %q (0x%x); clear didn't work",
			got, got)
	}
}

func TestReplayBlankDetectionIgnoresCursorOnlyBytes(t *testing.T) {
	t.Parallel()
	if !replayLooksBlank([]byte("\x1b[2J\x1b[H\x1b[10;20H   \r\n")) {
		t.Fatal("cursor-only replay should be treated as visually blank")
	}
	if replayLooksBlank([]byte("\x1b[2J\x1b[HClaude is waiting")) {
		t.Fatal("text replay should not use the grid fallback")
	}
}

func TestSerializeGridSnapshotPaintsCurrentScreen(t *testing.T) {
	t.Parallel()
	snap := grid.Snapshot{
		Cols: 4,
		Rows: 2,
		Cells: []grid.SnapshotCell{
			{Content: "C", FG: "#ff1100", Attrs: grid.AttrBold},
			{Content: "l"},
			{Content: "a"},
			{Content: "u"},
			{Content: "d"},
			{Content: "e"},
			{Content: " "},
			{Content: ">"},
		},
		CursorX: 2,
		CursorY: 1,
	}
	if !gridSnapshotHasText(snap) {
		t.Fatal("snapshot with text should be eligible for fallback paint")
	}
	paint := string(serializeGridSnapshot(snap, -1))
	for _, want := range []string{"\x1b[2J\x1b[H", "C", "lau", "de >", "\x1b[38;2;255;17;0m", "\x1b[2;3H"} {
		if !strings.Contains(paint, want) {
			t.Fatalf("paint missing %q in %q", want, paint)
		}
	}
}

func TestSerializeGridSnapshotKeepsAnsiPaletteColorsThemeable(t *testing.T) {
	t.Parallel()
	snap := grid.Snapshot{
		Cols: 2,
		Rows: 1,
		Cells: []grid.SnapshotCell{
			{Content: "~", FG: "#000080"},
			{Content: " ", BG: "#0000ff"},
		},
	}
	paint := string(serializeGridSnapshot(snap, -1))
	if !strings.Contains(paint, "\x1b[34m") {
		t.Fatalf("ANSI blue should stay palette-based/themeable; paint=%q", paint)
	}
	if !strings.Contains(paint, "\x1b[104m") {
		t.Fatalf("bright ANSI blue background should stay palette-based/themeable; paint=%q", paint)
	}
	if strings.Contains(paint, "\x1b[38;2;0;0;128m") || strings.Contains(paint, "\x1b[48;2;0;0;255m") {
		t.Fatalf("ANSI palette colors must not be serialized as hardcoded truecolor: %q", paint)
	}
}

func TestAppendGridSnapshotPaintKeepsRawReplayAndRepairsScreen(t *testing.T) {
	t.Parallel()
	raw := []byte("older scrollback\nnonblank-but-diff-only replay\n")
	snap := grid.Snapshot{
		Cols: 5,
		Rows: 1,
		Cells: []grid.SnapshotCell{
			{Content: "C"},
			{Content: "o"},
			{Content: "d"},
			{Content: "e"},
			{Content: "x"},
		},
		CursorX: 4,
		CursorY: 0,
	}
	got := appendGridSnapshotPaint(raw, snap)
	if !strings.HasPrefix(string(got), string(raw)) {
		t.Fatalf("raw replay was not preserved at the front: %q", string(got))
	}
	if !strings.Contains(string(got[len(raw):]), "Codex") {
		t.Fatalf("grid repair paint missing current screen content: %q", string(got[len(raw):]))
	}
	if !strings.Contains(string(got[len(raw):]), "\x1b[2J\x1b[H") {
		t.Fatalf("grid repair paint missing clear/home prefix: %q", string(got[len(raw):]))
	}
}

func TestAppendGridSnapshotPaintSkipsBlankSnapshot(t *testing.T) {
	t.Parallel()
	raw := []byte("meaningful replay")
	snap := grid.Snapshot{
		Cols:  2,
		Rows:  1,
		Cells: []grid.SnapshotCell{{Content: " "}, {Content: ""}},
	}
	got := appendGridSnapshotPaint(raw, snap)
	if string(got) != string(raw) {
		t.Fatalf("blank snapshot should leave replay unchanged: %q", string(got))
	}
}
