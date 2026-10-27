package main

import (
	"slices"
	"testing"
)

const reconnectTestThread = "01a09bd6-ef88-7d61-8155-e75654494511"

func TestCodexReconnectArmLifecycle(t *testing.T) {
	var arms codexReconnectArms
	stop, started := arms.arm("p")
	if !started {
		t.Fatal("first arm must start a waiter")
	}
	if _, again := arms.arm("p"); again {
		t.Fatal("a second arm must reuse the running waiter")
	}
	if armed, _ := arms.status("p"); !armed {
		t.Fatal("armed state not reported")
	}
	arms.finish("p", stop, "login required")
	if armed, failure := arms.status("p"); armed || failure != "login required" {
		t.Fatalf("finish => armed=%v failure=%q", armed, failure)
	}
	// Re-arming clears the previous failure; disarming stops the waiter.
	stop, _ = arms.arm("p")
	if _, failure := arms.status("p"); failure != "" {
		t.Fatal("re-arm kept a stale failure")
	}
	arms.disarm("p")
	select {
	case <-stop:
	default:
		t.Fatal("disarm did not stop the waiter")
	}
	arms.finish("p", stop, "late failure from a cancelled waiter")
	if armed, failure := arms.status("p"); armed || failure != "" {
		t.Fatal("a cancelled waiter must not report")
	}
}

// isolateCodexHome keeps eligibility checks off the developer's real
// ~/.codex sessions, which otherwise decide whether a test thread resumes.
func isolateCodexHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", "")
}

func TestCodexReconnectFailuresArePrunedWithTheirPane(t *testing.T) {
	var arms codexReconnectArms
	stop, _ := arms.arm("gone")
	arms.finish("gone", stop, "login required")
	stop, _ = arms.arm("kept")
	arms.finish("kept", stop, "login required")
	arms.prune(func(id string) bool { return id == "kept" })
	if _, failure := arms.status("gone"); failure != "" {
		t.Fatal("closed pane's failure retained")
	}
	if _, failure := arms.status("kept"); failure == "" {
		t.Fatal("live pane's failure dropped")
	}
}

func TestCodexReconnectBlockerClassification(t *testing.T) {
	for blocker, settles := range map[string]bool{
		"Codex thread identity is not currently verified":      true,
		"managed harness session identity is not yet recorded": true,
		"Codex rollout is not durably resumable":               true,
		"no durable codex conversation to resume":              false,
		"pane agent is not running":                            false,
	} {
		got := codexReconnectBlocked(blocker)
		if got.retryable != settles || got.settling != settles {
			t.Fatalf("%q => retryable=%v settling=%v", blocker, got.retryable, got.settling)
		}
	}
}

func TestCodexReconnectFailureClearsOnceConnected(t *testing.T) {
	a := &apiServer{mgr: NewManager(t.TempDir())}
	stop, _ := a.codexReconnects.arm("pane_ok")
	a.codexReconnects.finish("pane_ok", stop, "login required")
	a.decorateCodexPaneAuth(&codexPaneAuthInfo{State: "live"}, nil, PaneSnapshot{ID: "pane_ok"})
	if _, failure := a.codexReconnects.status("pane_ok"); failure != "" {
		t.Fatal("a stale failure outlived the connection")
	}
}

func TestCodexReconnectChoice(t *testing.T) {
	snap := PaneSnapshot{Cmd: []string{"codex"}, Cwd: "/recorded", CodexThreadID: reconnectTestThread}
	resume := []string{"codex", "resume", reconnectTestThread}
	for name, tc := range map[string]struct {
		fg                codexForeground
		running, unproven bool
		cmd               []string
		cwd               string
		shell, ok         bool
	}{
		"not running replays the pane":      {cmd: resume, cwd: "/recorded", ok: true},
		"running moves to its cwd":          {fg: codexForeground{cwd: "/actual"}, running: true, cmd: resume, cwd: "/actual", ok: true},
		"under a shell uses the process":    {fg: codexForeground{cwd: "/actual", underShell: true, args: []string{"codex", "--yolo"}}, running: true, cmd: append(append([]string{}, resume...), "--yolo"), cwd: "/actual", shell: true, ok: true},
		"under a shell, unprovable refuses": {fg: codexForeground{cwd: "/actual", underShell: true}, running: true, shell: true},
		"unidentified Codex refuses":        {unproven: true},
	} {
		cmd, cwd, shell, ok := codexReconnectChoice(snap, tc.fg, tc.running, tc.unproven)
		if ok != tc.ok || shell != tc.shell || cwd != tc.cwd || !slices.Equal(cmd, tc.cmd) {
			t.Fatalf("%s => cmd=%q cwd=%q shell=%v ok=%v", name, cmd, cwd, shell, ok)
		}
	}
}
