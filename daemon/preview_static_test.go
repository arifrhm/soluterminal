package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"testing"
	"time"
)

func TestPreviewInventoryCancellationDuringWalk(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	visited := 0
	files, bytes, err := previewTreeFilesFilteredContext(ctx, root, false, func(string, os.DirEntry) bool {
		visited++
		cancel()
		return false
	})
	if !errors.Is(err, context.Canceled) || visited != 1 || files != nil || bytes != 0 {
		t.Fatalf("cancelled inventory continued or claimed a complete measurement: visited=%d files=%v bytes=%d err=%v", visited, files, bytes, err)
	}
}

// Observe filesystem progress at cancellation checkpoints without scheduling
// races or global deletion hooks. Cancellation starts after real first removal.
type previewCancelOnProgressContext struct {
	context.Context
	check func()
}

func (c previewCancelOnProgressContext) Err() error {
	c.check()
	return c.Context.Err()
}

func TestPreviewSweepCancellationStopsFurtherMutation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("descriptor-guarded removal is Linux-only")
	}
	cacheRoot := t.TempDir()
	old := time.Now().Add(-time.Hour)
	first := previewReportBundle(t, cacheRoot, strings.Repeat("a", 64), 64, old)
	second := previewReportBundle(t, cacheRoot, strings.Repeat("b", 64), 64, old.Add(time.Minute))
	if err := os.Chmod(second, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(second, 0o700) })
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	sawRemoval := false
	ctx := previewCancelOnProgressContext{Context: parent, check: func() {
		if _, err := os.Lstat(first); os.IsNotExist(err) {
			sawRemoval = true
			cancel()
		}
	}}
	err := previewCacheSweepToLimitContext(ctx, cacheRoot, nil, 0)
	if !sawRemoval || !errors.Is(err, context.Canceled) {
		t.Fatalf("did not cancel inside an executing sweep: sawRemoval=%v err=%v", sawRemoval, err)
	}
	if info, err := os.Stat(second); err != nil || info.Mode().Perm() != 0o500 {
		t.Fatalf("later candidate was removed or chmodded after cancellation: info=%v err=%v", info, err)
	}
	if info, err := os.Stat(filepath.Join(second, "payload.bin")); err != nil || info.Size() != 64 {
		t.Fatalf("later payload changed after cancellation: info=%v err=%v", info, err)
	}
}

func TestPreviewMaintenanceLockWaitHonorsDeadline(t *testing.T) {
	manager := &previewManager{}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := manager.pruneCacheContext(ctx, 0)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("lock wait ignored inherited deadline: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("preview manager lock wait outlived deadline")
	}
}

func previewReportBundle(t *testing.T, root, name string, size int64, used time.Time) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(path, "payload.bin")
	if err := os.WriteFile(payload, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(payload, size); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(previewBundleManifest{SchemaVersion: 1, Digest: "sha256:" + name, Size: size, CreatedAt: used, LastUsedAt: used})
	if err := os.WriteFile(filepath.Join(path, previewBundleManifestName), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
