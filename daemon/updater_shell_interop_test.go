package main

import (
	"os"
	"os/exec"
	"testing"
)

// TestEnvWithInterop is a pure unit test (runs everywhere, incl. CI) for
// the WSL_INTEROP env rewrite used when launching the shell installer.
func TestEnvWithInterop(t *testing.T) {
	// Replaces an existing entry, preserves the rest, no duplicates.
	got := envWithInterop([]string{"A=1", "WSL_INTEROP=/old", "B=2"}, "/new")
	var interopCount int
	var sawA, sawB bool
	for _, kv := range got {
		switch kv {
		case "WSL_INTEROP=/new":
			interopCount++
		case "WSL_INTEROP=/old":
			t.Errorf("stale WSL_INTEROP=/old survived: %v", got)
		case "A=1":
			sawA = true
		case "B=2":
			sawB = true
		}
	}
	if interopCount != 1 {
		t.Errorf("want exactly one WSL_INTEROP=/new, got %d in %v", interopCount, got)
	}
	if !sawA || !sawB {
		t.Errorf("unrelated env vars were dropped: %v", got)
	}

	// Appends when absent.
	got = envWithInterop([]string{"A=1"}, "/new")
	var found bool
	for _, kv := range got {
		if kv == "WSL_INTEROP=/new" {
			found = true
		}
	}
	if !found {
		t.Errorf("WSL_INTEROP was not appended: %v", got)
	}
}

// TestLiveWSLInteropResolves is the real-host regression guard for the
// v0.0.621 "apply installer did nothing" bug: a long-lived/systemd daemon
// holds a stale WSL_INTEROP so every Windows launch fails. It must resolve
// a WORKING socket even when the inherited one is dead. Gated behind
// SOLU_TEST_WSL_INTEROP=1 so it never runs in the publish test gate (it
// spawns powershell.exe and depends on a live WSL host); run manually:
//
//	SOLU_TEST_WSL_INTEROP=1 go test ./ -run TestLiveWSLInteropResolves -v
func TestLiveWSLInteropResolves(t *testing.T) {
	if os.Getenv("SOLU_TEST_WSL_INTEROP") == "" {
		t.Skip("set SOLU_TEST_WSL_INTEROP=1 to run (spawns powershell.exe on a WSL host)")
	}
	if !isInsideWSL() {
		t.Skip("not inside WSL")
	}
	psExe := "powershell.exe"
	if _, err := exec.LookPath(psExe); err != nil {
		fallback := "/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe"
		if _, statErr := os.Stat(fallback); statErr != nil {
			t.Skip("powershell.exe not available")
		}
		psExe = fallback
	}

	// Simulate the bug: inherited socket is dead/nonexistent.
	t.Setenv("WSL_INTEROP", "/run/WSL/nonexistent_interop")

	sock := liveWSLInterop(psExe)
	if sock == "" {
		t.Fatal("liveWSLInterop found no working interop socket on a WSL host")
	}
	if !interopWorks(psExe, sock) {
		t.Fatalf("resolved socket %s does not actually launch a Windows process", sock)
	}
	t.Logf("resolved live interop socket despite a dead inherited one: %s", sock)
}
