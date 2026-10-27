// Package grid hosts a server-authored VT emulator per pane.
//
// Bytes-mode panes don't touch this code — they stream raw PTY bytes through
// a ringbuf to xterm.js clients exactly as before. Grid-mode panes tee the
// same PTY bytes into an Emulator and serve grid snapshots to canvas clients.
//
// Ported from the sibling soluterminal-grid/ POC repo. See its STEP0_REPORT.md
// for the correctness gate that justified picking charmbracelet/x/vt.
package grid

import (
	"bytes"
	"errors"
	"io"
	"log"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// Emulator wraps charmbracelet/x/vt with the surface we actually need for a
// hosted pane:
//
//   - Feed() writes PTY output bytes, non-blocking
//   - DrainReplies() pulls OSC/DA/CPR response bytes that must be forwarded
//     back into the PTY (otherwise the inner program hangs on its queries)
//   - AltScreen() is lock-free (written from a vt callback that fires
//     synchronously inside vt.Write)
//   - Snapshot() returns a dense cell grid + cursor position
//
// Two mutexes on purpose (writeMu + outMu). The drainer goroutine must not
// contend with vt.Write on long query-heavy bursts from codex sync-update +
// CPR — that deadlocked the first POC implementation.
type Emulator struct {
	writeMu sync.Mutex
	outMu   sync.Mutex

	vt          *vt.Emulator
	parser      *ansi.Parser
	out         bytes.Buffer
	alt         atomic.Bool
	closed      atomic.Bool
	w           int
	h           int
	titleFilter oscTitleFilter
	textValid   bool
	visibleText string

	done chan struct{}
}

// scrollbackLines caps the underlying vt scrollback history. charmbracelet/x/vt
// defaults to 10000 lines, but every Emulator in soluterminal (replayEmu and
// gridEmu) is only ever read for its *visible* screen — Snapshot() walks rows
// 0..h and never touches scrollback, and no caller reads ScrollbackCellAt. At
// the default, 36 long-lived panes retained ~2.7 GB of fully-styled scrollback
// cells (every cell boxes Fg/Bg color interfaces). A small cap keeps the
// emulator's screen semantics intact while reclaiming that memory.
//
// This cap is also a field hardening measure. One user's daemon died at 68-111GB
// committed while allocating in vt.(*Scrollback).Push, reached through
// Screen.DeleteLine/ScrollUp and grid.(*Emulator).Feed. Upstream's
// Emulator.SetScrollbackSize caps ONLY the primary screen, so the alternate
// screen previously retained 10000 fully-styled lines in every replayEmu,
// gridEmu, and agentGrid. Both screens are therefore capped at construction.
// Screen.Reset (including RIS used by selfHealLocked) and Resize do not replace
// or resize scrollback, so this construction-time cap survives both paths.
const scrollbackLines = 64

var altScrollbackWarning sync.Once

func New(cols, rows int) *Emulator {
	e := &Emulator{
		vt:   vt.NewEmulator(cols, rows),
		w:    cols,
		h:    rows,
		done: make(chan struct{}),
	}
	e.vt.SetScrollbackSize(scrollbackLines)
	e.parser = lazyParser(e.vt)
	if alt := altScreen(e.vt); alt != nil {
		alt.SetScrollbackSize(scrollbackLines)
	} else {
		altScrollbackWarning.Do(func() {
			log.Printf("[grid] WARNING: alt-screen scrollback cap not applied (vt internals changed); alt screen retains vt default 10000 lines")
		})
	}
	e.vt.SetCallbacks(vt.Callbacks{
		AltScreen: func(on bool) {
			// Fires synchronously inside vt.Write while writeMu is held.
			// Must be lock-free.
			e.alt.Store(on)
		},
	})
	registerMarginGuards(e.vt)
	go e.drainReplies()
	return e
}

// registerMarginGuards pre-empts two unclamped margin setters in the
// underlying vt library. Its DECSTBM ('r') and DECSLRM ('s') handlers default
// an OMITTED bottom/right margin to the screen size but never clamp an
// EXPLICIT value that exceeds it; the raw value lands in Screen.scroll, and a
// later scroll op (e.g. ESC M reverse-index → ScrollDown → InsertLineArea
// walking Lines[scroll.Max.Y-1]) indexes past the buffer and panics — the
// production "index out of range [38] with length 30" (margins for a 39-row
// screen on a 30-row emulator). This is GUARANTEED to happen on reattach
// after a daemon update: AttachExisting creates the replay emulator at the
// persisted panes.json dims, then feeds the agent's ring-buffer history,
// which spans epochs when the PTY was a different size — DECSTBM from a
// taller epoch is simply part of that stream. (vt.Resize is NOT the trigger:
// it resets scroll to the new bounds; only these set-time paths go stale.)
//
// CSI dispatch is LIFO with short-circuit — the LAST registered handler runs
// FIRST and returning true stops the chain — so these guards swallow ONLY the
// out-of-range forms (DEC STD-070: margin sequences with invalid params are
// ignored, so dropping them matches real-terminal behavior) and return false
// otherwise, falling through to the built-in handlers untouched.
//
// If TestUpstreamDECSTBMUnclampedStillPanics ever fails after a dependency
// bump, upstream fixed the clamp and these guards can be retired.
func registerMarginGuards(vte *vt.Emulator) {
	vte.RegisterCsiHandler('r', func(params ansi.Params) bool {
		// Mirror the built-in's defaulting (missing/0 → full height), then
		// swallow exactly the case it mishandles.
		h := vte.Height()
		bottom, _, _ := params.Param(1, h)
		if bottom < 1 {
			bottom = h
		}
		return bottom > h
	})
	vte.RegisterCsiHandler('s', func(params ansi.Params) bool {
		// 's' is DECSLRM only while ModeLeftRightMargin is set; otherwise it
		// is SCOSC (save cursor). The mode isn't readable from out here, so
		// key off the unambiguous shape instead: a real SCOSC never carries
		// parameters, and a valid DECSLRM fits the screen — an EXPLICIT
		// second parameter beyond the width can only be a margin set that
		// the built-in would store out-of-bounds.
		w := vte.Width()
		right, _, hasRight := params.Param(1, w)
		return hasRight && right > w
	})
}

func (e *Emulator) drainReplies() {
	buf := make([]byte, 4096)
	for {
		n, err := e.vt.Read(buf)
		if n > 0 {
			e.outMu.Lock()
			e.out.Write(buf[:n])
			e.outMu.Unlock()
		}
		if err != nil {
			close(e.done)
			return
		}
	}
}

// Feed writes PTY output bytes into the emulator.
//
// Wrapped in a recover so a panic inside the underlying vt library (unclamped
// DECSTBM/DECSLRM margins — see registerMarginGuards — or any future sharp
// edge) can't kill the daemon.
func (e *Emulator) Feed(p []byte) {
	if e.closed.Load() {
		return
	}
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	if e.closed.Load() {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			// Keep the daemon alive but surface the panic so we can
			// debug it. Until 2026-05-02 this swallowed `r` silently
			// — meaning any state-corrupting upstream library bug was
			// invisible. The log line + stack lets us correlate
			// rendering anomalies (e.g. literal escape text appearing
			// in cells) with the panic that caused them, instead of
			// guessing.
			log.Printf("grid: vt.Write panic recovered (payload=%dB): %v\n%s",
				len(p), r, debug.Stack())
			if OnPanic != nil {
				OnPanic("grid-emulator-write", r)
			}
			e.selfHealLocked()
		}
	}()
	e.textValid = false
	p = e.titleFilter.filter(p)
	if e.parser == nil {
		_, _ = e.vt.Write(p)
		return
	}
	for len(p) > 0 {
		n := min(len(p), parserFeedBytes)
		// vt.Write flushes its pending grapheme at the end of each call.
		// Split only after ASCII, which already ends that pending group;
		// a Unicode-only run stays together through its original Feed boundary.
		for n < len(p) && p[n-1] >= utf8.RuneSelf {
			n++
		}
		growParser(e.parser, n)
		_, _ = e.vt.Write(p[:n])
		p = p[n:]
	}
}

