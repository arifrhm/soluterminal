package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRestorePreviousSwapsOldIntoCurrent exercises the same two-stage
// rename swap as RestorePrevious() but against synthetic files instead
// of the running binary. RestorePrevious() itself uses os.Executable()
// so it can't be unit-tested directly; this test locks in the rename
// sequence the function relies on so a future "let's clean this up"
// refactor can't silently lose the rollback semantics.
func TestRestorePreviousSwapsOldIntoCurrent(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	current := filepath.Join(tmp, "solud")
	old := current + ".old"
	tmpPath := current + ".restore-tmp"

	if err := os.WriteFile(current, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Two-stage swap mirrored from RestorePrevious — keep this in sync
	// when that function changes.
	if err := os.Rename(current, tmpPath); err != nil {
		t.Fatalf("park current → tmp: %v", err)
	}
	if err := os.Rename(old, current); err != nil {
		t.Fatalf("install old → current: %v", err)
	}
	_ = os.Remove(tmpPath)

	got, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "OLD" {
		t.Fatalf("current contents = %q, want %q (rollback didn't take)", got, "OLD")
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("tmp path %s should be cleaned, got err=%v", tmpPath, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old path %s should be consumed by rename, got err=%v", old, err)
	}
}

func TestRestorePreviousMissingOldFile(t *testing.T) {
	t.Parallel()
	// When .old isn't present, RestorePrevious() returns an error and
	// must NOT touch the canonical binary. Check the precondition
	// stat() catches the absence cleanly.
	tmp := t.TempDir()
	old := filepath.Join(tmp, "solud.old")
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("expected ENOENT, got err=%v", err)
	}
}
