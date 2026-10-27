package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Test helpers shared by public tests.

func newTestScheduleStore(t *testing.T) *ScheduleStore {
	t.Helper()
	s, err := NewScheduleStore(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s
}

// waitForRun polls until the schedule has a run recorded (agent runs land
// from a goroutine) or times out.
func waitForRun(t *testing.T, s *ScheduleStore, id string) ScheduleRun {
	t.Helper()
	// Generous on purpose: the run is recorded only after a real process
	// spawn, and under the race detector on a host saturated by parallel
	// verification suites that spawn alone has been observed to exceed 5s.
	// The assertion is unchanged — a run that never appears still fails.
	deadline := time.Now().Add(testSpawnWait)
	for time.Now().Before(deadline) {
		for _, sched := range s.List() {
			if sched.ID == id && len(sched.Runs) > 0 {
				return sched.Runs[0]
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no run recorded within timeout")
	return ScheduleRun{}
}

// runTestPowerShell runs a PowerShell fixture script. On WSL the launch goes
// through the Windows interop transport, which drops connections under host
// load ("UtilAcceptVsock: accept4 failed") before the script ever runs; that is
// infrastructure, not a verdict on the script, so it is retried and then
// skipped. Timeouts are test failures, including for callers that expect a
// refused launch. reset, when non-nil, clears fixture state before each attempt.
func runTestPowerShell(t *testing.T, ps, script string, reset func()) ([]byte, error) {
	t.Helper()
	return runTestPowerShellContext(t, t.Context(), ps, script, reset)
}

func runTestPowerShellContext(t *testing.T, parent context.Context, ps, script string, reset func()) ([]byte, error) {
	t.Helper()
	var out []byte
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if reset != nil {
			reset()
		}
		out, err = runTestPowerShellCommand(t, parent, ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
		if !wslInteropTransportFailure(out) {
			return out, err
		}
		timer := time.NewTimer(time.Duration(attempt) * time.Second)
		select {
		case <-parent.Done():
			timer.Stop()
			t.Fatalf("PowerShell fixture interrupted during transport retry: %v", parent.Err())
		case <-timer.C:
		}
	}
	t.Skipf("WSL interop transport unavailable after 3 attempts: %s", out)
	return nil, nil
}

// Keep argv policy at the call site: the installer probe does not need the
// fixture scripts' ExecutionPolicy override. Command lifecycle failures must
// stop the test here, before any caller can mistake them for a refused launch
// or unavailable interop and pass or skip the test.
func runTestPowerShellCommand(t *testing.T, parent context.Context, ps string, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	if deadline, ok := t.Deadline(); ok {
		var cancelDeadline context.CancelFunc
		ctx, cancelDeadline = context.WithDeadline(ctx, deadline)
		defer cancelDeadline()
	}
	cmd := exec.CommandContext(ctx, ps, args...)
	applyDetachedNoBreakaway(cmd)
	cmd.Cancel = func() error { return killChildTree(cmd.Process) }
	cmd.WaitDelay = 500 * time.Millisecond
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil || errors.Is(err, exec.ErrWaitDelay) {
		_ = killChildTree(cmd.Process)
	}
	if ctx.Err() != nil {
		t.Fatalf("PowerShell fixture did not finish: %v: %s", ctx.Err(), out)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("PowerShell fixture did not finish: %v: %s", err, out)
	}
	return out, err
}

func codexAuthJSON(t *testing.T, access, refresh, accountID string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      "id-old",
			"access_token":  access,
			"refresh_token": refresh,
			"account_id":    accountID,
		},
		"last_refresh": "2026-06-28T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// credFileJSON builds a credential file body with the given oauth tokens
// plus an extra top-level key (mcpOAuth) we assert is preserved.
func credFileJSON(t *testing.T, access, refresh string, expiresAt int64, sub string) []byte {
	t.Helper()
	top := map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":      access,
			"refreshToken":     refresh,
			"expiresAt":        expiresAt,
			"subscriptionType": sub,
			"rateLimitTier":    "default",
		},
		"mcpOAuth": map[string]any{
			"some-server": map[string]any{"token": "do-not-touch"},
		},
	}
	b, err := json.Marshal(top)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeCredFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// seedRuntimeSecret stores a runtime secret where openRuntimeSecrets reads it:
// the runtime secret store when one is built in (seedRuntimeSecretStore),
// otherwise the daemon's environment.
func seedRuntimeSecret(t *testing.T, dataDir, runtimeID, env, value string) {
	t.Helper()
	if seedRuntimeSecretStore != nil {
		seedRuntimeSecretStore(t, dataDir, runtimeID, env, value)
		return
	}
	t.Setenv(env, value)
}

var seedRuntimeSecretStore func(t *testing.T, dataDir, runtimeID, env, value string)

// testSpawnWait bounds how long a test waits for a scheduled run to be
// recorded. Runs land from a goroutine only after the agent process has
// actually spawned, which on a host saturated by parallel race-detector
// suites has taken well over the old 5s bound (three cross-task flakes of
// TestLegacyManualRunIsExemptFromPause in one week, all green on rerun).
const testSpawnWait = 30 * time.Second

func wslInteropTransportFailure(out []byte) bool {
	s := string(out)
	return strings.Contains(s, "UtilAcceptVsock") || strings.Contains(s, "UtilConnectVsock") || strings.Contains(s, "UtilBindVsockAnyPort")
}
