package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func postRefresh(api *apiServer, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/panes/"+id+"/refresh", nil)
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	api.handleRefresh(rec, req)
	return rec
}

// writeTempTranscript drops an on-disk transcript file so claudeTranscriptOnDisk
// passes for a keep-conversation refresh, and returns its absolute path.
func writeTempTranscript(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sess.jsonl")
	if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The core happy path: a refresh of a running native pane KILLS the old agent and
// respawns under the SAME id resuming the SAME session (nil cmd → Spawn rewrites
// the headless --resume argv). This is the regression guard against the
// performSwitchMode no-op bug (snap.Kind==target returns without killing/spawning
// → zero RAM reclaimed). It also asserts the non-spawn state (auto-mode, flag,
// createdAt) survives — the bug where a respawn silently wipes it.
func TestRefreshNativeRespawnsKeepingSession(t *testing.T) {
	running := switchTestPane("pane_ref", "native", nil, "sess-xyz")
	running.ClaudeTranscriptPath = writeTempTranscript(t)
	running.Flagged = true
	running.FlagMemo = "keep me"
	running.AutoMode = "always"
	created := running.CreatedAt
	api, spawnOpts, killed := resumeTestServer(t, running)

	rec := postRefresh(api, "pane_ref")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if len(*killed) != 1 || (*killed)[0] != "pane_ref" {
		t.Fatalf("killed = %#v, want [pane_ref] (NOT a silent no-op)", *killed)
	}
	if len(*spawnOpts) != 1 {
		t.Fatalf("spawnOpts len = %d, want 1 (a real respawn happened)", len(*spawnOpts))
	}
	o := (*spawnOpts)[0]
	if o.ID != "pane_ref" || o.Kind != "native" || o.ClaudeSessionID != "sess-xyz" || o.Cmd != nil {
		t.Fatalf("opts = %+v, want id=pane_ref kind=native session=sess-xyz cmd=nil", o)
	}
	if o.ClaudeTranscriptPath != running.ClaudeTranscriptPath {
		t.Fatalf("transcript path not carried: %q", o.ClaudeTranscriptPath)
	}
	np, ok := api.mgr.Get("pane_ref")
	if !ok {
		t.Fatal("refreshed pane missing from manager")
	}
	snap := np.Snapshot()
	if snap.AutoMode != "always" || !snap.Flagged || snap.FlagMemo != "keep me" {
		t.Fatalf("state not restored: auto=%q flagged=%v memo=%q", snap.AutoMode, snap.Flagged, snap.FlagMemo)
	}
	if !snap.CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt reset by refresh: got %v want %v", snap.CreatedAt, created)
	}
}

// Codex Focus uses the same reclaim contract, but continuity comes from the
// durable app-server thread id instead of a Claude transcript. The respawn must
// remain native+Codex with a nil Cmd so Spawn performs thread/resume.
func TestRefreshCodexNativeRespawnsKeepingThread(t *testing.T) {
	running := switchTestPane("pane_codex_ref", "native", nil, "")
	running.Agent = agentCodex
	running.CodexThreadID = "019f543c-3f11-7701-bc5e-6a73a50a9083"
	running.Flagged = true
	running.AutoMode = "always"
	created := running.CreatedAt
	api, spawnOpts, killed := resumeTestServer(t, running)

	rec := postRefresh(api, "pane_codex_ref")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if len(*killed) != 1 || (*killed)[0] != "pane_codex_ref" {
		t.Fatalf("killed = %#v, want [pane_codex_ref]", *killed)
	}
	if len(*spawnOpts) != 1 {
		t.Fatalf("spawnOpts len = %d, want 1", len(*spawnOpts))
	}
	o := (*spawnOpts)[0]
	if o.ID != "pane_codex_ref" || o.Kind != "native" || o.Agent != agentCodex ||
		o.CodexThreadID != running.CodexThreadID || o.ClaudeSessionID != "" || o.Cmd != nil {
		t.Fatalf("opts = %+v, want native Codex thread/resume continuity", o)
	}
	np, ok := api.mgr.Get("pane_codex_ref")
	if !ok {
		t.Fatal("refreshed Codex pane missing from manager")
	}
	snap := np.Snapshot()
	if snap.Agent != agentCodex || snap.CodexThreadID != running.CodexThreadID {
		t.Fatalf("Codex identity not restored: agent=%q thread=%q", snap.Agent, snap.CodexThreadID)
	}
	if snap.AutoMode != "always" || !snap.Flagged || !snap.CreatedAt.Equal(created) {
		t.Fatalf("state not restored: auto=%q flagged=%v created=%v", snap.AutoMode, snap.Flagged, snap.CreatedAt)
	}
}

