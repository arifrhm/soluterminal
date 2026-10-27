package main

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testPaneForGrid() *Pane {
	return &Pane{
		ID:     "pane-grid-test",
		Cols:   80,
		Rows:   24,
		buffer: NewRingBuffer(1024),
		subs:   map[int64]*subEntry{},
		done:   make(chan struct{}),
	}
}

// TestEnsureGridSnapshot covers the surviving emulator path: ensureGrid
// lazily builds the per-pane VT emulator, GridSnapshot reads it, and Feed'd
// output is reflected in the snapshot — the exact pipeline ARIA auto-mode
// detection and the debug-grid endpoint depend on.
func TestEnsureGridSnapshot(t *testing.T) {
	t.Parallel()
	p := testPaneForGrid()
	defer close(p.done)

	if _, ok := p.GridSnapshot(); ok {
		t.Fatal("GridSnapshot should not be live before ensureGrid")
	}

	p.ensureGrid()
	defer p.shutdownGrid()

	p.subsMu.Lock()
	p.gridEmu.Feed([]byte("hello"))
	p.subsMu.Unlock()

	snap, ok := p.GridSnapshot()
	if !ok {
		t.Fatal("GridSnapshot not live after ensureGrid")
	}
	if snap.Cols != 80 || snap.Rows != 24 {
		t.Fatalf("grid size = %dx%d, want 80x24", snap.Cols, snap.Rows)
	}
	if !gridSnapshotHasText(snap) {
		t.Fatal("snapshot should contain fed text")
	}
}

// TestEnsureGridReplayDoesNotTurnOverPrompt pins that historical synchronized
// frames replayed while the ARIA grid is created establish a baseline only.
// They must not manufacture the live absent→present edge used to distinguish
// a new identical MCP modal from a stale answered one.
func TestEnsureGridReplayDoesNotTurnOverPrompt(t *testing.T) {
	t.Parallel()
	p := testPaneForGrid()
	p.Status = PaneRunning
	p.forceCodexForTest = true
	defer close(p.done)
	defer p.shutdownGrid()

	historical := "\x1b[?2026h\x1b[2J\x1b[H" +
		strings.ReplaceAll(codexMcpPermissionObservedPrompt, "\n", "\r\n") +
		"\x1b[?2026l"
	if _, err := p.buffer.Write([]byte(historical)); err != nil {
		t.Fatalf("buffer.Write: %v", err)
	}
	p.ensureGrid()
	if got := atomic.LoadUint64(&p.GridChoicePromptGeneration); got != 0 {
		t.Fatalf("historical ensureGrid replay advanced generation: %d", got)
	}

	gridText, _, ok := p.gridVisibleTextWithGeneration()
	if !ok {
		t.Fatal("grid not visible after ensureGrid")
	}
	fp := approvalPromptFingerprint(gridText, true)
	if fp == 0 {
		t.Fatal("replayed MCP modal has no fingerprint")
	}
	atomic.StoreUint64(&p.LastGridChoicePromptHash, fp)
	atomic.StoreInt64(&p.LastAckedChoicePromptPos, 100)
	atomic.StoreInt64(&p.LastInputStrippedPos, 101)
	atomic.StoreInt64(&p.LastActivityMs, time.Now().UnixMilli())
	if got := p.computeState(); got == StateChoicePrompt {
		t.Fatal("replayed answered modal was mistaken for a live turnover")
	}
}

// TestResizeGridEmuCapsDimensions verifies the emulator-lockstep resize used
// by recomputeSize caps to the daemon's terminal-size limits.
func TestResizeGridEmuCapsDimensions(t *testing.T) {
	t.Parallel()
	p := testPaneForGrid()
	defer close(p.done)
	p.ensureGrid()
	defer p.shutdownGrid()

	p.resizeGridEmu(maxTerminalCols+100, maxTerminalRows+100)

	snap, ok := p.GridSnapshot()
	if !ok {
		t.Fatal("GridSnapshot not live")
	}
	if snap.Cols != maxTerminalCols || snap.Rows != maxTerminalRows {
		t.Fatalf("grid size = %dx%d, want capped %dx%d",
			snap.Cols, snap.Rows, maxTerminalCols, maxTerminalRows)
	}
	if len(snap.Cells) != maxTerminalCols*maxTerminalRows {
		t.Fatalf("cell count = %d, want %d", len(snap.Cells), maxTerminalCols*maxTerminalRows)
	}
}
