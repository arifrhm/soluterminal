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

func installSyntheticPowerShell(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetic-powershell")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPowerShellFixturePreservesArgumentsAndOrdinaryExit(t *testing.T) {
	t.Run("arguments", func(t *testing.T) {
		ps := installSyntheticPowerShell(t, "printf '%s\\n' \"$@\"\n")
		script := "'two words'; \"quoted\"; $literal"
		out, err := runTestPowerShell(t, ps, script, nil)
		want := "-NoProfile\n-NonInteractive\n-ExecutionPolicy\nBypass\n-Command\n" + script + "\n"
		if err != nil || string(out) != want {
			t.Fatalf("fixture argv changed: output=%q err=%v", out, err)
		}
	})
	t.Run("ordinary refusal", func(t *testing.T) {
		ps := installSyntheticPowerShell(t, "printf 'fixture verdict\\n'\nprintf 'failure diagnostic\\n' >&2\nexit 17\n")
		resets := 0
		out, err := runTestPowerShell(t, ps, "ignored", func() { resets++ })
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 17 || string(out) != "fixture verdict\nfailure diagnostic\n" || resets != 1 {
			t.Fatalf("ordinary refusal changed or retried: output=%q err=%v resets=%d", out, err, resets)
		}
	})
	t.Run("installer argv", func(t *testing.T) {
		ps := installSyntheticPowerShell(t, "printf '%s\\n' \"$@\"\nexit 21\n")
		out, err := runTestPowerShellCommand(t, t.Context(), ps, "-NoProfile", "-NonInteractive", "-Command", "installer fixture")
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 21 || string(out) != "-NoProfile\n-NonInteractive\n-Command\ninstaller fixture\n" {
			t.Fatalf("installer argv or exit changed: output=%q err=%v", out, err)
		}
	})
}

func TestPowerShellFixtureRetriesExplicitTransportFailure(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "first-attempt")
	t.Setenv("SOLU_TEST_POWERSHELL_ATTEMPT", marker)
	ps := installSyntheticPowerShell(t, `if [ ! -e "$SOLU_TEST_POWERSHELL_ATTEMPT" ]; then
	: > "$SOLU_TEST_POWERSHELL_ATTEMPT"
	printf 'UtilAcceptVsock: synthetic transport failure\n' >&2
	exit 1
fi
printf 'fixture ran\n'
`)
	resets := 0
	out, err := runTestPowerShell(t, ps, "ignored", func() { resets++ })
	if err != nil || string(out) != "fixture ran\n" || resets != 2 {
		t.Fatalf("explicit transport retry changed: output=%q err=%v resets=%d", out, err, resets)
	}
}

// Run a real nested test process: a helper that returns a timeout as an ordinary
// error would make the negative assertion below PASS. The parent requires a
// test failure from the lifecycle guard, rather than accepting an error value.
func TestPowerShellFixtureLifecycleFailureCannotPassAsRefusal(t *testing.T) {
	if mode := os.Getenv("SOLU_TEST_POWERSHELL_WITNESS"); mode != "" {
		runPowerShellLifecycleFailureWitness(t, mode)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, want string }{
		{"deadline", "context deadline exceeded"},
		{"deadline with transport marker", "context deadline exceeded"},
		{"cancelled", "context canceled"},
		{"held output", "WaitDelay expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "process")
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestPowerShellFixtureLifecycleFailureCannotPassAsRefusal$", "-test.v", "-test.timeout=8s")
			cmd.WaitDelay = 500 * time.Millisecond
			cmd.Env = append(os.Environ(), "SOLU_TEST_POWERSHELL_WITNESS="+tc.name, "SOLU_TEST_POWERSHELL_PROCESS="+marker)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), "PowerShell fixture did not finish") || !strings.Contains(string(out), tc.want) || strings.Contains(string(out), "accepted as refusal") || strings.Contains(string(out), "--- SKIP") {
				t.Fatalf("lifecycle failure did not fail the negative test: err=%v context=%v output=%s", err, ctx.Err(), out)
			}
			if tc.name != "cancelled" {
				raw, err := os.ReadFile(marker + ".pid")
				if err != nil {
					t.Fatal(err)
				}
				pid, err := strconv.Atoi(string(raw))
				if err != nil || pid <= 0 {
					t.Fatalf("invalid synthetic process identity %q: %v", raw, err)
				}
				if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("synthetic PowerShell process was not reaped: pid=%d err=%v", pid, err)
				}
			}
		})
	}
}

func runPowerShellLifecycleFailureWitness(t *testing.T, mode string) {
	t.Helper()
	marker := os.Getenv("SOLU_TEST_POWERSHELL_PROCESS")
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	body := "printf '%s' \"$$\" > \"$SOLU_TEST_POWERSHELL_PROCESS.pid\"\nexec /bin/sleep 60\n"
	switch mode {
	case "deadline":
	case "deadline with transport marker":
		body = "printf 'UtilAcceptVsock: synthetic transport failure\\n' >&2\n" + body
	case "cancelled":
		cancel()
	case "held output":
		// The test owns the extra pipe writer, so no orphaned descendants are
		// needed to simulate inherited output that survives command exit.
		cancel()
		ctx, cancel = context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		if err := syscall.Mkfifo(marker+".fifo", 0600); err != nil {
			t.Fatal(err)
		}
		body = "printf '%s' \"$$\" > \"$SOLU_TEST_POWERSHELL_PROCESS.pid\"\nread ready < \"$SOLU_TEST_POWERSHELL_PROCESS.fifo\"\nprintf 'fixture exited\\n'\n"
		release := make(chan struct{})
		done := make(chan error, 1)
		go func() { done <- holdPowerShellFixtureOutput(ctx, marker, release) }()
		defer func() {
			close(release)
			if err := <-done; err != nil {
				t.Errorf("hold synthetic output: %v", err)
			}
		}()
	default:
		t.Fatalf("unknown witness mode %q", mode)
	}
	ps := installSyntheticPowerShell(t, body)
	out, err := runTestPowerShellContext(t, ctx, ps, "ignored", nil)
	// Deliberately mirrors the pre-existing expected-refusal callers. Timeout
	// handling must fail before reaching this assertion or the skip machinery.
	if err != nil && !strings.Contains(string(out), `"args"`) {
		t.Log("accepted as refusal")
		return
	}
	t.Fatal("synthetic launch was not refused")
}

func holdPowerShellFixtureOutput(ctx context.Context, marker string, release <-chan struct{}) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	var pid int
	for pid == 0 {
		if raw, err := os.ReadFile(marker + ".pid"); err == nil {
			pid, _ = strconv.Atoi(string(raw))
		}
		if pid != 0 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	writer, err := os.OpenFile(fmt.Sprintf("/proc/%d/fd/1", pid), os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer writer.Close()
	fifo, err := os.OpenFile(marker+".fifo", os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer fifo.Close()
	if _, err := fifo.WriteString("continue\n"); err != nil {
		return err
	}
	select {
	case <-release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
