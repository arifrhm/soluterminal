//go:build linux

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPreviewSealCleanupPreservesActiveAndReplacedOwnership(t *testing.T) {
	seal, _, release, err := previewCreateOwnedSeal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	marker, err := previewReadSealMarker(seal)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(seal, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := previewRemoveInactiveSeal(context.Background(), seal, marker); err == nil {
		t.Fatal("active seal was removed")
	}
	release()
	changed := marker
	changed.OwnershipID += "-replaced"
	raw, _ := json.Marshal(changed)
	if err := os.WriteFile(filepath.Join(seal, previewSealMarkerName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := previewRemoveInactiveSeal(context.Background(), seal, marker); err == nil {
		t.Fatal("changed seal ownership was accepted")
	}
	if raw, err := os.ReadFile(sentinel); err != nil || string(raw) != "keep" {
		t.Fatalf("sentinel=%q err=%v", raw, err)
	}
	raw, _ = json.Marshal(marker)
	if err := os.WriteFile(filepath.Join(seal, previewSealMarkerName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := previewRemoveInactiveSeal(context.Background(), seal, marker); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(seal); !os.IsNotExist(err) {
		t.Fatalf("owned inactive seal remains: %v", err)
	}
}

func TestPreviewSealFailureCleanupUsesCreationProof(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		name := "original-owner"
		if replaced {
			name = "replacement-owner"
		}
		t.Run(name, func(t *testing.T) {
			src, cache := t.TempDir(), t.TempDir()
			input := filepath.Join(src, "index.html")
			if err := os.WriteFile(input, []byte("preview"), 0600); err != nil {
				t.Fatal(err)
			}
			var sealPath string
			create := func(root string) (string, previewSealMarker, func(), error) {
				seal, marker, release, err := previewCreateOwnedSeal(root)
				if err != nil {
					return seal, marker, release, err
				}
				t.Cleanup(release)
				sealPath = seal
				if replaced {
					changed := marker
					changed.OwnershipID += "-replacement"
					raw, _ := json.Marshal(changed)
					if err := os.WriteFile(filepath.Join(seal, previewSealMarkerName), raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(seal, "sentinel"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
				// Digesting already succeeded. Fail the actual copy so the
				// production deferred cleanup runs against the changed marker.
				if err := os.Remove(input); err != nil {
					t.Fatal(err)
				}
				return seal, marker, release, nil
			}
			if _, _, err := previewSealBundleWithCreator(src, cache, create); !os.IsNotExist(err) {
				t.Fatalf("expected source-copy failure, got %v", err)
			}
			if previewSealActive(sealPath) {
				t.Fatal("failed seal kept active pin")
			}
			if replaced {
				if raw, err := os.ReadFile(filepath.Join(sealPath, "sentinel")); err != nil || string(raw) != "keep" {
					t.Fatalf("replacement owner data changed: %q %v", raw, err)
				}
			} else if _, err := os.Stat(sealPath); !os.IsNotExist(err) {
				t.Fatalf("original owned seal was not cleaned: %v", err)
			}
		})
	}
}

func TestPreviewSealMarkerGuardedRead(t *testing.T) {
	for _, kind := range []string{"regular", "symlink", "oversized", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			seal, _, release, err := previewCreateOwnedSeal(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			path := filepath.Join(seal, previewSealMarkerName)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			anchor := -1
			defer func() {
				if anchor >= 0 {
					_ = unix.Close(anchor)
				}
			}()
			switch kind {
			case "symlink":
				outside := filepath.Join(t.TempDir(), "marker")
				if err := os.WriteFile(outside, raw, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				// Still valid JSON, so size must be rejected by the guarded reader.
				for len(raw) <= 16<<10 {
					raw = append(raw, ' ')
				}
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
				anchor, err = unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK, 0)
				if err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			go func() { _, err := previewReadSealMarker(seal); done <- err }()
			select {
			case err := <-done:
				if (err == nil) != (kind == "regular") {
					t.Fatalf("marker kind=%s err=%v", kind, err)
				}
			case <-time.After(time.Second):
				// Release a regressed blocking FIFO reader before fixture cleanup.
				if anchor >= 0 {
					_ = unix.Close(anchor)
					anchor = -1
				}
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("marker reader did not join")
				}
				t.Fatal("marker inspection blocked")
			}
		})
	}
}
