package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ownershipSubDebug is one subscriber's ownership-relevant state as exposed
// by GET /api/debug/ownership.
type ownershipSubDebug struct {
	SubID       int64  `json:"subId"`
	Owner       bool   `json:"owner"`
	DeviceID    string `json:"deviceId,omitempty"`
	Mobile      bool   `json:"mobile,omitempty"`
	Automation  bool   `json:"automation,omitempty"`
	Visible     bool   `json:"visible"`
	Cols        uint16 `json:"cols"`
	Rows        uint16 `json:"rows"`
	AgeSec      int64  `json:"ageSec"`
	LastSeenSec int64  `json:"lastSeenSec"`
	// InputSec: seconds since this sub last delivered real user keystrokes;
	// -1 = never. DeviceActive: whether this sub's DEVICE typed anywhere
	// within ownerDeviceActiveWindow — the claim-arbitration signal.
	InputSec     int64 `json:"inputSec"`
	DeviceActive bool  `json:"deviceActive"`
}

type ownershipPaneDebug struct {
	PaneID     string              `json:"paneId"`
	Title      string              `json:"title,omitempty"`
	OwnerSubID int64               `json:"ownerSubId"`
	Subs       []ownershipSubDebug `json:"subs"`
}

// OwnershipDebug snapshots this pane's subscriber/ownership state for the
// debug endpoint. The take-over-lock class of bugs (ghost owners through the
// desktop's proxy chain, post-restart wipes) was effectively invisible
// before this existed — ownership lived only in memory with no way to ask
// "who does the daemon think owns this pane, and is that sub alive?".
func (p *Pane) OwnershipDebug() ownershipPaneDebug {
	p.subsMu.RLock()
	defer p.subsMu.RUnlock()
	d := ownershipPaneDebug{PaneID: p.ID, Title: p.Title, OwnerSubID: p.ownerSubID}
	now := time.Now()
	for sid, e := range p.subs {
		inputSec := int64(-1)
		if !e.lastInputAt.IsZero() {
			inputSec = int64(now.Sub(e.lastInputAt).Seconds())
		}
		d.Subs = append(d.Subs, ownershipSubDebug{
			SubID:        sid,
			Owner:        sid == p.ownerSubID,
			DeviceID:     e.deviceID,
			Mobile:       e.isMobile,
			Automation:   e.automation,
			Visible:      e.visible,
			Cols:         e.cols,
			Rows:         e.rows,
			AgeSec:       int64(now.Sub(e.createdAt).Seconds()),
			LastSeenSec:  int64(now.Sub(e.lastSeenAt).Seconds()),
			InputSec:     inputSec,
			DeviceActive: deviceInputWithin(e.deviceID, ownerDeviceActiveWindow),
		})
	}
	return d
}

