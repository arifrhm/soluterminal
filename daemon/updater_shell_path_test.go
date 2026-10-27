package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestApplyShellPathConversionTimeoutRetainsManualRecovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake wslpath command")
	}
	u, dataDir, staged := shellApplyTestUpdater(t)
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "wslpath"), []byte("#!/bin/sh\nexec /bin/sleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	u.shellDisplayPath = func(path, _ string) (string, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		out, err := runWSLPath(ctx, "-w", path)
		return strings.TrimSpace(string(out)), err
	}
	launches := 0
	u.launchShell = func(string, string, *shellLaunchLog) (launchResult, error) {
		launches++
		return launchResult{}, errors.New("installer must not launch without a converted path")
	}
	_, err := u.ApplyShell(t.Context(), dataDir, false)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), staged) || !strings.Contains(err.Error(), "open it manually") || !strings.Contains(err.Error(), "prepare Windows installer path") {
		t.Fatalf("conversion timeout lost its cause or manual recovery: %v", err)
	}
	status := u.Status()
	if launches != 0 || status.ShellApplying || status.ShellLaunch == nil || status.ShellLaunch.State != "launch-failed" || status.ShellLaunch.StagedPath != staged {
		t.Fatalf("conversion timeout left a launch or in-progress state: launches=%d status=%+v", launches, status)
	}
	if data, err := os.ReadFile(staged); err != nil || string(data) != "verified NSIS installer bytes" {
		t.Fatalf("verified installer was not retained for manual recovery: %q (%v)", data, err)
	}
	logBody, err := os.ReadFile(filepath.Join(dataDir, "shell-update", "launch.log"))
	if err != nil || !strings.Contains(string(logBody), `"event":"launch-failed"`) {
		t.Fatalf("conversion timeout was not recorded: %s (%v)", logBody, err)
	}

	// A timeout must not leave shellApplying latched and block a later attempt.
	u.shellDisplayPath = func(path, _ string) (string, error) { return path, nil }
	u.launchShell = func(string, string, *shellLaunchLog) (launchResult, error) {
		launches++
		return launchResult{Method: "test", PID: "123", Session: "1"}, nil
	}
	if _, err := u.ApplyShell(t.Context(), dataDir, false); err != nil || launches != 1 {
		t.Fatalf("conversion timeout blocked a later attempt: launches=%d err=%v", launches, err)
	}
}
