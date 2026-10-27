package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func waitForMemGuard(t *testing.T, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(message)
}

func diagnosticDirs(t *testing.T, dataDir, role string) []string {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(dataDir, "diagnostics", "oom-*-"+role+"*"))
	if err != nil {
		t.Fatal(err)
	}
	return dirs
}

func TestMemGuardDaemonTriggerCapturesAndRestarts(t *testing.T) {
	dataDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var restarts atomic.Int32
	restarted := make(chan struct{}, 1)
	startMemGuard(ctx, memGuardConfig{
		role:     "daemon",
		dataDir:  dataDir,
		softMB:   1024,
		interval: 10 * time.Millisecond,
		memFn:    func() uint64 { return 2048 },
		restartFn: func() error {
			restarts.Add(1)
			select {
			case restarted <- struct{}{}:
			default:
			}
			return nil
		},
	})
	select {
	case <-restarted:
	case <-time.After(10 * time.Second):
		t.Fatal("daemon guard did not trigger")
	}
	time.Sleep(40 * time.Millisecond)
	if got := restarts.Load(); got != 1 {
		t.Fatalf("restartFn calls = %d, want 1", got)
	}
	dirs := diagnosticDirs(t, dataDir, "daemon")
	if len(dirs) != 1 {
		t.Fatalf("daemon diagnostics dirs = %v, want exactly one", dirs)
	}
	for _, name := range []string{"heap.pprof", "heap.txt", "goroutine.txt", "memstats.txt"} {
		info, err := os.Stat(filepath.Join(dirs[0], name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if info.Size() == 0 {
			t.Fatalf("%s is empty", name)
		}
	}
}

func TestMemGuardDaemonRetriesWhileRestartDoesNotExit(t *testing.T) {
	dataDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var restarts atomic.Int32
	startMemGuard(ctx, memGuardConfig{
		role:     "daemon",
		dataDir:  dataDir,
		softMB:   1024,
		interval: 10 * time.Millisecond,
		memFn:    func() uint64 { return 2048 },
		restartFn: func() error {
			if restarts.Add(1) == 1 {
				return errors.New("successor spawn failed")
			}
			return nil
		},
	})
	waitForMemGuard(t, func() bool { return restarts.Load() >= 2 }, "restartFn was not retried after the first attempt returned")
	if got := restarts.Load(); got < 2 {
		t.Fatalf("restartFn calls = %d, want a retry after the first attempt returned without exiting", got)
	}
	if got := len(diagnosticDirs(t, dataDir, "daemon")); got < 2 {
		t.Fatalf("daemon diagnostics dirs = %d, want a fresh capture for the retry", got)
	}
}

func TestMemGuardRotationKeepsLastThree(t *testing.T) {
	dataDir := t.TempDir()
	diagnostics := filepath.Join(dataDir, "diagnostics")
	if err := os.MkdirAll(diagnostics, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := []string{
		"oom-20200101-000001-daemon1",
		"oom-20200101-000002-daemon1",
		"oom-20200101-000003-daemon1",
		"oom-20200101-000004-daemon1",
	}
	for _, name := range fake {
		if err := os.Mkdir(filepath.Join(diagnostics, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	agentDir := filepath.Join(diagnostics, "oom-20200101-000001-agent1")
	if err := os.Mkdir(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := captureOOMDiagnostics(dataDir, "daemon"); err != nil {
		t.Fatal(err)
	}
	dirs := diagnosticDirs(t, dataDir, "daemon")
	if len(dirs) != 3 {
		t.Fatalf("daemon diagnostics dirs = %v, want newest three", dirs)
	}
	if _, err := os.Stat(filepath.Join(diagnostics, fake[0])); !os.IsNotExist(err) {
		t.Fatalf("oldest daemon diagnostics was not pruned: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(diagnostics, fake[1])); !os.IsNotExist(err) {
		t.Fatalf("second-oldest daemon diagnostics was not pruned: err=%v", err)
	}
	if _, err := os.Stat(agentDir); err != nil {
		t.Fatalf("agent diagnostics was pruned by daemon rotation: %v", err)
	}
}

func TestMemGuardDisabledByEnv(t *testing.T) {
	t.Setenv("SOLU_MEM_GUARD_MB", "0")
	cfg := daemonMemGuardConfig(t.TempDir())
	var samples atomic.Int32
	cfg.interval = 5 * time.Millisecond
	cfg.memFn = func() uint64 {
		samples.Add(1)
		return ^uint64(0)
	}
	startMemGuard(context.Background(), cfg)
	time.Sleep(30 * time.Millisecond)
	if got := samples.Load(); got != 0 {
		t.Fatalf("disabled guard sampled memory %d times", got)
	}
}

func TestMemGuardAgentSoftProfilesHardExits(t *testing.T) {
	dataDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var measured atomic.Uint64
	measured.Store(1500)
	exited := make(chan int, 1)
	startMemGuard(ctx, memGuardConfig{
		role:     "agent",
		dataDir:  dataDir,
		softMB:   1024,
		hardMB:   2048,
		interval: 10 * time.Millisecond,
		memFn:    measured.Load,
		exitFn: func(code int) {
			exited <- code
		},
	})
	waitForMemGuard(t, func() bool {
		return len(diagnosticDirs(t, dataDir, "agent")) == 1
	}, "agent soft limit did not capture diagnostics")
	select {
	case code := <-exited:
		t.Fatalf("agent exited at soft limit with code %d", code)
	default:
	}
	time.Sleep(50 * time.Millisecond)
	if got := len(diagnosticDirs(t, dataDir, "agent")); got != 1 {
		t.Fatalf("agent soft cooldown produced %d diagnostic dirs, want 1", got)
	}

	measured.Store(4096)
	select {
	case code := <-exited:
		if code != 1 {
			t.Fatalf("agent hard-limit exit code = %d, want 1", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent hard limit did not call exitFn")
	}
}

func TestMemGuardAgentHardLimitWorksWithSoftDisabled(t *testing.T) {
	dataDir := t.TempDir()
	measuredMB := uint64(2047)
	exitCalls := 0
	exitCode := 0
	guard := newMemGuard(memGuardConfig{
		role:    "agent",
		dataDir: dataDir,
		softMB:  0,
		hardMB:  2048,
		memFn:   func() uint64 { return measuredMB },
		exitFn: func(code int) {
			exitCalls++
			exitCode = code
			dirs := diagnosticDirs(t, dataDir, "agent")
			if len(dirs) != 1 {
				t.Fatalf("agent diagnostics dirs at exit = %v, want exactly one", dirs)
			}
			for _, name := range []string{"heap.pprof", "heap.txt", "goroutine.txt", "memstats.txt"} {
				info, err := os.Stat(filepath.Join(dirs[0], name))
				if err != nil {
					t.Fatalf("stat %s at exit: %v", name, err)
				}
				if info.Size() == 0 {
					t.Fatalf("%s is empty at exit", name)
				}
			}
		},
	})
	if guard == nil {
		t.Fatal("hard-only agent guard was disabled along with the soft profile threshold")
	}

	// docs/audits/2026-08-26-wft_a6e62675-recovery.md reproduces the
	// cross-task failure: the old deadline measured ticker scheduling and
	// synchronous profile generation under load rather than guard correctness.
	now := time.Date(2026, time.August, 26, 0, 0, 0, 0, time.UTC)
	guard.sample(now)
	if exitCalls != 0 {
		t.Fatalf("below-limit exit calls = %d, want 0", exitCalls)
	}
	if dirs := diagnosticDirs(t, dataDir, "agent"); len(dirs) != 0 {
		t.Fatalf("below-limit diagnostics dirs = %v, want none", dirs)
	}

	measuredMB = 2048
	guard.sample(now.Add(time.Second))
	if exitCalls != 1 {
		t.Fatalf("hard-limit exit calls = %d, want 1", exitCalls)
	}
	if exitCode != 1 {
		t.Fatalf("agent hard-limit exit code = %d, want 1", exitCode)
	}
	dirs := diagnosticDirs(t, dataDir, "agent")
	if len(dirs) != 1 {
		t.Fatalf("agent diagnostics dirs = %v, want exactly one", dirs)
	}

	measuredMB = 4096
	guard.sample(now.Add(2 * time.Second))
	if exitCalls != 1 {
		t.Fatalf("repeated hard-limit exit calls = %d, want 1", exitCalls)
	}
	if got := diagnosticDirs(t, dataDir, "agent"); len(got) != 1 || got[0] != dirs[0] {
		t.Fatalf("repeated hard-limit diagnostics dirs = %v, want unchanged %v", got, dirs)
	}
}

func TestCaptureOOMDiagnosticsDoesNotCollideWithinSecond(t *testing.T) {
	dataDir := t.TempDir()
	// The original directory name had one-second resolution. Start near the
	// beginning of a wall-clock second so two quick captures deterministically
	// exercise that collision instead of occasionally straddling a boundary.
	for time.Now().Nanosecond() > 100_000_000 {
		time.Sleep(5 * time.Millisecond)
	}
	first, err := captureOOMDiagnostics(dataDir, "daemon")
	if err != nil {
		t.Fatal(err)
	}
	second, err := captureOOMDiagnostics(dataDir, "daemon")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("same-second diagnostics collided at %s", first)
	}
	if got := len(diagnosticDirs(t, dataDir, "daemon")); got != 2 {
		t.Fatalf("same-second diagnostics dirs = %d, want 2", got)
	}
}

func TestProcessMemoryUsageMBUsesLargerCommitOrWorkingSet(t *testing.T) {
	const mib = uintptr(1024 * 1024)
	if got := processMemoryUsageMB(68*mib, 25*mib); got != 68 {
		t.Fatalf("private-usage dominant result = %dMB, want 68", got)
	}
	if got := processMemoryUsageMB(25*mib, 32*mib); got != 32 {
		t.Fatalf("working-set dominant result = %dMB, want 32", got)
	}
}

func TestCaptureOOMDiagnosticsReportsUnusableDataDir(t *testing.T) {
	dataFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(dataFile, []byte("occupied"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir, err := captureOOMDiagnostics(dataFile, "daemon")
	if err == nil {
		t.Fatalf("capture into file-backed data dir unexpectedly succeeded at %s", dir)
	}
	if !strings.Contains(dir, filepath.Join("not-a-directory", "diagnostics")) {
		t.Fatalf("reported diagnostics dir = %q, want path under requested data dir", dir)
	}
}

func TestMemGuardEnvParsing(t *testing.T) {
	t.Setenv("SOLU_MEM_GUARD_MB", "")
	t.Setenv("SOLU_MEM_GUARD_AGENT_MB", "")
	t.Setenv("SOLU_MEM_GUARD_AGENT_HARD_MB", "")
	if got := daemonMemGuardConfig("x").softMB; got != 6144 {
		t.Fatalf("daemon default = %dMB, want 6144", got)
	}
	agent := agentMemGuardConfig("x")
	if agent.softMB != 2048 || agent.hardMB != 4096 {
		t.Fatalf("agent defaults = soft %dMB hard %dMB, want 2048/4096", agent.softMB, agent.hardMB)
	}

	t.Setenv("SOLU_MEM_GUARD_MB", "8192")
	t.Setenv("SOLU_MEM_GUARD_AGENT_MB", "3072")
	t.Setenv("SOLU_MEM_GUARD_AGENT_HARD_MB", "7168")
	if got := daemonMemGuardConfig("x").softMB; got != 8192 {
		t.Fatalf("explicit daemon threshold = %dMB, want 8192", got)
	}
	agent = agentMemGuardConfig("x")
	if agent.softMB != 3072 || agent.hardMB != 7168 {
		t.Fatalf("explicit agent thresholds = %d/%dMB, want 3072/7168", agent.softMB, agent.hardMB)
	}

	t.Setenv("SOLU_MEM_GUARD_MB", "1")
	if got := daemonMemGuardConfig("x").softMB; got != memGuardMinMB {
		t.Fatalf("threshold floor = %dMB, want %d", got, memGuardMinMB)
	}
	for _, off := range []string{"0", "false", "off", "no"} {
		t.Setenv("SOLU_MEM_GUARD_MB", off)
		if got := daemonMemGuardConfig("x").softMB; got != 0 {
			t.Fatalf("%q parsed as %dMB, want disabled", off, got)
		}
	}

	t.Setenv("SOLU_MEM_GUARD_AGENT_MB", "0")
	t.Setenv("SOLU_MEM_GUARD_AGENT_HARD_MB", "8192")
	agent = agentMemGuardConfig("x")
	if agent.softMB != 0 || agent.hardMB != 8192 {
		t.Fatalf("hard-only agent thresholds = %d/%dMB, want 0/8192", agent.softMB, agent.hardMB)
	}
}

func TestTopHeapConsumersNonEmpty(t *testing.T) {
	ballast := make([]byte, 2*1024*1024)
	for i := range ballast {
		ballast[i] = byte(i)
	}
	consumers := topHeapConsumers(5)
	runtime.KeepAlive(ballast)
	if len(consumers) == 0 {
		t.Fatal("topHeapConsumers returned no entries")
	}
	if !strings.Contains(consumers[0], " inuse=") || !strings.HasSuffix(consumers[0], "MB") {
		t.Fatalf("top consumer format = %q, want '<function> inuse=<MB>MB'", consumers[0])
	}
}