// ownershipDebug lists every pane that has at least one subscriber (or a
// recorded owner), with per-sub liveness — the first stop when a pane shows
// a take-over overlay nobody asked for.
func (a *apiServer) ownershipDebug(w http.ResponseWriter, r *http.Request) {
	out := []ownershipPaneDebug{}
	for _, p := range a.mgr.List() {
		d := p.OwnershipDebug()
		if len(d.Subs) == 0 && d.OwnerSubID == 0 {
			continue
		}
		out = append(out, d)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// paneDebugTail returns the tail of the pane's ring buffer, ANSI-stripped.
// Used for diagnosing why state detection (awaiting-input / choice-prompt)
// did or didn't match a particular Claude output.
func (a *apiServer) paneDebugTail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, ok := a.mgr.Get(id)
	if !ok {
		http.Error(w, "not found", 404)
		return
	}
	nMax := 2000
	if v := r.URL.Query().Get("n"); v != "" {
		// Silently cap at buffer size.
		if parsed, err := parseIntClamped(v, 100, ringBufferBytes); err == nil {
			nMax = parsed
		}
	}
	raw := p.buffer.Snapshot()
	stripped := stripANSI(raw)
	fullStripped := stripped
	if len(stripped) > nMax {
		stripped = stripped[len(stripped)-nMax:]
	}
	state := p.computeState()

	// Probe each regex individually so the caller can see exactly what did
	// or didn't match when diagnosing a missed detection.
	scanRegion := fullStripped
	if len(scanRegion) > 1200 {
		scanRegion = scanRegion[len(scanRegion)-1200:]
	}
	detections := map[string]bool{
		"newPromptLine":                      newPromptLineRE.MatchString(scanRegion),
		"newPromptFooter":                    newPromptFooterRE.MatchString(scanRegion),
		"newPrompt (both)":                   newPromptLineRE.MatchString(scanRegion) && newPromptFooterRE.MatchString(scanRegion),
		"legacyInputBox":                     inputBoxLineRE.MatchString(scanRegion),
		"legacyBoxBottom":                    boxBottomRE.MatchString(scanRegion),
		"choicePrompt":                       choicePromptRE.MatchString(scanRegion),
		"choicePrompt (after chrome filter)": len(choicePromptMatches(scanRegion)) > 0,
	}

	// Grab the last ~5 non-empty lines of stripped text with their bytes
	// as hex. Helps diagnose when a "❯" we see visually is actually a
	// different codepoint the regex doesn't match.
	var lastLines []map[string]string
	{
		lines := strings.Split(strings.TrimRight(stripped, "\n"), "\n")
		// keep only last 8 non-empty
		nonEmpty := make([]string, 0, 8)
		for i := len(lines) - 1; i >= 0 && len(nonEmpty) < 8; i-- {
			if strings.TrimSpace(lines[i]) != "" {
				nonEmpty = append([]string{lines[i]}, nonEmpty...)
			}
		}
		for _, line := range nonEmpty {
			// Show first 12 bytes as hex for any line whose first non-space
			// char is non-ASCII — that's where glyph mismatches hide.
			trimmed := strings.TrimLeft(line, " \t")
			if len(trimmed) == 0 {
				continue
			}
			hexPrefix := ""
			if trimmed[0] > 0x7f {
				n := 12
				if len(trimmed) < n {
					n = len(trimmed)
				}
				hexPrefix = fmt.Sprintf("% x", []byte(trimmed[:n]))
			}
			lastLines = append(lastLines, map[string]string{
				"text": line,
				"hex":  hexPrefix,
			})
		}
	}

	// The assembled CURRENT screen (always-on replay grid) plus the live-turn
	// probes claudePromptVisible runs against it — the working/handed-back
	// decision for the renderer-switch paths is made on THIS text, not the raw
	// ring tail, so diagnosing a missed mid-turn detection needs both views.
	screen, screenOK := p.screenVisibleText()
	screenProbes := map[string]bool{}
	if screenOK {
		screenProbes["activeSpinner"] = claudeActiveSpinnerRE.MatchString(screen)
		screenProbes["turnDone"] = claudeTurnDoneRE.MatchString(screen)
		screenProbes["prompt"] = looksLikeClaudePrompt(screen)
	}

	writeJSON(w, 200, map[string]any{
		"paneId":         p.ID,
		"computedState":  state,
		"rawLen":         len(raw),
		"strippedLen":    len(stripped),
		"returned":       nMax,
		"detections":     detections,
		"lastLines":      lastLines,
		"tail":           stripped,
		"screenOK":       screenOK,
		"screenProbes":   screenProbes,
		"screen":         screen,
		"promptVisible":  p.claudePromptVisible(),
		"agentMidTurnUI": p.agentMidTurnForState(state),
	})
}

// paneDebugGrid returns the grid emulator's reconstructed visible text
// for a Codex (ratatui) pane, plus dimensions and cursor position.
// Counterpart to paneDebugTail for diagnosing the codex-specific
// fallback at state.go ~178: when the byte-tail regex fails because
// spinner output buried the menu, computeState falls back to the live
// cell grid. If that ALSO doesn't classify the pane as choice-prompt
// (as happened on pane_4aa85895… on 2026-05-02), this endpoint lets us
// see exactly what the grid emulator has reconstructed — was the menu
// overwritten by a spinner repaint, or is the regex just missing a
// glyph?
func (a *apiServer) paneDebugGrid(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, ok := a.mgr.Get(id)
	if !ok {
		http.Error(w, "not found", 404)
		return
	}
	// ?emu=replay dumps the REPLAY emulator instead of the grid-mode
	// one. They're separate instances with separate lifecycles, and
	// replayEmu is what reattach/repair paints serialize — when a
	// ghost row keeps reappearing after reconnects, this view shows
	// whether the phantom lives in the model (v0.0.510 diagnosis aid).
	if r.URL.Query().Get("emu") == "replay" {
		p.subsMu.RLock()
		em := p.replayEmu
		p.subsMu.RUnlock()
		if em == nil {
			writeJSON(w, 200, map[string]any{"paneId": p.ID, "replayEmu": false})
			return
		}
		snap := em.Snapshot()
		var b strings.Builder
		for y := 0; y < snap.Rows; y++ {
			start := y * snap.Cols
			if start >= len(snap.Cells) {
				break
			}
			end := start + snap.Cols
			if end > len(snap.Cells) {
				end = len(snap.Cells)
			}
			var line strings.Builder
			for _, c := range snap.Cells[start:end] {
				if c.Content == "" {
					line.WriteByte(' ')
				} else {
					line.WriteString(c.Content)
				}
			}
			b.WriteString(strings.TrimRight(line.String(), " \t"))
			b.WriteByte('\n')
		}
		writeJSON(w, 200, map[string]any{
			"paneId":    p.ID,
			"replayEmu": true,
			"cols":      snap.Cols,
			"rows":      snap.Rows,
			"cursorX":   snap.CursorX,
			"cursorY":   snap.CursorY,
			"altScreen": snap.AltScreen,
			"gridText":  b.String(),
		})
		return
	}
	// Force-init the emulator so we don't return ok=false just because
	// no other code path has touched it yet on this pane.
	p.ensureGrid()
	gridText, gridOK := p.gridVisibleText()
	snap, snapOK := p.GridSnapshot()

	resp := map[string]any{
		"paneId":           p.ID,
		"hasCodex":         p.HasCodexDescendant(),
		"hasMuse":          p.HasMuseDescendant(),
		"hasPi":            p.HasPiDescendant(),
		"hasDeepseek":      p.HasDeepseekDescendant(),
		"computedState":    p.computeState(),
		"gridTextOK":       gridOK,
		"gridSnapshotOK":   snapOK,
		"choicePromptHits": choicePromptRE.FindAllStringIndex(gridText, -1),
		// Opaque counters make an answered-menu collision diagnosable without
		// asking the operator to share terminal history or command contents.
		"choicePromptIdentity": map[string]any{
			"ackedPosition":        atomic.LoadInt64(&p.LastAckedChoicePromptPos),
			"inputPosition":        atomic.LoadInt64(&p.LastInputStrippedPos),
			"gridGeneration":       atomic.LoadUint64(&p.GridChoicePromptGeneration),
			"ackedGridGeneration":  atomic.LoadUint64(&p.LastAckedGridChoicePromptGeneration),
			"gridFingerprint":      fmt.Sprintf("%x", approvalPromptFingerprint(gridText, p.HasCodexDescendant() || p.HasMuseDescendant())),
			"ackedGridFingerprint": fmt.Sprintf("%x", atomic.LoadUint64(&p.LastGridChoicePromptHash)),
		},
	}
	if snapOK {
		resp["cols"] = snap.Cols
		resp["rows"] = snap.Rows
		resp["cursorX"] = snap.CursorX
		resp["cursorY"] = snap.CursorY
		resp["altScreen"] = snap.AltScreen
		resp["cellCount"] = len(snap.Cells)
	}
	resp["gridText"] = gridText
	writeJSON(w, 200, resp)
}

func parseIntClamped(s string, min, max int) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	if n < min {
		return min, nil
	}
	if n > max {
		return max, nil
	}
	return n, nil
}