// Refresh is native-only: a terminal pane (which already has Ctrl+C + exec bash)
// is rejected with 400 and never respawned.
func TestRefreshTerminalRejected(t *testing.T) {
	running := switchTestPane("pane_t", "", []string{"claude"}, "sess-1")
	api, spawnOpts, _ := resumeTestServer(t, running)
	rec := postRefresh(api, "pane_t")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%q", rec.Code, rec.Body.String())
	}
	if len(*spawnOpts) != 0 {
		t.Fatalf("terminal refresh must not respawn, got %d", len(*spawnOpts))
	}
}

// An exited pane is the resume path's job; refresh declines it with 409.
func TestRefreshExitedUsesResume(t *testing.T) {
	p := switchTestPane("pane_e", "native", nil, "s")
	p.Status = PaneExited
	api, spawnOpts, _ := resumeTestServer(t, p)
	rec := postRefresh(api, "pane_e")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%q", rec.Code, rec.Body.String())
	}
	if len(*spawnOpts) != 0 {
		t.Fatalf("exited refresh must not respawn")
	}
}

// A brand-new native pane with no captured session id can't be resumed (a fresh
// respawn would silently start a DIFFERENT session and lose the in-flight turn):
// 409, no respawn.
func TestRefreshNoSessionRejected(t *testing.T) {
	p := switchTestPane("pane_ns", "native", nil, "")
	api, spawnOpts, _ := resumeTestServer(t, p)
	rec := postRefresh(api, "pane_ns")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%q", rec.Code, rec.Body.String())
	}
	if len(*spawnOpts) != 0 {
		t.Fatalf("session-less refresh must not respawn")
	}
}

func TestRefreshCodexNoThreadRejected(t *testing.T) {
	p := switchTestPane("pane_codex_no_thread", "native", nil, "")
	p.Agent = agentCodex
	api, spawnOpts, _ := resumeTestServer(t, p)
	rec := postRefresh(api, "pane_codex_no_thread")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no codex conversation") {
		t.Fatalf("status = %d body=%q, want Codex conversation conflict", rec.Code, rec.Body.String())
	}
	if len(*spawnOpts) != 0 {
		t.Fatalf("thread-less Codex refresh must not respawn")
	}
}

// No on-disk transcript ⇒ a keep-conversation --resume would silently fall through
// to a fresh session, so refresh refuses (409) rather than surprise-wiping context.
func TestRefreshNoTranscriptRejected(t *testing.T) {
	p := switchTestPane("pane_nt", "native", nil, "sess-no-transcript-xyz")
	api, spawnOpts, _ := resumeTestServer(t, p)
	rec := postRefresh(api, "pane_nt")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%q", rec.Code, rec.Body.String())
	}
	if len(*spawnOpts) != 0 {
		t.Fatalf("no-transcript refresh must not respawn")
	}
}

