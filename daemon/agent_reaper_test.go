package main

import (
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// A Setsid agent remains the daemon's child. Boot recovery can replace it
// immediately after spawn, so the per-child supervision goroutine must still
// wait(2) it after the kill instead of leaving a defunct process behind for the
// daemon's lifetime. This harness exercises that exact start-detach-kill shape;
// the reaped channel is the supervision loop's completion signal.
func TestSupervisedAgentChildReapsBootRecoveryReplacement(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("zombie state is asserted through Linux procfs")
	}

	cmd := exec.Command("sleep", "30")
	applyDetached(cmd)
	reaped, err := startSupervisedAgentChild(cmd)
	if err != nil {
		t.Fatalf("start agent child harness: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = hardKillPid(pid)
		select {
		case <-reaped:
		case <-time.After(3 * time.Second):
			t.Errorf("agent child %d was not reaped during cleanup", pid)
		}
	})

	statusPath := fmt.Sprintf("/proc/%d/status", pid)
	status, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("read live agent child status: %v", err)
	}
	if !strings.Contains(string(status), fmt.Sprintf("PPid:\t%d\n", os.Getpid())) {
		t.Fatalf("agent child %d is not parented to harness %d:\n%s", pid, os.Getpid(), status)
	}

	// Mirrors startup's kill-before-replace paths.
	killAgentTree(pid)
	select {
	case <-reaped:
	case <-time.After(3 * time.Second):
		t.Fatalf("supervision loop did not reap killed agent child %d", pid)
	}

	if status, err := os.ReadFile(statusPath); err == nil {
		t.Fatalf("agent child still has a process-table entry after Wait (want no defunct entry):\n%s", status)
	} else if !os.IsNotExist(err) {
		t.Fatalf("check reaped agent child status: %v", err)
	}
}

// waitForAgentSocket must reject bad inputs immediately and, when the socket
// never reappears (no self-healing agent owns it), honor the full wait window
// before giving up — the reattach loop relies on that window to let a live
// agent re-bind its unlinked socket instead of being killed+respawned.
func TestWaitForAgentSocket(t *testing.T) {
	t.Parallel()
	if waitForAgentSocket(0, "/x.sock", time.Second) {
		t.Fatal("pid<=0 must return false")
	}
	if waitForAgentSocket(123, "", time.Second) {
		t.Fatal("empty socket path must return false")
	}
	// A pid that owns nothing + a path that never appears → false, and not
	// before the wait window elapses (it must actually poll, not bail at once).
	start := time.Now()
	if waitForAgentSocket(999999999, "/nonexistent/agent.sock", 200*time.Millisecond) {
		t.Fatal("missing socket must time out to false")
	}
	if el := time.Since(start); el < 150*time.Millisecond {
		t.Fatalf("returned after %v — did not honor the wait window", el)
	}
}

// agentsToReap must return every discovered agent NOT owned by a running pane —
// orphans AND duplicates (a second agent on a running pane's socket has a
// different pid, so it's reaped while the real owner is spared).
func TestAgentsToReap(t *testing.T) {
	t.Parallel()
	agents := []procAgent{
		{pid: 100, socket: "/a.sock"}, // the live owner of pane A — keep
		{pid: 200, socket: "/b.sock"}, // orphan (no running pane) — reap
		{pid: 300, socket: "/a.sock"}, // DUPLICATE on A's socket, different pid — reap
		{pid: 0, socket: "/x.sock"},   // invalid pid — skip
	}
	owned := map[int]bool{100: true}
	got := agentsToReap(agents, owned)
	sort.Ints(got)
	if want := []int{200, 300}; !reflect.DeepEqual(got, want) {
		t.Fatalf("agentsToReap = %v, want %v", got, want)
	}

	// All owned → nothing reaped.
	if got := agentsToReap([]procAgent{{pid: 1, socket: "/a"}}, map[int]bool{1: true}); len(got) != 0 {
		t.Fatalf("owned-only: got %v, want none", got)
	}
}

// parseAgentCmdline must recognize ONLY our `solud agent --socket <path>` helper
// (so the reaper never SIGKILLs an unrelated process) and extract the served path.
func TestParseAgentCmdline(t *testing.T) {
	t.Parallel()
	nul := func(parts ...string) []byte { return []byte(strings.Join(parts, "\x00") + "\x00") }

	ok, sock := parseAgentCmdline(nul(
		"/home/u/.local/share/soluterminal/solud", "agent",
		"--socket", "/home/u/.solu/agents/pane_x.sock", "--cwd", "/repo"))
	if !ok || sock != "/home/u/.solu/agents/pane_x.sock" {
		t.Fatalf("our agent: ok=%v sock=%q", ok, sock)
	}

	if ok, _ := parseAgentCmdline(nul("/usr/bin/bash", "agent", "--socket", "/x")); ok {
		t.Fatal("non-solud argv0 must not match (don't kill unrelated procs)")
	}
	if ok, _ := parseAgentCmdline(nul("/x/solud", "--addr", "0.0.0.0:8787")); ok {
		t.Fatal("the solud daemon itself (no 'agent' subcommand) must not match")
	}
	if ok, _ := parseAgentCmdline(nul("/x/solud", "agent", "--cwd", "/repo")); ok {
		t.Fatal("agent without --socket must not match")
	}
}

