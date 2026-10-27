package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAriaSearchFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "alpha.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatalf("write alpha: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "beta.go"), []byte("package beta"), 0o644); err != nil {
		t.Fatalf("write beta: %v", err)
	}
	got := (&Aria{}).toolSearchFiles(root, "beta", "files", 10)
	var parsed struct {
		Hits []fileSearchHit `json:"hits"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("search result is not JSON: %v (%q)", err, got)
	}
	if len(parsed.Hits) != 1 || parsed.Hits[0].Name != "beta.go" {
		t.Fatalf("hits = %#v, want beta.go", parsed.Hits)
	}
}

func TestAriaReadFileBoundsAndRefusesEnv(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	got := (&Aria{}).toolReadFile(path, 2, 1)
	if !strings.Contains(got, `"line":2`) || !strings.Contains(got, `"text":"two"`) {
		t.Fatalf("read result = %q, want line 2", got)
	}
	envPath := filepath.Join(root, ".env")
	if err := os.WriteFile(envPath, []byte("TOKEN=secret\n"), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}
	got = (&Aria{}).toolReadFile(envPath, 1, 10)
	if !strings.Contains(got, "refusing to access likely secret/env file") {
		t.Fatalf("env read = %q, want refusal", got)
	}
	// The agent read path now uses the full secretPathRefusal denylist, not
	// just .env detection — so SSH keys and other credential files are
	// refused too.
	keyPath := filepath.Join(root, "id_rsa")
	if err := os.WriteFile(keyPath, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if got := (&Aria{}).toolReadFile(keyPath, 1, 10); !strings.Contains(got, "refusing to access") {
		t.Fatalf("key read = %q, want refusal", got)
	}
}
