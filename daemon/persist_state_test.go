package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersistedPaneContextPackJSON(t *testing.T) {
	empty, err := json.Marshal(PersistedPane{ID: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), "contextPack") {
		t.Fatalf("empty context pack fields were not omitted: %s", empty)
	}
	want := PersistedPane{ID: "p", ContextPack: "/packs/strategy", ContextPackStatus: contextPackInjected}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got PersistedPane
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.ContextPack != want.ContextPack || got.ContextPackStatus != want.ContextPackStatus {
		t.Fatalf("round trip = %+v", got)
	}
}

func TestWriteFileAtomicSyncedReplaces(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	if err := writeFileAtomicSynced(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if err := writeFileAtomicSynced(path, []byte("second"), 0o600); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Fatalf("content = %q, want second", got)
	}
	// No leftover temp files in the directory.
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("expected only the state file, got %d entries", len(entries))
	}
}

func TestPaneStoreCorruptFileDoesNotBrickStartup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "panes.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// NewPaneStore must NOT return an error on a corrupt file (it would be
	// fatal in main.go); it should quarantine and start fresh.
	s, err := NewPaneStore(dir)
	if err != nil {
		t.Fatalf("NewPaneStore on corrupt file returned error: %v", err)
	}
	if got := s.List(); len(got) != 0 {
		t.Fatalf("expected empty list after corrupt load, got %d", len(got))
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Fatalf("expected corrupt file quarantined to %s.corrupt: %v", path, err)
	}
}
