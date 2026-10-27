package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRetainedMaterialCorruptionRefusesCleanupWithoutRepairingData(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "workflow", "retirement")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "committed.json")
	raw := []byte("{corrupt historical marker\n")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	authority, err := loadRetirementAuthority(dir)
	if err == nil {
		t.Fatal("corrupt retained marker accepted")
	}
	if _, _, err = retirementCleanupContext(context.Background(), authority, "", filepath.Join(dir, "cache")); !errors.Is(err, errRetirementAuthority) {
		t.Fatalf("cleanup was not refused: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != string(raw) {
		t.Fatal("retained marker was repaired or replaced")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("read-only load created or removed files: %v %v", entries, err)
	}
}
