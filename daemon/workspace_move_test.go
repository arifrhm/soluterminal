package main

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func paneMoveFixture(t *testing.T) *WorkspaceStore {
	t.Helper()
	s, err := NewWorkspaceStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []*Workspace{
		{Name: "A", PaneIds: []string{"p", "keep"}, Panes: []PaneDef{{ID: "p", Cmd: []string{"bash"}, ClaudeSessionID: "conversation", Model: "original", ContextPack: "/original-pack"}, {ID: "keep"}}, Layout: LayoutNode{"type": "split", "ratio": 0.3, "direction": "vertical", "a": LayoutNode{"type": "pane", "paneId": "p"}, "b": LayoutNode{"type": "pane", "paneId": "keep"}}},
		{Name: "B", PaneIds: []string{}, Layout: LayoutNode{"type": "file", "fileId": "file"}, Files: []FileTileInfoDef{{ID: "file", Path: "/keep.md"}}, Protected: true},
	} {
		if err := s.Put(w); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestPaneMoveFailedPersistenceChangesNeitherWorkspace(t *testing.T) {
	s := paneMoveFixture(t)
	before := s.List()
	path := s.path
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	obstacle := filepath.Join(filepath.Dir(path), "blocked")
	if err := os.WriteFile(obstacle, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(obstacle, "workspaces.json")
	if _, _, err := s.MovePane("A", "B", "p", "horizontal", "", 0); !errors.Is(err, errWorkspacePersistence) {
		t.Fatalf("move error = %v", err)
	}
	for _, want := range before {
		got, _ := s.Get(want.Name)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("failed move changed %s: %+v", want.Name, got)
		}
	}
	if len(s.recentAdds) != 0 {
		t.Fatalf("failed move changed bookkeeping: %+v", s.recentAdds)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(disk) {
		t.Fatalf("failed move changed disk: %v", err)
	}
}

func TestPaneMoveRejectsStaleSavesAfterRestart(t *testing.T) {
	s := paneMoveFixture(t)
	oldA, _ := s.Get("A")
	oldB, _ := s.Get("B")
	a, b, err := s.MovePane("A", "B", "p", "horizontal", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if a.PaneMoveVersion != 1 || b.PaneMoveVersion != 1 {
		t.Fatalf("versions: %d %d", a.PaneMoveVersion, b.PaneMoveVersion)
	}
	if a.Layout["paneId"] != "keep" || b.Files[0].Path != "/keep.md" || !b.Protected {
		t.Fatalf("unrelated layout/metadata changed: %+v %+v", a, b)
	}
	if b.Panes[0].ClaudeSessionID != "conversation" || b.Panes[0].Model != "original" || b.Panes[0].ContextPack != "/original-pack" {
		t.Fatalf("pane definition changed: %+v", b.Panes)
	}
	reloaded, err := NewWorkspaceStore(filepath.Dir(s.path))
	if err != nil {
		t.Fatal(err)
	}
	for _, stale := range []*Workspace{oldA, oldB} {
		if err := reloaded.Put(stale); !errors.Is(err, errWorkspaceMoveConflict) {
			t.Fatalf("stale %s save = %v", stale.Name, err)
		}
	}
	if _, _, err := reloaded.MovePane("B", "A", "p", "horizontal", "", 0); !errors.Is(err, errWorkspaceMoveConflict) {
		t.Fatalf("stale move = %v", err)
	}
	if err := reloaded.Put(b); err != nil {
		t.Fatalf("current save failed: %v", err)
	}
	if _, _, err := reloaded.MovePane("B", "A", "p", "horizontal", "", b.PaneMoveVersion); err != nil {
		t.Fatalf("move back failed: %v", err)
	}
}

func TestPaneMoveAutosavePreservesWorkspaceProtection(t *testing.T) {
	s := paneMoveFixture(t)
	_, destination, err := s.MovePane("A", "B", "p", "horizontal", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	// The normal client autosave sends layout data; workspace protection is
	// server-owned and must survive the move's destination save.
	save := &Workspace{
		Name: destination.Name, Layout: destination.Layout,
		PaneIds: destination.PaneIds, Panes: destination.Panes,
		Files: destination.Files, PaneMoveVersion: destination.PaneMoveVersion,
	}
	if err := s.Put(save); err != nil {
		t.Fatal(err)
	}
	actual, _ := s.Get("B")
	if !actual.Protected || !save.Protected {
		t.Fatal("the destination autosave removed workspace protection")
	}
}

func TestPaneMoveInvalidRequestsPreserveBothWorkspaces(t *testing.T) {
	for _, tc := range []struct{ name, source, destination, pane string }{
		{"missing destination", "A", "missing", "p"},
		{"missing source", "missing", "B", "p"},
		{"same workspace", "A", "A", "p"},
		{"scratch destination", "A", "_current", "p"},
		{"missing pane", "A", "B", "missing"},
		{"managed owner", "A", "B", "p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := paneMoveFixture(t)
			if tc.name == "managed owner" {
				s.SetOwnerBindings(func() map[string]string { return map[string]string{"p": "task"} })
			}
			beforeA, _ := s.Get("A")
			beforeB, _ := s.Get("B")
			if _, _, err := s.MovePane(tc.source, tc.destination, tc.pane, "horizontal", "", 0); err == nil {
				t.Fatal("invalid move succeeded")
			}
			a, _ := s.Get("A")
			b, _ := s.Get("B")
			if !reflect.DeepEqual(a, beforeA) || !reflect.DeepEqual(b, beforeB) {
				t.Fatal("invalid move changed workspaces")
			}
		})
	}
}

func TestMoveWorkspacePaneAPIKeepsProcessIdentity(t *testing.T) {
	s := paneMoveFixture(t)
	p := &Pane{ID: "p", Pid: 123, Status: PaneRunning, Cmd: []string{"bash"}, ClaudeSessionID: "conversation"}
	a := &apiServer{ws: s, mgr: &Manager{panes: map[string]*Pane{"p": p}}}
	r := httptest.NewRequest("POST", "/api/workspaces/A/move-pane", strings.NewReader(`{"paneId":"p","destination":"B"}`))
	r.SetPathValue("name", "A")
	w := httptest.NewRecorder()
	a.moveWorkspacePane(w, r)
	if w.Code != 200 {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
	var result struct{ Source, Destination *Workspace }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Source.PaneMoveVersion != 1 || result.Destination.PaneMoveVersion != 1 {
		t.Fatal("missing authoritative versions")
	}
	got, _ := a.mgr.Get("p")
	if got != p || p.Pid != 123 || p.ClaudeSessionID != "conversation" || p.Status != PaneRunning {
		t.Fatal("move changed running pane")
	}

	// A delayed old client cannot undo the move through the ordinary save API.
	w = httptest.NewRecorder()
	a.putWorkspace(w, httptest.NewRequest("POST", "/api/workspaces", strings.NewReader(`{"name":"A","paneIds":["p"],"layout":{"type":"pane","paneId":"p"}}`)))
	if w.Code != 409 {
		t.Fatalf("stale save status = %d", w.Code)
	}
}

func TestMoveWorkspacePaneAPIRejectsOwnersAndEndedPanes(t *testing.T) {
	for _, tc := range []struct {
		name, role string
		status     PaneStatus
	}{
		{"owner", paneRoleTaskOwner, PaneRunning}, {"ended", "", PaneExited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := paneMoveFixture(t)
			a := &apiServer{ws: s, mgr: &Manager{panes: map[string]*Pane{"p": {ID: "p", Role: tc.role, Status: tc.status}}}}
			r := httptest.NewRequest("POST", "/api/workspaces/A/move-pane", strings.NewReader(`{"paneId":"p","destination":"B"}`))
			r.SetPathValue("name", "A")
			w := httptest.NewRecorder()
			a.moveWorkspacePane(w, r)
			if w.Code != 400 {
				t.Fatalf("status = %d", w.Code)
			}
			after, _ := s.Get("A")
			if len(after.PaneIds) != 2 {
				t.Fatal("rejected move changed source")
			}
		})
	}
}
