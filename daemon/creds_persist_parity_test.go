package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These registry-style parity assertions pin audit T2
// (docs/audits/2026-08-22-credentials-multi-model-readiness.md §T2): provider
// stores must retain usage timestamps and keep active pointers on the shared
// atomic, directory-synced 0600 writer. The behavior is already correct; these
// tables keep a provider added later from silently regressing to os.WriteFile.

func assertCredCacheMode0600(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("%s mode = %o, want 600", path, got)
	}
}

func assertCredPointerAtomic0600(t *testing.T, path, want string, exact bool) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if exact && got != want {
		t.Fatalf("%s content = %q, want %q", path, got, want)
	}
	if !exact && !strings.Contains(got, want) {
		t.Fatalf("%s content = %q, want it to contain %q", path, got, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("%s mode = %o, want 600", path, mode)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") && strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("atomic temp file remained beside %s: %s", path, entry.Name())
		}
	}
}
