//go:build !linux

package main

import (
	"errors"
	"testing"
)

func TestGeneratedStorageMountProtectionUnsupportedPlatform(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	identity, err := generatedProbeMount(left)
	if !errors.Is(err, errWFGeneratedUnsupported) || identity != (generatedMountIdentity{}) {
		t.Fatalf("unsupported probe returned proof: %+v %v", identity, err)
	}
	if _, err := generatedDescendantMounts(left, right); !errors.Is(err, errWFGeneratedUnsupported) {
		t.Fatalf("unsupported descendant evidence returned proof: %v", err)
	}
	if !generatedProtectedOverlap(left, right) {
		t.Fatal("unsupported production probe allowed cleanup")
	}
}
