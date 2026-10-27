package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// failingPaneStore is written to by watcher GOROUTINES in the deferred-switch
// tests while the test body polls it, so every access goes through mu — an
// unsynchronized putIDs read kept the go-race CI job red from v0.0.869.
type failingPaneStore struct {
	mu      sync.Mutex
	putErr  error
	putIDs  []string
	removed []string
}

func (s *failingPaneStore) List() []PersistedPane {
	return nil
}

func (s *failingPaneStore) Put(p *Pane) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.putIDs = append(s.putIDs, p.ID)
	return s.putErr
}

func (s *failingPaneStore) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removed = append(s.removed, id)
	return nil
}

// putIDsSnapshot / removedSnapshot are the race-safe read side for tests
// that poll while a watcher goroutine may still be live.
func (s *failingPaneStore) putIDsSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.putIDs...)
}

func (s *failingPaneStore) removedSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.removed...)
}

func transactionTestPane(id string) *Pane {
	done := make(chan struct{})
	close(done)
	return &Pane{
		ID:        id,
		Cwd:       "/tmp",
		Cmd:       []string{"bash"},
		Title:     "bash",
		Status:    PaneRunning,
		CreatedAt: time.Now().UTC(),
		Cols:      80,
		Rows:      24,
		done:      done,
		buffer:    NewRingBuffer(1024),
		subs:      map[int64]*subEntry{},
	}
}

func TestCreatePanePrevalidatesWorkspaceBeforeSpawn(t *testing.T) {
	t.Parallel()
	ws, err := NewWorkspaceStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspaceStore: %v", err)
	}
	spawnCalled := false
	api := &apiServer{
		mgr:   NewManager(t.TempDir()),
		ws:    ws,
		panes: &failingPaneStore{},
		spawnPane: func(SpawnOpts) (*Pane, error) {
			spawnCalled = true
			return transactionTestPane("pane_should_not_spawn"), nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/panes", strings.NewReader(`{"cmd":["bash"],"workspace":"Missing"}`))
	rec := httptest.NewRecorder()
	api.createPane(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if spawnCalled {
		t.Fatal("spawn was called before rejecting missing workspace")
	}
}

func TestMCPSpawnPanePrevalidatesWorkspaceBeforeSpawn(t *testing.T) {
	t.Parallel()
	ws, err := NewWorkspaceStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspaceStore: %v", err)
	}
	spawnCalled := false
	api := &apiServer{
		mgr:   NewManager(t.TempDir()),
		ws:    ws,
		panes: &failingPaneStore{},
		spawnPane: func(SpawnOpts) (*Pane, error) {
			spawnCalled = true
			return transactionTestPane("pane_should_not_spawn"), nil
		},
	}

	got, ok := api.mcpToolSpawnPane(map[string]any{"cmd": "bash", "workspace": "Missing"}, "").(map[string]any)
	if !ok {
		t.Fatalf("mcpToolSpawnPane returned %T", got)
	}
	if isErr, _ := got["isError"].(bool); !isErr {
		t.Fatalf("mcpToolSpawnPane = %#v, want error", got)
	}
	if spawnCalled {
		t.Fatal("spawn was called before rejecting missing workspace")
	}
}

func TestCreatePaneRollsBackSpawnWhenPanePersistFails(t *testing.T) {
	t.Parallel()
	mgr := NewManager(t.TempDir())
	store := &failingPaneStore{putErr: errors.New("disk full")}
	killed := false
	api := &apiServer{
		mgr:   mgr,
		panes: store,
		spawnPane: func(SpawnOpts) (*Pane, error) {
			p := transactionTestPane("pane_persist_fail")
			mgr.panes[p.ID] = p
			return p, nil
		},
		killPane: func(id string) error {
			killed = true
			delete(mgr.panes, id)
			return nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/panes", strings.NewReader(`{"cmd":["bash"]}`))
	rec := httptest.NewRecorder()
	api.createPane(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if !killed {
		t.Fatal("spawned pane was not killed after persist failure")
	}
	if _, ok := mgr.Get("pane_persist_fail"); ok {
		t.Fatal("spawned pane remained in manager after rollback")
	}
	if got := store.removedSnapshot(); len(got) != 1 || got[0] != "pane_persist_fail" {
		t.Fatalf("removed = %#v, want pane_persist_fail", got)
	}
}

func TestCreatePaneRollsBackSpawnWhenWorkspaceAttachFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ws, err := NewWorkspaceStore(dir)
	if err != nil {
		t.Fatalf("NewWorkspaceStore: %v", err)
	}
	if err := ws.Put(&Workspace{Name: "Desk"}); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	// Force the next workspace save to fail by making its directory
	// read-only (the atomic-write helper can't create its temp file).
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("block workspace dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	mgr := NewManager(t.TempDir())
	store := &failingPaneStore{}
	killed := false
	api := &apiServer{
		mgr:   mgr,
		ws:    ws,
		panes: store,
		spawnPane: func(SpawnOpts) (*Pane, error) {
			p := transactionTestPane("pane_attach_fail")
			mgr.panes[p.ID] = p
			return p, nil
		},
		killPane: func(id string) error {
			killed = true
			delete(mgr.panes, id)
			return nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/panes", strings.NewReader(`{"cmd":["bash"],"workspace":"Desk"}`))
	rec := httptest.NewRecorder()
	api.createPane(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if !killed {
		t.Fatal("spawned pane was not killed after workspace attach failure")
	}
	if got := store.removedSnapshot(); len(got) != 1 || got[0] != "pane_attach_fail" {
		t.Fatalf("removed = %#v, want pane_attach_fail", got)
	}
	if w, ok := ws.Get("Desk"); !ok || len(w.PaneIds) != 0 {
		t.Fatalf("workspace after rollback = %#v, want no attached pane", w)
	}
}

func TestPaneStorePutKeepsMemoryUnchangedWhenSaveFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store, err := NewPaneStore(dir)
	if err != nil {
		t.Fatalf("NewPaneStore: %v", err)
	}
	if err := store.Put(transactionTestPane("pane_existing")); err != nil {
		t.Fatalf("initial Put: %v", err)
	}
	// Force the next save to fail by making the store directory read-only
	// (the atomic-write helper can't create its temp file).
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("block pane dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err = store.Put(transactionTestPane("pane_new"))
	if err == nil {
		t.Fatal("Put succeeded with tmp path blocked")
	}
	got := store.List()
	if len(got) != 1 || got[0].ID != "pane_existing" {
		t.Fatalf("store.List() = %#v, want only pane_existing", got)
	}
}
