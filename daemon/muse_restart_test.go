package main

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const museFinishedScreen = "› go ahead and do all that u think is best for us\n" +
	"no data loss.\n" +
	"Verified: 7/7 new tests pass\n" +
	"◆ Worked for 5m 05s · 10:24 AM\n" +
	"› \n" +
	"  muse-spark-1.3-contributor · xhigh · ../aria · Auto-review\n"

func museTestPane(id, screen string, watermark int64, lastActivity time.Time) *Pane {
	p := deferredTestPane(id, "")
	p.Status = PaneRunning
	p.Agent = agentMuse
	writeBuf(p, screen)
	// writeBuf bypasses the read loop, so keep the stripped-char counter
	// consistent with the buffer by hand (ASCII + multibyte, no ANSI/R).
	atomic.StoreInt64(&p.TotalStrippedChars, int64(len(screen)))
	p.fleetTurnOutputWatermark.Store(watermark)
	p.fleetTurnAdmitted.Store(true)
	atomic.StoreInt64(&p.LastActivityMs, lastActivity.UnixMilli())
	return p
}

// A Muse Code TUI pane whose turn finished ("◆ Worked for …") must clear the
// restart latch: no Claude/Codex prompt exists on its screen and the quiet
// hand-back deliberately excludes muse panes, so without the done-marker
// settle the pane read "busy" forever (the stuck "old build · busy" badge).
func TestFleetPaneBusyClearsOnMuseWorkedForMarker(t *testing.T) {
	t.Parallel()
	p := museTestPane("pane-muse-done", museFinishedScreen, 10, time.Now().Add(-time.Minute))
	if fleetPaneBusyForRestart(p) {
		t.Fatal("finished Muse pane stayed busy")
	}
	if p.fleetTurnAdmitted.Load() {
		t.Fatal("admission latch was not cleared")
	}
}

// A Muse turn that is still streaming (output, no completion marker) must
// stay busy — the marker settle must not fire early.
func TestFleetPaneBusyStaysTrueForMuseMidTurn(t *testing.T) {
	t.Parallel()
	screen := "› go ahead and do all that u think is best for us\n" +
		"Reading desktop/src/lib/email.ts…\n"
	p := museTestPane("pane-muse-working", screen, 10, time.Now())
	if !fleetPaneBusyForRestart(p) {
		t.Fatal("streaming Muse pane was not busy")
	}
	if !p.fleetTurnAdmitted.Load() {
		t.Fatal("admission latch was cleared mid-turn")
	}
}

// A completion marker from a PREVIOUS turn must not settle a NEW admission:
// the user submitted again after the marker, so the marker predates the
// watermark and the pane is busy until the new turn hands back.
func TestFleetPaneBusyIgnoresStaleMuseMarker(t *testing.T) {
	t.Parallel()
	markerAt := int64(strings.Index(museFinishedScreen, "◆"))
	if markerAt < 0 {
		t.Fatal("test screen lost its completion marker")
	}
	// New turn admitted after the marker, plus a few output bytes since.
	watermark := markerAt + 1
	p := museTestPane("pane-muse-stale", museFinishedScreen, watermark, time.Now().Add(-time.Minute))
	if !fleetPaneBusyForRestart(p) {
		t.Fatal("stale Muse marker settled a new admission")
	}
	if !p.fleetTurnAdmitted.Load() {
		t.Fatal("admission latch was cleared by a stale marker")
	}
}
