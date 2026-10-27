package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOwnerWorkspaceSanitizationAtomicStaleSave(t *testing.T) {
	s, err := NewWorkspaceStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ordinary := LayoutNode{"type": "split", "id": "survivor", "ratio": 0.37, "direction": "horizontal", "a": LayoutNode{"type": "pane", "paneId": "p1"}, "b": LayoutNode{"type": "tasks", "tasksId": "generic"}}
	stale := &Workspace{Name: "A", Protected: true, ContextPack: "/context", ActiveAt: time.Now(), Layout: LayoutNode{"type": "split", "ratio": 0.61, "a": ordinary, "b": LayoutNode{"type": "pane", "paneId": "owner"}}, PaneIds: []string{"p1", "owner"}, Panes: []PaneDef{{ID: "p1"}, {ID: "owner", Role: paneRoleTaskOwner}}}
	if err := s.Put(stale); err != nil {
		t.Fatal(err)
	}
	s.recentAdds["A"] = map[string]time.Time{"owner": time.Now(), "p1": time.Now()}
	s.SetOwnerBindings(func() map[string]string {
		if !s.mu.TryLock() {
			t.Fatal("supplier invoked inside workspace lock")
		}
		s.mu.Unlock()
		return map[string]string{"owner": "task"}
	})
	before, _ := s.Get("A")
	adds := cloneWorkspaceRecentAdds(s.recentAdds)
	input := cloneWorkspace(stale)
	originalPath := s.path
	blocked := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocked, []byte("x"), 0600)
	s.path = filepath.Join(blocked, "bad")
	if s.Put(input) == nil {
		t.Fatal("injected persistence failure passed")
	}
	after, _ := s.Get("A")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(adds, s.recentAdds) || !reflect.DeepEqual(input, stale) {
		t.Fatal("failed transaction changed data/input/bookkeeping")
	}
	s.path = originalPath
	if err := s.DetachManagedOwnerPresentation(); err != nil {
		t.Fatal(err)
	}
	migrated, _ := s.Get("A")
	if !reflect.DeepEqual(migrated.Layout, ordinary) || !migrated.ActiveAt.Equal(before.ActiveAt) || migrated.ContextPack != "/context" || !migrated.Protected {
		t.Fatalf("migration lost unrelated state: %+v", migrated)
	}
	if err := s.DetachManagedOwnerPresentation(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(stale)
	r := httptest.NewRequest("PUT", "/api/workspaces", strings.NewReader(string(raw)))
	w := httptest.NewRecorder()
	(&apiServer{ws: s}).putWorkspace(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var response Workspace
	json.Unmarshal(w.Body.Bytes(), &response)
	committed, _ := s.Get("A")
	responseJSON, _ := json.Marshal(response)
	committedJSON, _ := json.Marshal(committed)
	ordinaryJSON, _ := json.Marshal(ordinary)
	layoutJSON, _ := json.Marshal(response.Layout)
	if string(responseJSON) != string(committedJSON) || string(layoutJSON) != string(ordinaryJSON) || len(response.Panes) != 1 || len(response.PaneIds) != 1 {
		t.Fatalf("stale response regraft: %s", w.Body.String())
	}
	if _, ok := s.recentAdds["A"]["owner"]; ok {
		t.Fatal("owner recent addition survived")
	}
}
func TestOwnerOnlyClearAndUnknownOwnerLookingID(t *testing.T) {
	s, _ := NewWorkspaceStore(t.TempDir())
	s.Put(&Workspace{Name: "owners", PaneIds: []string{"bound"}, Layout: LayoutNode{"type": "pane", "paneId": "bound"}})
	s.SetOwnerBindings(func() map[string]string { return map[string]string{"bound": "task"} })
	candidate := &Workspace{Name: "owners", PaneIds: []string{"bound"}, Layout: LayoutNode{"type": "tasks", "tasksId": "bound", "ownerTaskId": "task"}}
	if err := s.Put(candidate); err != nil || candidate.Layout != nil || len(candidate.PaneIds) != 0 {
		t.Fatalf("owner-only clear: %+v %v", candidate, err)
	}
	s.Put(&Workspace{Name: "ordinary", PaneIds: []string{"owner_looks_managed"}, Layout: LayoutNode{"type": "pane", "paneId": "owner_looks_managed"}})
	if err := s.Put(&Workspace{Name: "ordinary"}); err == nil {
		t.Fatal("ordinary clear guard weakened")
	}
	s.DetachManagedOwnerPresentation()
	got, _ := s.Get("ordinary")
	if got.Layout == nil {
		t.Fatal("prefix guessed ownership")
	}
}

func TestOwnerWorkspaceConcurrentBindingsMigrationAndStaleSaves(t *testing.T) {
	store, err := NewWorkspaceStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var bindingsMu sync.Mutex
	bindings := map[string]string{"owner": "task"}
	store.SetOwnerBindings(func() map[string]string {
		bindingsMu.Lock()
		defer bindingsMu.Unlock()
		copy := map[string]string{}
		for pane, task := range bindings {
			copy[pane] = task
		}
		return copy
	})
	var work sync.WaitGroup
	failures := make(chan error, 200)
	for worker := 0; worker < 3; worker++ {
		work.Add(1)
		go func(worker int) {
			defer work.Done()
			for iteration := 0; iteration < 40; iteration++ {
				switch worker {
				case 0:
					bindingsMu.Lock()
					bindings["owner"] = "task"
					bindingsMu.Unlock()
				case 1:
					if err := store.Put(&Workspace{Name: "Work", Layout: LayoutNode{"type": "split", "ratio": 0.37, "a": LayoutNode{"type": "pane", "paneId": "ordinary"}, "b": LayoutNode{"type": "pane", "paneId": "owner"}}, PaneIds: []string{"ordinary", "owner"}, Panes: []PaneDef{{ID: "ordinary"}, {ID: "owner", Role: paneRoleTaskOwner}}}); err != nil {
						failures <- err
					}
				case 2:
					if err := store.DetachManagedOwnerPresentation(); err != nil {
						failures <- err
					}
				}
			}
		}(worker)
	}
	work.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	workspace, ok := store.Get("Work")
	if !ok || len(workspace.PaneIds) != 1 || workspace.PaneIds[0] != "ordinary" || workspace.Layout["paneId"] != "ordinary" {
		t.Fatalf("concurrent stale regraft: %+v", workspace)
	}
}
