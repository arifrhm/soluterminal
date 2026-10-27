package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Helper: write small files into the ring slots so tests can verify
// that the right contents end up in the right slot after rotation.
func seedRing(t *testing.T, current string, contents map[int]string) {
	t.Helper()
	for i, c := range contents {
		path := ringSlot(current, i)
		if i < 0 {
			// i = -1 sentinel for "the current binary itself"
			path = current
		}
		if err := os.WriteFile(path, []byte(c), 0o755); err != nil {
			t.Fatalf("seed slot %d: %v", i, err)
		}
	}
}

func ringContents(t *testing.T, current string) map[int]string {
	t.Helper()
	out := map[int]string{}
	for i := 0; i < oldRingDepth; i++ {
		b, err := os.ReadFile(ringSlot(current, i))
		if err != nil {
			continue
		}
		out[i] = string(b)
	}
	return out
}

func TestRingSlotNaming(t *testing.T) {
	t.Parallel()
	got := []string{
		ringSlot("/x/solud", 0),
		ringSlot("/x/solud", 1),
		ringSlot("/x/solud", 2),
	}
	want := []string{"/x/solud.old", "/x/solud.old.1", "/x/solud.old.2"}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("slot %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRotateOldRingForwardShiftsAndDropsOldest(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	current := filepath.Join(tmp, "solud")
	// Seed: .old has v3 (most recent prior), .old.1 has v2, .old.2 has v1.
	// After rotateOldRingForward, v1 should be gone, v2 → .old.2, v3 → .old.1,
	// and .old should be empty (caller fills it next).
	seedRing(t, current, map[int]string{
		0: "v3",
		1: "v2",
		2: "v1",
	})

	rotateOldRingForward(current)

	got := ringContents(t, current)
	if _, exists := got[0]; exists {
		t.Errorf(".old should be empty after forward-rotate, got %q", got[0])
	}
	if got[1] != "v3" {
		t.Errorf(".old.1 = %q, want v3", got[1])
	}
	if got[2] != "v2" {
		t.Errorf(".old.2 = %q, want v2 (v1 should have been dropped)", got[2])
	}
}

func TestRotateOldRingForwardSparseRing(t *testing.T) {
	t.Parallel()
	// Only .old exists (early in a daemon's life — first update from
	// pristine install). Forward-rotate should move it to .old.1 and
	// leave .old empty.
	tmp := t.TempDir()
	current := filepath.Join(tmp, "solud")
	seedRing(t, current, map[int]string{0: "first-prior"})

	rotateOldRingForward(current)

	got := ringContents(t, current)
	if _, exists := got[0]; exists {
		t.Errorf(".old should be empty, got %q", got[0])
	}
	if got[1] != "first-prior" {
		t.Errorf(".old.1 = %q, want first-prior", got[1])
	}
}

func TestRotateOldRingBackwardShiftsTowardNewest(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	current := filepath.Join(tmp, "solud")
	// After RestorePrevious has consumed .old, the ring has slots
	// 1+2 populated. Backward-rotate should pull both one step newer.
	seedRing(t, current, map[int]string{
		1: "v2",
		2: "v1",
	})

	rotateOldRingBackward(current)

	got := ringContents(t, current)
	if got[0] != "v2" {
		t.Errorf(".old = %q, want v2", got[0])
	}
	if got[1] != "v1" {
		t.Errorf(".old.1 = %q, want v1", got[1])
	}
	if _, exists := got[2]; exists {
		t.Errorf(".old.2 should be empty after backward-rotate, got %q", got[2])
	}
}

func TestRotateOldRingForwardEmptyIsNoop(t *testing.T) {
	t.Parallel()
	// Pristine install: no .old files at all. Rotate should not error
	// (it's best-effort) and the ring should remain empty.
	tmp := t.TempDir()
	current := filepath.Join(tmp, "solud")
	rotateOldRingForward(current)
	if got := ringContents(t, current); len(got) != 0 {
		t.Errorf("rotation on empty ring left files: %v", got)
	}
}

func TestRollbackSwappedBinaryRestoresOldAndRewindsRing(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	current := filepath.Join(tmp, "solud")
	seedRing(t, current, map[int]string{
		-1: "new",
		0:  "old",
		1:  "older",
	})

	if err := rollbackSwappedBinary(current); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "old" {
		t.Fatalf("current = %q, want old", b)
	}
	got := ringContents(t, current)
	if got[0] != "older" {
		t.Fatalf(".old = %q, want older", got[0])
	}
	if _, err := os.Stat(current + ".spawn-failed"); !os.IsNotExist(err) {
		t.Fatalf("spawn-failed binary still exists: %v", err)
	}
}
