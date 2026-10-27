//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// os.Open(dir).Sync() is ERROR_ACCESS_DENIED on Windows; syncDir must still
// succeed there for every caller that persists a directory entry.
func TestSyncDirSucceedsOnWindows(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "entry"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syncDir(dir); err != nil {
		t.Fatalf("syncDir: %v", err)
	}
	if err := syncDir(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("syncDir on a missing directory should fail")
	}
}

func TestProcessTreeFromParentsDropsSelfParent(t *testing.T) {
	tree := processTreeFromParents(map[int]int{0: 0, 4: 0})
	if got := tree.children[0]; !slices.Equal(got, []int{4}) {
		t.Fatalf("children[0] = %v, want [4]", got)
	}
}

// A real child must still be found, and a stale PPID that would make this
// (older) test process a "child" of its own newer child must be rejected.
func TestDescendantPidsRejectsRecycledParentEdge(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "ping -n 30 127.0.0.1 >NUL")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start child: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	self, child := os.Getpid(), cmd.Process.Pid

	got := freshProcessTreeSnapshot().descendantPids(self)
	if !slices.Contains(got, child) {
		t.Fatalf("live child %d missing from descendants %v", child, got)
	}

	cyclic := processTreeFromParents(map[int]int{child: self, self: child})
	if got := cyclic.descendantPids(child); slices.Contains(got, self) {
		t.Fatalf("older process %d adopted via recycled ppid: %v", self, got)
	}
	if got := cyclic.descendantPids(self); !slices.Equal(got, []int{self, child}) {
		t.Fatalf("descendantPids(self) = %v, want [%d %d]", got, self, child)
	}
}
