package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeMarkerAttempts(t *testing.T, selfPath string, p updatePending) {
	t.Helper()
	b, _ := json.Marshal(p)
	if err := os.WriteFile(updatePendingPath(selfPath), b, 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
}

func TestUpdatePendingRoundTrip(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "solud")
	writeUpdatePending(self, "0.0.801", "0.0.800")
	got := readUpdatePending(self)
	if got == nil || got.To != "0.0.801" || got.From != "0.0.800" || got.Attempts != 0 {
		t.Fatalf("round-trip = %+v, want {0.0.801 0.0.800 0}", got)
	}
	clearUpdatePendingMarkerAt(self)
	if readUpdatePending(self) != nil {
		t.Fatalf("marker should be cleared")
	}
}

// clearUpdatePendingMarkerAt mirrors clearUpdatePendingMarker but for an explicit
// path (the production one resolves os.Executable()).
func clearUpdatePendingMarkerAt(selfPath string) { _ = os.Remove(updatePendingPath(selfPath)) }

func TestBootGuardIncrementsUnderThreshold(t *testing.T) {
	t.Setenv("SOLU_ROLLBACK_DONE", "")
	dir := t.TempDir()
	self := filepath.Join(dir, "solud")
	if err := os.WriteFile(self, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeMarkerAttempts(t, self, updatePending{To: "0.0.801", From: "0.0.800", Attempts: 1})

	called := 0
	orig := execInPlaceHook
	execInPlaceHook = func(path string, args, env []string) error { called++; return nil }
	defer func() { execInPlaceHook = orig }()

	maybeRollbackForSelf(self)

	if called != 0 {
		t.Fatalf("exec called %d times, want 0 (under threshold)", called)
	}
	got := readUpdatePending(self)
	if got == nil || got.Attempts != 2 {
		t.Fatalf("attempts = %+v, want 2", got)
	}
	if b, _ := os.ReadFile(self); string(b) != "NEW" {
		t.Fatalf("binary changed under threshold: %q", b)
	}
}

func TestBootGuardRollsBackAtThreshold(t *testing.T) {
	t.Setenv("SOLU_ROLLBACK_DONE", "")
	dir := t.TempDir()
	self := filepath.Join(dir, "solud")
	if err := os.WriteFile(self, []byte("NEW-BAD"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(self+".old", []byte("OLD-GOOD"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeMarkerAttempts(t, self, updatePending{To: "0.0.801", From: "0.0.800", Attempts: maxUpdateBootAttempts})

	called := 0
	gotPath := ""
	orig := execInPlaceHook
	execInPlaceHook = func(path string, args, env []string) error { called++; gotPath = path; return nil }
	defer func() { execInPlaceHook = orig }()

	maybeRollbackForSelf(self)

	if called != 1 || gotPath != self {
		t.Fatalf("exec called=%d path=%q, want 1 call with %q", called, gotPath, self)
	}
	if b, _ := os.ReadFile(self); string(b) != "OLD-GOOD" {
		t.Fatalf("binary after rollback = %q, want OLD-GOOD", b)
	}
	if readUpdatePending(self) != nil {
		t.Fatalf("marker should be cleared after rollback")
	}
}

func TestBootGuardNoOldClearsMarker(t *testing.T) {
	t.Setenv("SOLU_ROLLBACK_DONE", "")
	dir := t.TempDir()
	self := filepath.Join(dir, "solud")
	if err := os.WriteFile(self, []byte("NEW-BAD"), 0o755); err != nil {
		t.Fatal(err)
	}
	// No .old parked.
	writeMarkerAttempts(t, self, updatePending{To: "0.0.801", From: "0.0.800", Attempts: maxUpdateBootAttempts})

	called := 0
	orig := execInPlaceHook
	execInPlaceHook = func(path string, args, env []string) error { called++; return nil }
	defer func() { execInPlaceHook = orig }()

	maybeRollbackForSelf(self)

	if called != 0 {
		t.Fatalf("exec called %d, want 0 (no .old to roll back to)", called)
	}
	if readUpdatePending(self) != nil {
		t.Fatalf("marker should be cleared to avoid a boot loop when rollback can't act")
	}
	if b, _ := os.ReadFile(self); string(b) != "NEW-BAD" {
		t.Fatalf("binary should be untouched when there's nothing to restore: %q", b)
	}
}

func TestBootGuardRollbackDoneGuard(t *testing.T) {
	t.Setenv("SOLU_ROLLBACK_DONE", "1")
	dir := t.TempDir()
	self := filepath.Join(dir, "solud")
	if err := os.WriteFile(self, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(self+".old", []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeMarkerAttempts(t, self, updatePending{Attempts: maxUpdateBootAttempts})

	called := 0
	orig := execInPlaceHook
	execInPlaceHook = func(path string, args, env []string) error { called++; return nil }
	defer func() { execInPlaceHook = orig }()

	maybeRollbackForSelf(self)

	if called != 0 {
		t.Fatalf("exec called %d, want 0 (SOLU_ROLLBACK_DONE guard)", called)
	}
	if readUpdatePending(self) != nil {
		t.Fatalf("marker should be cleared under the loop guard")
	}
	if b, _ := os.ReadFile(self); string(b) != "NEW" {
		t.Fatalf("binary should be untouched under the loop guard: %q", b)
	}
}

func TestBootGuardNoMarkerNoop(t *testing.T) {
	t.Setenv("SOLU_ROLLBACK_DONE", "")
	dir := t.TempDir()
	self := filepath.Join(dir, "solud")
	if err := os.WriteFile(self, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	called := 0
	orig := execInPlaceHook
	execInPlaceHook = func(path string, args, env []string) error { called++; return nil }
	defer func() { execInPlaceHook = orig }()

	maybeRollbackForSelf(self) // no marker present

	if called != 0 {
		t.Fatalf("exec called %d, want 0 (no marker)", called)
	}
	if _, err := os.Stat(updatePendingPath(self)); !os.IsNotExist(err) {
		t.Fatalf("no marker should have been created")
	}
}
