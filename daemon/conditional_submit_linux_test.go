//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Deliberately no environ/session files: foreground checks need neither
// credentials nor a real provider. All process metadata lives in t.TempDir.
func conditionalFakeProcess(t *testing.T, proc string, pid, parent, group, foreground int, start, comm, cwd string, argv ...string) {
	t.Helper()
	dir := filepath.Join(proc, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields("S 0 0 0 34818 0 0 0 0 0 0 0 0 0 0 0 0 0 0 1")
	fields[1], fields[2], fields[3], fields[5], fields[19] = strconv.Itoa(parent), strconv.Itoa(group), strconv.Itoa(group), strconv.Itoa(foreground), start
	for name, value := range map[string]string{"stat": fmt.Sprintf("%d (%s) %s\n", pid, comm, strings.Join(fields, " ")), "comm": comm + "\n", "cmdline": strings.Join(argv, "\x00") + "\x00"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(cwd, filepath.Join(dir, "cwd")); err != nil {
		t.Fatal(err)
	}
}

func TestConditionalSubmitProcessProbe(t *testing.T) {
	for _, mode := range []string{"codex", "claude", "background", "shell", "ambiguous", "missing_cwd", "missing_stat", "zombie", "headless", "remote_pair", "restart"} {
		t.Run(mode, func(t *testing.T) {
			proc, cwd := t.TempDir(), t.TempDir()
			cwd, _ = filepath.EvalSymlinks(cwd)
			conditionalFakeProcess(t, proc, 100, 1, 100, 101, "1000", "solud", cwd, "solud", "agent")
			conditionalFakeProcess(t, proc, 101, 100, 101, 102, "1001", "bash", cwd, "bash")
			provider, foreground := "codex", 102
			if mode == "claude" {
				provider = "claude"
			}
			if mode == "shell" {
				provider = "bash"
			}
			if mode == "background" {
				foreground = 101
			}
			argv := []string{provider}
			if mode == "headless" {
				argv = []string{"codex", "app-server"}
			}
			conditionalFakeProcess(t, proc, 102, 101, 102, foreground, "2000", provider, cwd, argv...)
			if mode == "ambiguous" {
				conditionalFakeProcess(t, proc, 103, 101, 102, 102, "2001", "claude", cwd, "claude")
			}
			if mode == "remote_pair" {
				conditionalFakeProcess(t, proc, 103, 101, 102, 102, "2001", "codex", cwd, "codex", "app-server")
			}
			if mode == "missing_cwd" {
				if err := os.Remove(filepath.Join(proc, "102/cwd")); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "missing_stat" {
				if err := os.Remove(filepath.Join(proc, "102/stat")); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "zombie" {
				path := filepath.Join(proc, "102/stat")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(strings.Replace(string(raw), ") S ", ") Z ", 1)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := probeConditionalSubmitProcessAt(context.Background(), proc, 100)
			wantOK := mode == "codex" || mode == "claude" || mode == "remote_pair" || mode == "restart"
			if !wantOK {
				if err == nil {
					t.Fatalf("unsafe foreground accepted: %+v", got)
				}
				return
			}
			if err != nil || got.Pid != 102 || got.Provider != provider || got.Cwd != cwd || got.StartTime != "2000" {
				t.Fatalf("probe %+v %v", got, err)
			}
			if mode == "restart" {
				path := filepath.Join(proc, "102/stat")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(strings.Replace(string(raw), "2000", "3000", 1)), 0600); err != nil {
					t.Fatal(err)
				}
				next, err := probeConditionalSubmitProcessAt(context.Background(), proc, 100)
				if err != nil || next.StartTime == got.StartTime {
					t.Fatalf("same-PID restart not distinguished: %+v %v", next, err)
				}
			}
		})
	}
}

func TestConditionalSubmitProcessProbeCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := probeConditionalSubmitProcessAt(ctx, "/unused-fixture-proc", 100)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled probe did filesystem work: %v", err)
	}
}
