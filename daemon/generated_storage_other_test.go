//go:build !linux

package main

import (
	"context"
	"testing"
)

func TestGeneratedStorageNativeUnsupportedProducersRemainUsable(t *testing.T) {
	// The engine's task temp dirs went with the engine; previews still seal.
	seal, _, release, err := previewCreateOwnedSeal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if !previewSealActive(seal) {
		t.Fatal("seal not pinned")
	}
}

func TestGeneratedStorageUnsupportedPlatformRefusesMutation(t *testing.T) {
	if err := generatedRemove(context.Background(), t.TempDir(), generatedFSIdentity{}, nil); generatedReason(err) != "filesystem_identity_unsupported" {
		t.Fatalf("unsupported platform removal err=%v", err)
	}
}
