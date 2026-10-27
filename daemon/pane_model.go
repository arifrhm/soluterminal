package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
)

// pane_model.go implements POST /api/panes/{id}/model: change a NATIVE (Focus)
// pane's Claude model (--model) on a running conversation.
//
// Why an endpoint and not a slash command: same story as pane_effort.go — a
// Focus pane runs headless `claude -p`, which does NOT interpret /model (it's
// TUI-only), so the model has to be baked into the argv at spawn
// (nativeClaudeArgv) and changing it means respawning with `claude --resume`.
//
// Why this matters MORE than effort (verified empirically on claude 2.1.205,
// 2026-07-09): `--resume` carries the resumed session's STORED model, silently
// overriding the user's settings.json default. Every Focus respawn / mode
// switch / restart resumes, so a session is permanently trapped on whatever
// model it last ran — a settings change never takes effect. Passing --model
// once REWRITES the session's stored model (subsequent bare resumes keep it),
// so a single respawn through this endpoint heals a trapped pane for good.

// validModelID accepts "" (no flag) or a plausible claude --model value: a
// model id ("claude-fable-5-1"), an alias ("sonnet"), or the 1M-context marker
// form ("claude-fable-5-1[1m]"). Deliberately a CHARSET check, not an allowlist —
// model names churn far too often to hardcode here; the CLI itself rejects an
// unknown id at spawn and the respawn error surfaces to the caller.
var validModelRE = regexp.MustCompile(`^[A-Za-z0-9._\[\]-]{1,64}$`)

func validModelID(s string) bool {
	s = strings.TrimSpace(s)
	return s == "" || validModelRE.MatchString(s)
}

// modelChangeEnabled is the kill-switch for the model endpoint: DEFAULT-ON,
// set SOLU_PANE_MODEL to "0"/"false"/"off"/"no" to disable globally (mirrors
// effortChangeEnabled — a safety valve, since changing model respawns a live agent).
func modelChangeEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SOLU_PANE_MODEL"))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

type setModelReq struct {
	Model string `json:"model"`
}

func (a *apiServer) handleSetModel(w http.ResponseWriter, r *http.Request) {
	if !modelChangeEnabled() {
		http.Error(w, "model change is disabled via SOLU_PANE_MODEL", http.StatusBadRequest)
		return
	}
	var req setModelReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	model := strings.TrimSpace(req.Model)
	if !validModelID(model) {
		http.Error(w, "invalid model: "+model, http.StatusBadRequest)
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
		http.Error(w, "model change is not yet available for Codex Focus panes", http.StatusConflict)
		return
	}
	if effectiveAgentKind(snap.Agent) == agentMuse {
		http.Error(w, "model change is not yet available for Muse Focus panes", http.StatusConflict)
		return
	}

	// Native-only: a terminal pane runs the interactive claude TUI where the
	// /model slash command already works, so there's nothing to respawn for.
	if snap.Kind != "native" {
		http.Error(w, "model change is only available for native (Focus) panes", http.StatusBadRequest)
		return
	}

	// An exited-but-resumable pane is the resume path's job; don't duplicate it.
	if snap.Status == PaneExited {
		http.Error(w, "pane has exited — use resume", http.StatusConflict)
		return
	}

	// No-op fast path: the requested id is already the pane's explicit choice,
	// so skip the respawn. NOTE this compares against OUR flag, not the session's
	// effective model: a pane with Model=="" runs whatever the session stored
	// (the --resume stickiness), so any non-empty request respawns even though
	// the session might coincidentally already run that model — the respawn is
	// what makes the choice explicit + persistent, and it's cheap.
	if strings.TrimSpace(snap.Model) == model {
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
	// just to change model. Refuse (don't defer) — mirrors refresh/effort.
	if p.IsAgentWorking() || p.hasNativePending() {
		http.Error(w, "agent is busy — finish the current turn (or answer the pending approval) first", http.StatusConflict)
		return
	}

	// Re-snapshot AFTER the lock + settle: the session id may have rotated
	// (a /clear) since the first read.
	snap = p.Snapshot()
	sessionID := strings.TrimSpace(snap.ClaudeSessionID)
	if sessionID == "" {
		http.Error(w, "no claude session yet — start a turn before changing model", http.StatusConflict)
		return
	}
	if !claudeTranscriptResumable(snap.Cwd, sessionID, strings.TrimSpace(snap.ClaudeTranscriptPath)) {
		http.Error(w, "no on-disk transcript to resume — cannot change model yet", http.StatusConflict)
		return
	}

	// Recreate the pane under the SAME native kind + conversation, with the NEW
	// model. Cmd stays nil so Spawn rebuilds the headless argv (nativeClaudeArgv
	// reads opts.Model); restoreResumedState carries auto-mode/flag/prompts.
	opts := SpawnOpts{
		ID:                   snap.ID,
		Cwd:                  snap.Cwd,
		Title:                snap.Title,
		Cols:                 snap.Cols,
		Rows:                 snap.Rows,
		ClaudeSessionID:      sessionID,
		ClaudeTranscriptPath: strings.TrimSpace(snap.ClaudeTranscriptPath),
		TitleOverride:        snap.TitleOverride,
		Origin:               snap.Origin.clone(),
		Review:               snap.Review.clone(),
		Identity:             snap.Identity.clone(),
		Kind:                 "native",
		Agent:                snap.Agent,
		CodexThreadID:        snap.CodexThreadID,
		Provider:             snap.Provider,
		Effort:               snap.Effort,
		Model:                model,
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
