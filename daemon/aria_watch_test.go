package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTrimTrailingStatusJunk_RecoversMenuBehindSpinnerLines is the
// regression test for the 2026-04-27 bug where pane_7603fcb...'s
// auto-approve never fired: the choice-prompt menu was buried under
// hundreds of trailing spinner-redraw lines (Claude Code redraws its
// "✶ thinking" status using cursor-position escapes that stripANSI
// loses), so the 30-line eval tail saw nothing actionable and the
// model declined twice with "no actionable prompt".
func TestTrimTrailingStatusJunk_RecoversMenuBehindSpinnerLines(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("Do you want to proceed?\n")
	b.WriteString("  ❯  1.  Yes \n")
	b.WriteString("  2.  Yes, and always allow access to trading-stack / from this project\n")
	b.WriteString("  3.  No\n")
	b.WriteString("\n  Esc to cancel · Tab to amend · ctrl+e to explain \n")
	// Now simulate the spinner-redraw artifact: ~200 lines of empty +
	// short single-glyph status.
	for i := 0; i < 100; i++ {
		b.WriteString("\n\n  ●  \n\n  ✶ k  t \n\n   p     4 \n")
	}

	end := trimTrailingStatusJunk(b.String())
	trimmed := b.String()[:end]
	if !strings.Contains(trimmed, "Do you want to proceed?") {
		t.Fatalf("expected menu to survive trim, got tail:\n%s", trimmed)
	}
	if !strings.Contains(trimmed, "1.  Yes") {
		t.Fatalf("expected option 1 to survive trim, got tail:\n%s", trimmed)
	}
	// The trimmed end should be the "Esc to cancel..." line — that's
	// the deepest actionable content above the spinner zone.
	last := strings.TrimSpace(trimmed[strings.LastIndex(trimmed[:len(trimmed)-1], "\n")+1:])
	if !strings.HasPrefix(last, "Esc to cancel") {
		t.Errorf("expected last surviving line to be the Esc-to-cancel hint, got %q", last)
	}
}

// TestTailOfPane_WindowedSnapshotStillSurfacesMenu guards the v0.0.484-class
// fix: tailOfPane copies a bounded recent-tail window (not the full 8 MB
// ring), and that window must still be large enough to surface a menu
// buried under heavy trailing spinner-redraw junk.
func TestTailOfPane_WindowedSnapshotStillSurfacesMenu(t *testing.T) {
	t.Parallel()
	p := &Pane{
		ID:     "pane_windowed",
		Status: PaneRunning,
		Cols:   80,
		Rows:   24,
		buffer: NewRingBuffer(4 << 20), // 4 MB ring, bigger than the 2 MB window
		subs:   map[int64]*subEntry{},
	}
	// Old scrollback that should fall outside the window (and doesn't matter).
	if _, err := p.buffer.Write([]byte(strings.Repeat("ancient log line that is irrelevant\n", 20000))); err != nil {
		t.Fatalf("buffer.Write: %v", err)
	}
	// The menu, then heavy trailing junk that exceeds the min window but
	// fits within the max window.
	if _, err := p.buffer.Write([]byte("Do you want to proceed?\n  ❯  1.  Yes \n  2.  No\n")); err != nil {
		t.Fatalf("buffer.Write: %v", err)
	}
	for i := 0; i < 500; i++ {
		if _, err := p.buffer.Write([]byte("\n\n  ●  \n\n  ✶ k  t \n")); err != nil {
			t.Fatalf("buffer.Write: %v", err)
		}
	}
	out := tailOfPane(p, 30)
	if !strings.Contains(out, "Do you want to proceed?") {
		t.Fatalf("windowed tail lost the menu under trailing junk:\n%s", out)
	}
}

func TestIsStatusJunkLine(t *testing.T) {
	t.Parallel()
	junk := []string{
		"",
		"●",
		"✶ k  t",
		"p     4",
		"  ",
		"   12k", // short, no menu marker
	}
	for _, j := range junk {
		if !isStatusJunkLine(strings.TrimSpace(j)) {
			t.Errorf("expected %q to be junk", j)
		}
	}

	keep := []string{
		"Do you want to proceed?",
		"❯  1.  Yes",
		"1.  Yes",
		"2. Cautious",
		"y/n",
		"Y/N",
		"Append WATCHLIST flag to .env",
	}
	for _, k := range keep {
		if isStatusJunkLine(strings.TrimSpace(k)) {
			t.Errorf("expected %q to be kept", k)
		}
	}
}

func TestCapPaneTailTruncatesLongLine(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", ariaTailMaxLineRunes+500)
	got := capPaneTail("before\n" + long)
	if !strings.Contains(got, "[line truncated]") {
		t.Fatalf("capPaneTail did not mark long line truncation")
	}
	if !strings.HasSuffix(got, strings.Repeat("a", ariaTailMaxLineRunes)) {
		t.Fatalf("capPaneTail did not preserve the tail of the long line")
	}
}

func TestCapPaneTailTruncatesTotalBytesOnUTF8Boundary(t *testing.T) {
	t.Parallel()
	suffix := "recent prompt suffix"
	got := capPaneTail(strings.Repeat("界\n", ariaTailMaxBytes) + suffix)
	if !strings.HasPrefix(got, "[tail truncated]\n") {
		t.Fatalf("capPaneTail missing total truncation marker")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("capPaneTail returned invalid UTF-8")
	}
	if !strings.HasSuffix(got, suffix) {
		t.Fatalf("capPaneTail did not preserve recent suffix")
	}
}
