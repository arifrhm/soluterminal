package main

import (
	"slices"
	"testing"
)

// A recycled PPID can make two processes each other's parent. The walk used to
// loop forever on such a snapshot, growing its queue until the daemon OOMed.
func TestDescendantPidsTerminatesOnParentCycle(t *testing.T) {
	tree := processTreeSnapshot{children: map[int][]int{
		100: {200},
		200: {300, 100}, // 100's stale ppid was recycled by its own grandchild
		300: {200},
	}}
	got := tree.descendantPids(100)
	slices.Sort(got)
	if want := []int{100, 200, 300}; !slices.Equal(got, want) {
		t.Fatalf("descendantPids = %v, want %v", got, want)
	}
}

func TestDescendantPidsTerminatesOnSelfParent(t *testing.T) {
	// Windows' System Idle process reports pid 0 as its own parent.
	tree := processTreeSnapshot{children: map[int][]int{0: {0, 4}, 4: {8}}}
	got := tree.descendantPids(0)
	slices.Sort(got)
	if want := []int{0, 4, 8}; !slices.Equal(got, want) {
		t.Fatalf("descendantPids = %v, want %v", got, want)
	}
}
