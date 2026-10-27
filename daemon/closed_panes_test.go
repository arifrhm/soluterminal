package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Review finding on #365: the client replayed a closed pane's raw argv (stale
// `--resume <old id>`, lost role/harness context, other backend's recipe).
// Reopen now rebuilds from the daemon's own snapshot via resumeSpawnOpts.
func TestReopenSpawnOptsUsesCurrentIdentityNotStaleArgv(t *testing.T) {
	e := closedPaneEntry{Snap: PaneSnapshot{
		ID: "pane_old", Cwd: "/repo", Title: "orch",
		Cmd:             []string{"claude", "--resume", "OLD-SESSION"},
		ClaudeSessionID: "CURRENT-SESSION",
		Role:            "orchestrator",
		Effort:          "high",
	}}
	opts := reopenSpawnOpts(e, 120, 40)
	if opts.ID != "" {
		t.Fatalf("reopen must spawn a NEW pane, got id %q", opts.ID)
	}
	joined := strings.Join(opts.Cmd, " ")
	if strings.Contains(joined, "OLD-SESSION") || !strings.Contains(joined, "CURRENT-SESSION") {
		t.Fatalf("reopen cmd = %v, want a resume of the current session only", opts.Cmd)
	}
	if opts.Role != "orchestrator" || opts.Effort != "high" || opts.Cwd != "/repo" {
		t.Fatalf("lost pane settings: role=%q effort=%q cwd=%q", opts.Role, opts.Effort, opts.Cwd)
	}
	if opts.Cols != 120 || opts.Rows != 40 {
		t.Fatalf("live dims not used: %dx%d", opts.Cols, opts.Rows)
	}
}

func TestReopenSpawnOptsShellComesBackWhereItWas(t *testing.T) {
	e := closedPaneEntry{
		Snap:    PaneSnapshot{ID: "pane_sh", Cwd: "/home/u", Cmd: []string{"bash"}, Title: "shell"},
		LiveCwd: "/home/u/project/sub",
	}
	opts := reopenSpawnOpts(e, 0, 0)
	if opts.Cwd != "/home/u/project/sub" || strings.Join(opts.Cmd, " ") != "bash" || !opts.DimsUnverified {
		t.Fatalf("shell reopen = cwd %q cmd %v unverified=%v", opts.Cwd, opts.Cmd, opts.DimsUnverified)
	}
	// A claude pane whose identity was never captured must not replay a stale
	// --resume from its argv.
	stale := closedPaneEntry{Snap: PaneSnapshot{ID: "p", Cwd: "/r", Cmd: []string{"claude", "--resume", "GONE"}}}
	if got := strings.Join(reopenSpawnOpts(stale, 0, 0).Cmd, " "); got != "claude" {
		t.Fatalf("stale resume not stripped: %q", got)
	}
}

func TestClosedPaneStoreCapAgeAndPersistence(t *testing.T) {
	dir := t.TempDir()
	clock := time.Unix(1_800_000_000, 0)
	s := newClosedPaneStore(dir)
	s.now = func() time.Time { return clock }
	for i := 0; i < closedPanesCap+3; i++ {
		s.add(closedPaneEntry{Snap: PaneSnapshot{ID: "p" + string(rune('a'+i))}})
	}
	if n := len(s.entries()); n != closedPanesCap {
		t.Fatalf("kept %d, want cap %d", n, closedPanesCap)
	}
	if got := s.entries()[0].Snap.ID; got != "p"+string(rune('a'+closedPanesCap+2)) {
		t.Fatalf("newest first violated: %q", got)
	}
	// Survives a restart.
	s2 := newClosedPaneStore(dir)
	s2.now = func() time.Time { return clock }
	if len(s2.entries()) != closedPanesCap {
		t.Fatal("closed panes not persisted")
	}
	// Expire after a day.
	s2.now = func() time.Time { return clock.Add(closedPaneMaxAge + time.Minute) }
	if len(s2.entries()) != 0 {
		t.Fatal("old entries not pruned")
	}
}

func TestReopenClosedPaneEndpoint(t *testing.T) {
	var got SpawnOpts
	api := &apiServer{
		closed: newClosedPaneStore(t.TempDir()),
		spawnPane: func(opts SpawnOpts) (*Pane, error) {
			got = opts
			return transactionTestPane("reopened"), nil
		},
	}
	api.closed.add(closedPaneEntry{Snap: PaneSnapshot{ID: "pane_x", Cwd: "/r", Cmd: []string{"codex"}, Agent: "codex", CodexThreadID: "T-1"}})

	rec := httptest.NewRecorder()
	api.listClosedPanes(rec, httptest.NewRequest(http.MethodGet, "/api/panes/closed", nil))
	if !strings.Contains(rec.Body.String(), `"kind":"codex"`) {
		t.Fatalf("list = %s", rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/api/panes/closed/pane_x/reopen", strings.NewReader(`{"cols":100,"rows":30}`))
	req.SetPathValue("id", "pane_x")
	rec = httptest.NewRecorder()
	api.reopenClosedPane(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("reopen status %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Join(got.Cmd, " ") != "codex resume T-1" {
		t.Fatalf("reopen cmd = %v", got.Cmd)
	}
	// Consumed: a second reopen is a 404, not a duplicate pane.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/panes/closed/pane_x/reopen", nil)
	req.SetPathValue("id", "pane_x")
	api.reopenClosedPane(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second reopen status %d, want 404", rec.Code)
	}
}
