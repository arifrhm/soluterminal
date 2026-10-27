package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
)

// pane_effort.go implements POST /api/panes/{id}/effort: change a NATIVE (Focus)
// pane's reasoning-effort level (--effort) on a running conversation.
//
// Why an endpoint and not a slash command: a Focus pane runs headless
// `claude -p`, which does NOT interpret /effort (it's TUI-only), so the level
// has to be baked into the argv at spawn (nativeClaudeArgv). Changing it on a
// live pane therefore means respawning the headless agent with the new --effort,
// reusing the exact keep-conversation recipe as refresh (respawnInPlace +
// `claude --resume <session>`). Provider-agnostic: this drives effort for both
// Anthropic and GLM panes identically — only the per-pane DEFAULT differs (GLM
// defaults to "max" at creation; see createPane). Terminal panes are excluded:
// they run the interactive claude TUI where /effort already works directly.

// validEffortLevel reports whether s is an accepted --effort value. "" is valid
// and means "no flag" — claude's built-in default (also used to RESET effort).
func validEffortLevel(s string) bool {
	switch strings.TrimSpace(s) {
	case "", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

// effortChangeEnabled is the kill-switch for the effort endpoint: DEFAULT-ON,
// set SOLU_PANE_EFFORT to "0"/"false"/"off"/"no" to disable globally (mirrors
// refreshEnabled — a safety valve, since changing effort respawns a live agent).
func effortChangeEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SOLU_PANE_EFFORT"))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

type setEffortReq struct {
	Effort string `json:"effort"`
}

func (a *apiServer) handleSetEffort(w http.ResponseWriter, r *http.Request) {
	if !effortChangeEnabled() {
		http.Error(w, "effort change is disabled via SOLU_PANE_EFFORT", http.StatusBadRequest)
		return
	}
	var req setEffortReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	effort := strings.TrimSpace(req.Effort)
	if !validEffortLevel(effort) {
		http.Error(w, "unknown effort: "+effort, http.StatusBadRequest)
		return
	}

	id := r.PathValue("id")
	p, ok := a.mgr.Get(id)
	if !ok {
		http.Error(w, "pane not found", http.StatusNotFound)
		return
	}
	snap := p.Snapshot()
	if effectiveAgentKind(snap.Agent) == agentCodex {
		http.Error(w, "effort change is not yet available for Codex Focus panes", http.StatusConflict)
		return
	}
	if effectiveAgentKind(snap.Agent) == agentMuse {
		http.Error(w, "effort change is not yet available for Muse Focus panes", http.StatusConflict)
		return
	}

	// Native-only: a terminal pane runs the interactive claude TUI where the
	// /effort slash command already works, so there's nothing to respawn for.
	if snap.Kind != "native" {
		http.Error(w, "effort change is only available for native (Focus) panes", http.StatusBadRequest)
		return
	}

	// An exited-but-resumable pane is the resume path's job; don't duplicate it.
	// (Checked before the no-op so an exited pane gets the same 409 as refresh,
	// rather than a misleading 200 just because its level happens to match.)
	if snap.Status == PaneExited {
		http.Error(w, "pane has exited — use resume", http.StatusConflict)
		return
	}

	// No-op fast path: the requested level is already in effect, so skip the
	// respawn (and its brief reconnect flicker) entirely.
	if strings.TrimSpace(snap.Effort) == effort {
		writeJSON(w, http.StatusOK, makePaneDTO(p, a.aria, a.attention))
		return
	}

	// Single-flight shared with resume + refresh + switch-mode so concurrent
	// respawns can't bind two agents to one socket.
	if !p.tryAcquireRespawn() {
		http.Error(w, "respawn already in progress", http.StatusConflict)
		return
	}
	defer p.releaseRespawn()

	// Settle gate: never interrupt an in-flight turn or a parked tool approval
	// just to change effort. Refuse (don't defer) — mirrors refresh. Because we
	// refuse while working, the snapshot below is post-settle, so ClaudeSessionID
	// is the rotation-final id.
	if p.IsAgentWorking() || p.hasNativePending() {
		http.Error(w, "agent is busy — finish the current turn (or answer the pending approval) first", http.StatusConflict)
		return
	}

	// Re-snapshot AFTER the lock + settle: the session id may have rotated
	// (a /clear) since the first read.
	snap = p.Snapshot()
	sessionID := strings.TrimSpace(snap.ClaudeSessionID)
	if sessionID == "" {
		http.Error(w, "no claude session yet — start a turn before changing effort", http.StatusConflict)
		return
	}
	// The respawn resumes from the on-disk transcript; if it isn't there,
	// `--resume` would silently fall through to a fresh session (the doomed-resume
	// guard in Spawn), so refuse rather than surprise-wipe context.
	if !claudeTranscriptResumable(snap.Cwd, sessionID, strings.TrimSpace(snap.ClaudeTranscriptPath)) {
		http.Error(w, "no on-disk transcript to resume — cannot change effort yet", http.StatusConflict)
		return
	}

	// Recreate the pane under the SAME native kind + conversation, with the NEW
	// effort. Cmd stays nil so Spawn rebuilds the headless argv (nativeClaudeArgv
	// reads opts.Effort); restoreResumedState carries auto-mode/flag/prompts.
	opts := SpawnOpts{
		ID:                   snap.ID,
		Cwd:                  snap.Cwd,
		Title:                snap.Title,
		Cols:                 snap.Cols,
		Rows:                 snap.Rows,
		ClaudeSessionID:      sessionID,
		ClaudeTranscriptPath: strings.TrimSpace(snap.ClaudeTranscriptPath),
		TitleOverride:        snap.TitleOverride,
		Kind:                 "native",
		Agent:                snap.Agent,
		CodexThreadID:        snap.CodexThreadID,
		Provider:             snap.Provider,
		Effort:               effort,
		Model:                snap.Model,
		Role:                 snap.Role,
		ContextPack:          snap.ContextPack,
		NativeAllowlist:      snap.NativeAllowlist,
	}

	newPane, err := a.respawnInPlace(snap, opts)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errNativeSessionDied) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, http.StatusOK, makePaneDTO(newPane, a.aria, a.attention))
}
