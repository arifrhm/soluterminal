package main

import (
	"context"
	"log"
	"time"
)

// Focus (native renderer) retirement, stage 1+2.
//
// Focus mode is off by default (nativeRendererEnabled). Panes that are already
// native — restored from panes.json, or resumed later by the boot respawn
// path — are moved back to the terminal renderer through the existing
// DEFERRED switch: it fires only once the pane's turn has settled, resumes the
// same Claude/Codex/Muse conversation, never interrupts work, and survives a
// restart (pendingSwitch is persisted). The sweep below just keeps arming that
// switch for any native pane that doesn't already have one, so panes that come
// back later, or whose deferred switch gave up after deferredSwitchMaxWait,
// are retried.

const focusRetireSweepInterval = time.Minute

func (a *apiServer) runFocusRetirement(ctx context.Context) {
	a.sweepFocusPanes()
	t := time.NewTicker(focusRetireSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.sweepFocusPanes()
		}
	}
}

// sweepFocusPanes arms a deferred switch to the terminal renderer for every
// running native pane that has none pending. Returns how many it armed.
func (a *apiServer) sweepFocusPanes() int {
	armed := 0
	for _, p := range a.mgr.List() {
		if !focusPaneNeedsRetiring(p) {
			continue
		}
		log.Printf("focus-retire: switching native pane %s back to terminal once its turn settles", p.ID)
		a.armDeferredSwitch(p, "")
		armed++
	}
	return armed
}

func focusPaneNeedsRetiring(p *Pane) bool {
	snap := p.Snapshot()
	if snap.Kind != "native" || snap.Status != PaneRunning {
		return false
	}
	if pend := p.pendingSwitchSnapshot(); pend != nil && pend.Target == "" {
		return false // already heading back to the terminal
	}
	return true
}
