package main

import (
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWorkspaceFailedWriteKeepsCommittedState(t *testing.T) {
	for _, operation := range []string{"rename", "delete", "context", "default", "put", "add", "remove"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			s, err := NewWorkspaceStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			name := "A"
			if operation == "default" {
				name = "_current"
			}
			if err := s.Put(&Workspace{Name: name, PaneIds: []string{"p1"}, Panes: []PaneDef{{ID: "p1"}}, Layout: LayoutNode{"type": "pane", "paneId": "p1"}}); err != nil {
				t.Fatal(err)
			}
			before, _ := s.Get(name)
			path := s.path
			diskBefore, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// A file in place of the parent directory deterministically prevents a
			// temp-file write even when tests run as an elevated user.
			obstacle := filepath.Join(dir, "not-a-directory")
			if err := os.WriteFile(obstacle, []byte("blocked"), 0600); err != nil {
				t.Fatal(err)
			}
			s.path = filepath.Join(obstacle, "workspaces.json")
			switch operation {
			case "rename":
				err = s.Rename(name, "B")
			case "delete":
				err = s.Delete(name)
			case "context":
				_, err = s.SetContextPack(name, "/new-pack")
			case "default":
				err = s.EnsureDefault()
			case "put":
				err = s.Put(&Workspace{Name: name, PaneIds: []string{"p2"}, Layout: LayoutNode{"type": "pane", "paneId": "p2"}})
			case "add":
				_, err = s.AddPane(name, PaneDef{ID: "p2"}, "horizontal", "p1")
			case "remove":
				err = s.RemovePane("p1")
			}
			if !errors.Is(err, errWorkspacePersistence) {
				t.Fatalf("write error = %v", err)
			}
			got, ok := s.Get(name)
			if !ok || !reflect.DeepEqual(got, before) || len(s.List()) != 1 {
				t.Fatalf("failed %s changed memory: got %+v, before %+v", operation, got, before)
			}
			diskAfter, err := os.ReadFile(path)
			if err != nil || string(diskBefore) != string(diskAfter) {
				t.Fatalf("failed write changed disk: %v", err)
			}
			// A later successful write must not smuggle in the rejected operation.
			s.path = path
			if err := s.Put(&Workspace{Name: "unrelated"}); err != nil {
				t.Fatal(err)
			}
			reloaded, err := NewWorkspaceStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			got, ok = reloaded.Get(name)
			if !ok || !reflect.DeepEqual(got, before) || len(reloaded.List()) != 2 {
				t.Fatalf("later save persisted failed %s: %+v", operation, got)
			}
		})
	}
}

func TestRenameWorkspaceStorageFailureIsRetryable(t *testing.T) {
	s, err := NewWorkspaceStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(&Workspace{Name: "A"}); err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(s.path, "blocked.json")
	a := &apiServer{ws: s}
	r := httptest.NewRequest("POST", "/api/workspaces/A/rename", strings.NewReader(`{"newName":"B"}`))
	r.SetPathValue("name", "A")
	w := httptest.NewRecorder()
	a.renameWorkspace(w, r)
	if w.Code != 500 || !strings.Contains(w.Body.String(), "retry") {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
	if _, ok := s.Get("A"); !ok {
		t.Fatal("failed request lost original workspace")
	}
}