// selfHealLocked resets the vt after a recovered Write panic. Without this,
// the state that CAUSED the panic (e.g. an out-of-bounds scroll region) stays
// in place — every subsequent Feed re-panics on the next scroll op and every
// Snapshot serves the garbled half-fed screen to reattaching clients, which is
// exactly the post-update "terminal rendering glitch" (gridPaintForSub builds
// the reconnect repaint from this emulator). RIS rebuilds both screens,
// margins, modes and tab stops from scratch through the same public Write
// path, giving a blank-but-COHERENT grid that the next app output repaints.
// Best-effort: if the reset itself panics the emulator is beyond in-band
// repair, and the next client-driven Resize rebuilds it. Caller holds writeMu.
func (e *Emulator) selfHealLocked() {
	defer func() { _ = recover() }()
	e.textValid = false
	e.titleFilter = oscTitleFilter{}
	_, _ = e.vt.Write([]byte("\x1bc")) // RIS — full reset
	// RIS lands the terminal on the primary screen, but the reset path does
	// not fire the AltScreen mode callback — realign our cached flag so
	// Snapshot/subscribe logic doesn't keep reporting a stale alt=true.
	e.alt.Store(false)
}

// DrainReplies returns any pending reply bytes and clears the internal buffer.
// These MUST be written back to the PTY or query-driven TUIs (ratatui / codex
// under sync-update) will block on their handshake.
func (e *Emulator) DrainReplies() []byte {
	return e.drain(0)
}

