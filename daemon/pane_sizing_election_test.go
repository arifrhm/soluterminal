package main

import (
	"testing"
	"time"

	"soluterminal/grid"
)

func mkSizingSub(cols, rows uint16, visible, mobile bool, seq int64) *subEntry {
	return &subEntry{
		cols:          cols,
		rows:          rows,
		visible:       visible,
		isMobile:      mobile,
		lastActiveSeq: seq,
		lastSeenAt:    time.Now(),
	}
}

func paneWithSizingSubs(subs ...*subEntry) *Pane {
	p := &Pane{subs: map[int64]*subEntry{}}
	for i, s := range subs {
		p.subs[int64(i+1)] = s
	}
	return p
}

// A background grid tile (hidden, narrow, even with a NEWER active stamp) must
// not pin the shared PTY narrow while a visible wide desktop is the on-screen
// viewer. This is the core "full-screen pane stuck at 65" defect.
func TestDesiredSizeHiddenNarrowDoesNotBeatVisibleWide(t *testing.T) {
	t.Parallel()
	p := paneWithSizingSubs(
		mkSizingSub(190, 50, true, false, 1), // visible wide desktop, older stamp
		mkSizingSub(65, 29, false, false, 9), // hidden narrow tile, newer stamp
	)
	if c, r := p.desiredSizeLocked(); c != 190 || r != 50 {
		t.Fatalf("got %dx%d, want 190x50 (visible wide must beat hidden narrow)", c, r)
	}
}

// When the only visible viewer is mid-layout (cols==0) and a hidden narrow sub
// has dims, HOLD (0,0 = leave PTY unchanged) rather than adopting the hidden
// narrow width — that adoption is how a parked tile shrank an active pane.
func TestDesiredSizeHoldsWhenVisibleHasNoDims(t *testing.T) {
	t.Parallel()
	p := paneWithSizingSubs(
		mkSizingSub(0, 0, true, false, 1),    // visible, dims pending
		mkSizingSub(65, 29, false, false, 9), // hidden narrow with dims
	)
	if c, r := p.desiredSizeLocked(); c != 0 || r != 0 {
		t.Fatalf("got %dx%d, want 0x0 (hold, not adopt hidden narrow)", c, r)
	}
}

// A stale sub (no inbound frame/pong past staleSubWindow) stops voting even with
// a higher active stamp, so a half-open narrow connection can't hold the width.
func TestDesiredSizeSkipsStaleSub(t *testing.T) {
	t.Parallel()
	visWide := mkSizingSub(190, 50, true, false, 5)
	staleNarrow := mkSizingSub(65, 29, true, false, 9) // higher seq but stale
	staleNarrow.lastSeenAt = time.Now().Add(-2 * staleSubWindow)
	p := paneWithSizingSubs(visWide, staleNarrow)
	if c, r := p.desiredSizeLocked(); c != 190 || r != 50 {
		t.Fatalf("got %dx%d, want 190x50 (stale narrow must be skipped)", c, r)
	}
}

// With NO visible sub at all, fall back to the most-recently-active hidden sub
// so a brief all-hidden blip doesn't spuriously resize.
func TestDesiredSizeFallsBackToHiddenWhenNoVisible(t *testing.T) {
	t.Parallel()
	p := paneWithSizingSubs(
		mkSizingSub(120, 40, false, false, 3),
		mkSizingSub(190, 50, false, false, 7), // newer hidden → wins fallback
	)
	if c, r := p.desiredSizeLocked(); c != 190 || r != 50 {
		t.Fatalf("got %dx%d, want 190x50 (most-active hidden fallback)", c, r)
	}
}

// Regression guard: a phone actively viewing still wins its narrow width
// (phone-first precedence must survive the hardening).
func TestDesiredSizeVisibleMobileStillWins(t *testing.T) {
	t.Parallel()
	p := paneWithSizingSubs(
		mkSizingSub(190, 50, true, false, 1), // visible desktop
		mkSizingSub(40, 90, true, true, 9),   // visible phone, newer → wins
	)
	if c, r := p.desiredSizeLocked(); c != 40 || r != 90 {
		t.Fatalf("got %dx%d, want 40x90 (visible mobile wins)", c, r)
	}
}

// updateSize's active-widen reclaim: a visible desktop that grows materially
// wider (a maximize) re-stamps itself active; sub-threshold jitter does not.
func TestUpdateSizeWidenReclaimStampsActive(t *testing.T) {
	t.Parallel()
	p := &Pane{
		ID:        "pane-test",
		Cols:      80,
		Rows:      24,
		buffer:    newTestRing(),
		subs:      map[int64]*subEntry{},
		replayEmu: grid.New(80, 24),
	}
	subID, _, _, _, updateSize, _, _, cancel := p.Subscribe(80, 24)
	defer cancel()

	seqOf := func() int64 {
		p.subsMu.Lock()
		defer p.subsMu.Unlock()
		return p.subs[subID].lastActiveSeq
	}

	before := seqOf()
	updateSize(190, 50) // 80 -> 190: a real maximize (>= widenReclaimCols)
	if after := seqOf(); after <= before {
		t.Fatalf("widen-reclaim did not stamp active: before=%d after=%d", before, after)
	}
	mid := seqOf()
	updateSize(191, 50) // +1 col: below threshold, must not restamp
	if after := seqOf(); after != mid {
		t.Fatalf("sub-threshold jitter wrongly restamped: %d -> %d", mid, after)
	}
}