// TestSocketUnderDir guards the cross-daemon containment fix: the reaper must
// treat ONLY agents whose socket lives directly in its own agentsDir as
// candidates. A foreign daemon's agent (socket under a DIFFERENT dataDir) must be
// excluded, even though its cmdline parses as an agent — otherwise a second daemon
// (a dev/test instance, or a reinstall) SIGKILLs the live daemon's whole fleet.
func TestSocketUnderDir(t *testing.T) {
	t.Parallel()
	dir := "/home/u/.solu/agents"
	if !socketUnderDir("/home/u/.solu/agents/pane_x.sock", dir) {
		t.Fatal("our own agent socket must match agentsDir")
	}
	if !socketUnderDir("/home/u/.solu/agents/pane_x.sock", dir+"/") {
		t.Fatal("trailing-slash agentsDir must still match (Clean-normalized)")
	}
	if socketUnderDir("/tmp/test-data/agents/pane_y.sock", dir) {
		t.Fatal("a FOREIGN daemon's agent socket must NOT match (the bug that wiped the live fleet)")
	}
	if socketUnderDir("/home/u/.solu/agents/sub/pane_z.sock", dir) {
		t.Fatal("a socket NESTED below agentsDir must not match (not how we name sockets)")
	}
	if socketUnderDir("", dir) || socketUnderDir("/a/b.sock", "") {
		t.Fatal("empty sock or dir must fail safe to no-match")
	}
}

// harnessClientsToReap must spare a live pane's own (descendant) client and
// reap only true orphans: a client whose pane is gone, or whose pane still
// runs but under a REPLACEMENT agent (a predecessor respawn left it behind).
// An out-of-pane session has no SOLU_PANE_ID and is never touched.
func TestHarnessClientsToReap(t *testing.T) {
	t.Parallel()
	clients := []procHarnessClient{
		{pid: 100, paneID: "pane_a"},       // running pane, owned — keep
		{pid: 200, paneID: "pane_a"},       // running pane, NOT owned (orphan) — reap
		{pid: 300, paneID: "pane_gone"},    // pane no longer exists — reap
		{pid: 400, paneID: ""},             // out-of-pane user session — never reap
		{pid: 0, paneID: "pane_a"},         // invalid pid — skip
		{pid: 500, paneID: "pane_unknown"}, // live pane, agent pid unpublished — spare
	}
	running := map[string]bool{"pane_a": true, "pane_unknown": false}
	owned := map[int]bool{100: true}
	got := harnessClientsToReap(clients, running, owned)
	sort.Ints(got)
	if want := []int{200, 300}; !reflect.DeepEqual(got, want) {
		t.Fatalf("harnessClientsToReap = %v, want %v", got, want)
	}
	if got := harnessClientsToReap(nil, running, owned); len(got) != 0 {
		t.Fatalf("no clients: got %v, want none", got)
	}
}

// A second daemon (side-port test build, dev instance) must never judge the
// main daemon's harness clients: their pane ids are unknown to it, which used
// to read as "pane gone" and SIGKILL live DeepSeek sessions at its boot.
func TestHarnessClientBelongsToOwningDataDirOnly(t *testing.T) {
	cases := []struct {
		client  procHarnessClient
		ours    string
		belongs bool
	}{
		{procHarnessClient{pid: 1, paneID: "p", dataDir: "/home/u/.solu"}, "/home/u/.solu", true},
		{procHarnessClient{pid: 1, paneID: "p", dataDir: "/home/u/.solu/"}, "/home/u/.solu", true},
		{procHarnessClient{pid: 1, paneID: "p", dataDir: "/home/u/.solu"}, "/tmp/staging-data", false},
		// Untagged clients (e.g. Pi before launch.mjs tagged it) are nobody's
		// to kill — not even the default-data-dir daemon's.
		{procHarnessClient{pid: 1, paneID: "p", dataDir: ""}, "/home/u/.solu", false},
		{procHarnessClient{pid: 1, paneID: "p", dataDir: ""}, "/tmp/staging-data", false},
		{procHarnessClient{pid: 1, paneID: "p", dataDir: "/tmp/staging-data"}, "", false},
	}
	for i, c := range cases {
		if got := harnessClientBelongsTo(c.client, c.ours); got != c.belongs {
			t.Errorf("case %d: harnessClientBelongsTo(%+v, %q) = %v, want %v", i, c.client, c.ours, got, c.belongs)
		}
	}
}