// DrainRepliesAfter waits up to d for pending reply bytes, then returns them.
func (e *Emulator) DrainRepliesAfter(d time.Duration) []byte {
	return e.drain(d)
}

func (e *Emulator) drain(d time.Duration) []byte {
	if d > 0 {
		deadline := time.Now().Add(d)
		for {
			e.outMu.Lock()
			has := e.out.Len() > 0
			e.outMu.Unlock()
			if has || time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	e.outMu.Lock()
	defer e.outMu.Unlock()
	b := e.out.Bytes()
	out := make([]byte, len(b))
	copy(out, b)
	e.out.Reset()
	return out
}

// Resize updates the grid dimensions.
func (e *Emulator) Resize(cols, rows int) {
	if e.closed.Load() {
		return
	}
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	if e.closed.Load() {
		return
	}
	e.textValid = false
	e.vt.Resize(cols, rows)
	e.w, e.h = cols, rows
}

// AltScreen reports whether the emulator is currently on the alternate screen.
func (e *Emulator) AltScreen() bool { return e.alt.Load() }

// Size returns current (cols, rows).
func (e *Emulator) Size() (int, int) {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	return e.w, e.h
}

// Snapshot returns a dense grid snapshot. Takes writeMu because CellAt /
// CursorPosition read emulator internals that can mutate during vt.Write.
func (e *Emulator) Snapshot() Snapshot {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	return e.snapshotLocked()
}

// TrySnapshot lets fail-closed observers decline a busy screen without waiting
// for a concurrent terminal update while holding an input ownership lease.
func (e *Emulator) TrySnapshot() (Snapshot, bool) {
	if !e.writeMu.TryLock() {
		return Snapshot{}, false
	}
	defer e.writeMu.Unlock()
	return e.snapshotLocked(), true
}

func (e *Emulator) snapshotLocked() Snapshot {
	cols, rows := e.w, e.h
	cells := make([]SnapshotCell, 0, cols*rows)
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			cells = append(cells, cellFrom(e.vt.CellAt(x, y)))
		}
	}
	pos := e.vt.CursorPosition()
	return Snapshot{
		Cols:      cols,
		Rows:      rows,
		Cells:     cells,
		CursorX:   pos.X,
		CursorY:   pos.Y,
		AltScreen: e.alt.Load(),
	}
}

// Close shuts down the drainer goroutine.
func (e *Emulator) Close() error {
	if e.closed.Swap(true) {
		return nil
	}
	e.writeMu.Lock()
	closer, ok := e.vt.InputPipe().(interface {
		CloseWithError(error) error
	})
	if !ok {
		e.writeMu.Unlock()
		return errors.New("vt input pipe cannot be closed")
	}
	_ = closer.CloseWithError(io.EOF)
	e.writeMu.Unlock()
	<-e.done
	return nil
}

// SnapshotCell is a serialised cell used in wire frames.
type SnapshotCell struct {
	Content string `json:"s,omitempty"`
	Width   int    `json:"w,omitempty"`
	FG      string `json:"fg,omitempty"`
	BG      string `json:"bg,omitempty"`
	Attrs   uint16 `json:"a,omitempty"`
}

// Snapshot is a grid + cursor state snapshot at a point in time.
type Snapshot struct {
	Cols      int            `json:"cols"`
	Rows      int            `json:"rows"`
	Cells     []SnapshotCell `json:"cells"`
	CursorX   int            `json:"cx"`
	CursorY   int            `json:"cy"`
	AltScreen bool           `json:"alt"`
}

func cellFrom(c *uv.Cell) SnapshotCell {
	if c == nil {
		return SnapshotCell{Content: " ", Width: 1}
	}
	s := c.Content
	if s == "" {
		s = " "
	}
	w := c.Width
	if w == 0 {
		w = 1
	}
	return SnapshotCell{
		Content: s,
		Width:   w,
		FG:      styleColor(c.Style.Fg),
		BG:      styleColor(c.Style.Bg),
		Attrs:   styleAttrs(c.Style),
	}
}