// Refresh is settle-gated: it refuses (409) while the agent is mid-turn rather
// than interrupting a working agent just to reclaim RAM.
func TestRefreshBusyRejected(t *testing.T) {
	p := switchTestPane("pane_b", "native", nil, "sess-xyz")
	p.ClaudeTranscriptPath = writeTempTranscript(t)
	p.nativeInTurn.Store(true)
	p.nativeOpenTools.Store(1) // makes nativeWorkingNow() return true deterministically
	api, spawnOpts, _ := resumeTestServer(t, p)
	rec := postRefresh(api, "pane_b")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%q", rec.Code, rec.Body.String())
	}
	if len(*spawnOpts) != 0 {
		t.Fatalf("busy refresh must not respawn")
	}
}

// The shared respawn single-flight rejects a refresh while another respawn
// (resume/switch/refresh) already holds it → 409, no second kill.
func TestRefreshSingleFlightConflict(t *testing.T) {
	p := switchTestPane("pane_sf", "native", nil, "sess-xyz")
	p.ClaudeTranscriptPath = writeTempTranscript(t)
	api, spawnOpts, killed := resumeTestServer(t, p)
	if !p.tryAcquireRespawn() {
		t.Fatal("precondition: could not acquire respawn lock")
	}
	rec := postRefresh(api, "pane_sf")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%q", rec.Code, rec.Body.String())
	}
	if len(*spawnOpts) != 0 || len(*killed) != 0 {
		t.Fatalf("refresh must not respawn while another holds the lock: spawns=%d kills=%d", len(*spawnOpts), len(*killed))
	}
}

// The SOLU_PANE_REFRESH kill-switch disables the endpoint (400, no respawn).
func TestRefreshDisabledByEnv(t *testing.T) {
	t.Setenv("SOLU_PANE_REFRESH", "0")
	p := switchTestPane("pane_d", "native", nil, "sess-xyz")
	p.ClaudeTranscriptPath = writeTempTranscript(t)
	api, spawnOpts, _ := resumeTestServer(t, p)
	rec := postRefresh(api, "pane_d")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%q", rec.Code, rec.Body.String())
	}
	if len(*spawnOpts) != 0 {
		t.Fatalf("disabled refresh must not respawn")
	}
}

// Regression for the BUNDLED switch-mode fix: a Terminal↔Focus switch must
// preserve AutoMode (the ARIA auto-approval policy), flag+memo, LastPrompts, and
// the original CreatedAt — previously dropped silently and persisted (a real
// safety hole: auto-approval turned itself off on every switch).
func TestSwitchModePreservesAutoModeAndFlags(t *testing.T) {
	t.Setenv("SOLU_NATIVE_RENDERER", "1")
	orig := switchTestPane("pane_sw", "", []string{"bash"}, "sess-123")
	orig.AutoMode = "always"
	orig.Flagged = true
	orig.FlagMemo = "important"
	orig.LastPrompts = []string{"do the thing"}
	created := orig.CreatedAt
	api, _, _ := resumeTestServer(t, orig)

	req := httptest.NewRequest(http.MethodPost, "/api/panes/pane_sw/switch-mode", strings.NewReader(`{"kind":"native"}`))
	req.SetPathValue("id", "pane_sw")
	rec := httptest.NewRecorder()
	api.handleSwitchMode(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}

	np, ok := api.mgr.Get("pane_sw")
	if !ok {
		t.Fatal("switched pane missing from manager")
	}
	snap := np.Snapshot()
	if snap.AutoMode != "always" {
		t.Fatalf("AutoMode dropped on switch: %q (ARIA auto-approval silently disabled)", snap.AutoMode)
	}
	if !snap.Flagged || snap.FlagMemo != "important" {
		t.Fatalf("flag dropped on switch: flagged=%v memo=%q", snap.Flagged, snap.FlagMemo)
	}
	if len(snap.LastPrompts) != 1 || snap.LastPrompts[0] != "do the thing" {
		t.Fatalf("LastPrompts dropped on switch: %#v", snap.LastPrompts)
	}
	if !snap.CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt reset on switch: got %v want %v", snap.CreatedAt, created)
	}
}
