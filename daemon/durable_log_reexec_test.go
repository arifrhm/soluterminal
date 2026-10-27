//go:build !windows

package main

// The durable log has to survive the one place in boot where the daemon
// replaces its own process image: maybeReexecCleanEnv. main.go attaches the log
// BEFORE that re-exec on purpose, so the decision to re-exec is itself on the
// record — which only pays off if the process on the other side of the exec
// re-attaches and re-arms rather than inheriting a stale fd.

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDurableLogSurvivesCleanEnvReexec drives attachDurableLog ->
// maybeReexecCleanEnv -> attachDurableLog in one real process lineage.
//
// syscall.Exec keeps the pid but throws away the address space and (because Go
// opens files O_CLOEXEC) the fd table, so everything the first generation armed
// is gone. What must be true afterwards:
//
//   - the successor appends to the SAME solud.log rather than truncating it, so
//     the pre-exec records are still there to explain why the exec happened;
//   - it writes its own boot banner, so the file reads as two honest runs and
//     not one confusing one;
//   - and its crash output is armed on its own handle, so a panic AFTER the
//     re-exec still lands on disk. That last one is the whole point: an
//     unrecovered panic in a re-exec'd daemon was invisible before this change,
//     and would be invisible again if attachDurableLog ever moved below the
//     re-exec in main().
func TestDurableLogSurvivesCleanEnvReexec(t *testing.T) {
	if os.Getenv("SOLU_DURABLE_REEXEC_CHILD") == "1" {
		durableLogReexecChild()
		return
	}

	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestDurableLogSurvivesCleanEnvReexec", "-test.v")
	// The sentinel must start ABSENT, not empty: maybeReexecCleanEnv appends
	// its own SOLU_ENV_CLEANED=1 for the successor, and os.Getenv answers with
	// the first match, so an empty one carried in from here would mask it and
	// the successor would never reach the branch this test asserts on.
	env := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, envCleanedSentinel+"=") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env,
		"SOLU_DURABLE_REEXEC_CHILD=1",
		"SOLU_DURABLE_LOG_DIR="+dir,
		// maybeReexecCleanEnv only fires for a daemon launched from inside a
		// Claude Code session; this is the marker it looks for.
		"CLAUDECODE=1",
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("child was supposed to die of its post-re-exec panic; it exited 0:\n%s", out)
	}

	body, readErr := os.ReadFile(filepath.Join(dir, durableLogName))
	if readErr != nil {
		t.Fatalf("read durable log after re-exec: %v (child output:\n%s)", readErr, out)
	}
	got := string(body)

	if n := strings.Count(got, "solud boot"); n != 2 {
		t.Errorf("durable log holds %d boot banners, want 2 (one per process image); got:\n%s", n, got)
	}

	first := strings.Index(got, "before-clean-env-reexec")
	second := strings.Index(got, "after-clean-env-reexec")
	switch {
	case first < 0:
		t.Errorf("the pre-exec record is gone — the successor truncated the log instead of appending; got:\n%s", got)
	case second < 0:
		t.Errorf("the successor never logged; got:\n%s", got)
	case first > second:
		t.Errorf("records are out of order: the successor's record precedes its predecessor's; got:\n%s", got)
	}

	for _, needle := range []string{"panic:", "dying-words-after-reexec", "goroutine "} {
		if !strings.Contains(got, needle) {
			t.Errorf("durable log is missing %q — the successor's crash output was never armed; got:\n%s", needle, got)
		}
	}
	if idx := strings.Index(got, "panic:"); idx >= 0 && second >= 0 && idx < second {
		t.Errorf("the traceback precedes the successor's own record, so it came from the wrong generation; got:\n%s", got)
	}
}

// durableLogReexecChild is one process image of the lineage: attach, then
// either re-exec (first generation) or die loudly (second).
func durableLogReexecChild() {
	attachDurableLog()
	if durableLog == nil {
		fmt.Fprintln(os.Stderr, "durable log never attached")
		os.Exit(3)
	}

	if os.Getenv(envCleanedSentinel) == "1" {
		slog.Info("after-clean-env-reexec")
		panic("dying-words-after-reexec")
	}

	slog.Info("before-clean-env-reexec")
	maybeReexecCleanEnv() // replaces this process image; only returns on failure
	fmt.Fprintln(os.Stderr, "maybeReexecCleanEnv returned without exec'ing")
	os.Exit(3)
}
