//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestGeneratedStorageDirectoryEnumerationChecksCancellationBetweenBatches(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 600; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("entry-%04d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs := generatedNativeFS()
	batches := 0
	fs.readDirNames = func(file *os.File, n int) ([]string, error) {
		batches++
		if n != 256 {
			t.Fatalf("unbounded enumeration size=%d", n)
		}
		names, err := file.Readdirnames(n)
		cancel()
		return names, err
	}
	if _, err := fs.readNames(ctx, fd); !errors.Is(err, context.Canceled) || batches != 1 {
		t.Fatalf("err=%v batches=%d", err, batches)
	}
}

func TestGeneratedStorageMarkedMutationRechecksAfterPreflight(t *testing.T) {
	for _, removeRoot := range []bool{false, true} {
		t.Run(fmt.Sprintf("removeRoot=%v", removeRoot), func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(root, ".marker")
			sentinel := filepath.Join(root, "sentinel")
			if err := os.WriteFile(marker, []byte("owned"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			identity, err := generatedIdentity(root)
			if err != nil {
				t.Fatal(err)
			}
			fs := generatedMarkedFS(".marker", func(raw []byte) error {
				if string(raw) != "owned" {
					return errors.New("ownership_marker_mismatch")
				}
				return nil
			})
			fs.afterPreflight = func() {
				if err := os.WriteFile(marker, []byte("replacement"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			preserve := ".marker"
			if removeRoot {
				preserve = ""
			}
			if err := fs.remove(context.Background(), root, identity, nil, preserve, removeRoot); err == nil {
				t.Fatal("changed ownership marker permitted mutation")
			}
			if raw, err := os.ReadFile(sentinel); err != nil || string(raw) != "keep" {
				t.Fatalf("sentinel=%q err=%v", raw, err)
			}
		})
	}
}

func TestGeneratedStorageGuardedTraversalPreflightAndLateHazards(t *testing.T) {
	for _, boundary := range []string{"root", "directory", "file", "unsupported", "late"} {
		t.Run(boundary, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "candidate")
			if err := os.MkdirAll(filepath.Join(root, "child"), 0700); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(root, "child", "sentinel")
			if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			identity, err := generatedIdentity(root)
			if err != nil {
				t.Fatal(err)
			}
			late := false
			fs := generatedFS{open: func(fd int, name string, how *unix.OpenHow) (int, error) {
				if boundary == "unsupported" {
					return -1, unix.ENOSYS
				}
				if (boundary == "root" && name == "candidate") || (boundary == "directory" && name == "child") || (boundary == "file" && name == "sentinel") || (boundary == "late" && late && name == "child") {
					if how.Resolve&unix.RESOLVE_NO_XDEV == 0 {
						t.Fatal("missing mount-boundary resolution")
					}
					return -1, unix.EXDEV
				}
				return unix.Openat2(fd, name, how)
			}, afterPreflight: func() { late = true }}
			err = fs.remove(context.Background(), root, identity, nil, "", true)
			want := "mount_crossing"
			if boundary == "unsupported" {
				want = "filesystem_identity_unsupported"
			}
			if generatedReason(err) != want {
				t.Fatalf("err=%v want=%s", err, want)
			}
			if raw, err := os.ReadFile(sentinel); err != nil || string(raw) != "keep" {
				t.Fatalf("sentinel=%q err=%v", raw, err)
			}
		})
	}
}

func TestGeneratedStorageRootRejectsIncompleteIdentity(t *testing.T) {
	root := t.TempDir()
	identity, err := generatedIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(root, "keep")
	if err := os.WriteFile(sentinel, []byte("owned"), 0600); err != nil {
		t.Fatal(err)
	}
	identity.Inode = 0
	if err := generatedRemoveTree(context.Background(), root, identity); err == nil {
		t.Fatal("accepted wildcard root identity")
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatal(err)
	}
}

func TestGeneratedStorageGuardedTraversalRemovesAllReadOnlyChildren(t *testing.T) {
	root := filepath.Join(t.TempDir(), "candidate")
	for _, dir := range []string{"one", "two", "three"} {
		child := filepath.Join(root, dir)
		if err := os.MkdirAll(child, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(child, "data"), []byte("owned"), 0400); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(child, 0500); err != nil {
			t.Fatal(err)
		}
	}
	identity, err := generatedIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, complete, reason := generatedMeasure(context.Background(), root, identity); !complete {
		t.Fatal(reason)
	}
	if err := generatedRemoveTree(context.Background(), root, identity); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("root survived: %v", err)
	}
}

func TestGeneratedStorageGuardedPreflightRefusesSymlinkWithoutPartialDeletion(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	sentinel := filepath.Join(root, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	identity, err := generatedIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := generatedRemoveTree(context.Background(), root, identity); generatedReason(err) != "symlink_detected" {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatal(err)
	}
}

func TestGeneratedStorageDescriptorRemovalRefusesDescendantDeviceMismatch(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(child, "sentinel")
	if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := generatedIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := generatedRemoveAt(context.Background(), fd, "child", identity.Device+1); err == nil || generatedReason(err) != "mount_crossing" {
		t.Fatalf("device mismatch err=%v", err)
	}
	if raw, err := os.ReadFile(sentinel); err != nil || string(raw) != "untouched" {
		t.Fatalf("cross-device refusal changed sentinel raw=%q err=%v", raw, err)
	}
}
