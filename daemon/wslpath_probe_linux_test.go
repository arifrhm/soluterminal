//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func installTestWSLPath(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wslpath"), []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	// Only the synthetic executable is discoverable; a real WSL conversion
	// cannot run even when this test is executed on a WSL host.
	t.Setenv("PATH", dir)
}

func TestWSLPathProbePreservesArgumentsAndFailure(t *testing.T) {
	t.Run("arguments", func(t *testing.T) {
		installTestWSLPath(t, "printf '%s\\n' \"$@\"\n")
		out, err := runWSLPath(context.Background(), "-w", "/tmp/path with spaces/'quote'")
		if err != nil || string(out) != "-w\n/tmp/path with spaces/'quote'\n" {
			t.Fatalf("conversion argv changed: output=%q err=%v", out, err)
		}
	})
	t.Run("failure", func(t *testing.T) {
		installTestWSLPath(t, "printf 'synthetic path failure' >&2\nexit 17\n")
		out, err := runWSLPath(context.Background(), "-w", "/tmp/example")
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 17 || string(exit.Stderr) != "synthetic path failure" || len(out) != 0 {
			t.Fatalf("conversion failure was hidden: output=%q err=%v", out, err)
		}
	})
}

func TestWSLPathProbeHungProcessIsBoundedAndReaped(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	quotedSleep := "'" + strings.ReplaceAll(sleep, "'", "'\"'\"'") + "'"
	installTestWSLPath(t, "printf '%s' \"$$\" > \"$2.pid\"\nprintf 'started\\n'\nexec "+quotedSleep+" 60\n")
	marker := filepath.Join(t.TempDir(), "probe")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	out, err := runWSLPath(ctx, "-w", marker)
	if !errors.Is(err, context.DeadlineExceeded) || string(out) != "started\n" {
		t.Fatalf("hung conversion was not cancelled after starting: output=%q err=%v", out, err)
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("hung conversion exceeded deadline and drain allowance: %s", elapsed)
	}
	raw, err := os.ReadFile(marker + ".pid")
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid synthetic process identity %q: %v", raw, err)
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled conversion process was not reaped: pid=%d err=%v", pid, err)
	}
}

func TestWSLPathProbeInheritedOutputIsBounded(t *testing.T) {
	installTestWSLPath(t, "printf '%s' \"$$\" > \"$2.pid\"\nread ready < \"$2.fifo\"\nprintf 'converted\\n'\n")
	marker := filepath.Join(t.TempDir(), "probe")
	if err := syscall.Mkfifo(marker+".fifo", 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	type result struct {
		out []byte
		err error
	}
	completed := make(chan result, 1)
	done := make(chan struct{})
	go func() {
		out, err := runWSLPath(ctx, "-w", marker)
		completed <- result{out, err}
		close(done)
	}()
	defer func() { cancel(); <-done }()
	var pid int
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for pid == 0 {
		if raw, err := os.ReadFile(marker + ".pid"); err == nil {
			pid, _ = strconv.Atoi(string(raw))
		}
		if pid != 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("synthetic conversion did not start")
		case <-ticker.C:
		}
	}
	// Duplicate the child's stdout writer while it is waiting. This has the
	// same pipe-liveness effect as an inheriting descendant, without creating
	// an orphan that this test cannot reap.
	writer, err := os.OpenFile(fmt.Sprintf("/proc/%d/fd/1", pid), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	// Opening both ends is nonblocking even if the shell has written its PID
	// but has not yet reached read; retain the FIFO until the probe finishes.
	release, err := os.OpenFile(marker+".fifo", os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer release.Close()
	if _, err := release.WriteString("continue\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-completed:
		if !errors.Is(got.err, exec.ErrWaitDelay) || string(got.out) != "converted\n" || ctx.Err() != nil {
			t.Fatalf("held stdout did not return through WaitDelay: output=%q err=%v context=%v", got.out, got.err, ctx.Err())
		}
	case <-ctx.Done():
		t.Fatal("held stdout waited for the command deadline instead of bounded drain")
	}
}
