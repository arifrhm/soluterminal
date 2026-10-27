package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestCmdLaunchesManagedHarness(t *testing.T) {
	t.Parallel()
	for _, cmd := range [][]string{
		{"node", "/home/u/.solu/harness-launchers/launch.mjs", "deepseek", "--data-dir", "/home/u/.solu"},
		{"node", "/home/u/.solu/harness-launchers/deepseek-terminal.mjs", "/dsh/lib/bin.js", "deepseek-flash"},
		{"node", "/home/u/.solu/harness-launchers/launch.mjs", "pi", "--data-dir", "/home/u/.solu"},
		{"node", "/home/u/.solu/harness-launchers/launch.mjs", "pi-glm", "--data-dir", "/home/u/.solu"},
	} {
		if !cmdLaunchesManagedHarness(cmd) {
			t.Errorf("managed harness argv misclassified: %v", cmd)
		}
	}
	for _, cmd := range [][]string{
		nil,
		{"/bin/bash"},
		{"node", "/home/u/.solu/harness-launchers/launch.mjs", "glm"},
		{"claude", "--resume", "session"},
	} {
		if cmdLaunchesManagedHarness(cmd) {
			t.Errorf("non-harness argv misclassified: %v", cmd)
		}
	}
}

func TestHarnessSessionDirsCoverPiRows(t *testing.T) {
	t.Parallel()
	dirs := harnessSessionDirs("/data")
	got := map[string]string{}
	for _, d := range dirs {
		got[d.provider] = d.dir
	}
	for _, provider := range []string{"deepseek", "pi", "pi-glm"} {
		if got[provider] != filepath.Join("/data", "harness-state", provider, "pane-sessions") {
			t.Errorf("provider %q dir = %q", provider, got[provider])
		}
	}
}

func TestHarnessSessionRecordName(t *testing.T) {
	t.Parallel()
	if got := harnessSessionRecordName("pane_abc-123"); got != "pane_abc-123" {
		t.Fatalf("record name = %q", got)
	}
	if got := harnessSessionRecordName("pane/a b"); got != "pane_a_b" {
		t.Fatalf("record name sanitize = %q", got)
	}
}

func TestDiscoverHarnessSessionFromSidecar(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "harness-state", "deepseek", "pane-sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pane_ds.json"), []byte(`{"provider":"deepseek","sessionId":"sess-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := deferredTestPane("pane_ds", "")
	p.harnessSessionDirs = harnessSessionDirs(dataDir)
	if !p.discoverHarnessSession() {
		t.Fatal("sidecar session was not discovered")
	}
	snap := p.Snapshot()
	if snap.HarnessProvider != "deepseek" || snap.HarnessSessionID != "sess-1" {
		t.Fatalf("adopted identity = %q/%q", snap.HarnessProvider, snap.HarnessSessionID)
	}
	// Idempotent: a second scan has nothing to change.
	if p.discoverHarnessSession() {
		t.Fatal("second discovery reported a change")
	}
	// The adopted identity must survive persistence and the identity heal, or
	// a restart would forget the conversation.
	persisted := persistedFromSnapshot(p.Snapshot())
	healed, _ := healPersistedPane(persisted)
	if healed.HarnessProvider != "deepseek" || healed.HarnessSessionID != "sess-1" {
		t.Fatalf("persisted/healed identity = %q/%q", healed.HarnessProvider, healed.HarnessSessionID)
	}

	// A record whose provider does not match its directory is ignored.
	if err := os.WriteFile(filepath.Join(dir, "pane_bad.json"), []byte(`{"provider":"pi","sessionId":"other"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := deferredTestPane("pane_bad", "")
	bad.harnessSessionDirs = harnessSessionDirs(dataDir)
	if bad.discoverHarnessSession() {
		t.Fatal("mismatched provider record was adopted")
	}
}

func TestResumeSpawnOptsReplaysHarnessLaunch(t *testing.T) {
	t.Parallel()
	managed := PaneSnapshot{
		ID: "pane_ds", Cwd: "/repo", Cmd: []string{"node", "/l/launch.mjs", "deepseek", "--data-dir", "/d"},
		HarnessProvider: "deepseek", HarnessSessionID: "sess-1", ClaudeSessionID: "stale-claude",
	}
	opts := resumeSpawnOpts(managed, 0, 0)
	if !slices.Equal(opts.Cmd, managed.Cmd) {
		t.Fatalf("harness resume cmd = %v, want the original launch %v", opts.Cmd, managed.Cmd)
	}
	if !slices.Contains(opts.Env, "SOLU_HARNESS_RESUME_SESSION=sess-1") {
		t.Fatalf("harness resume env = %v, want the session hint", opts.Env)
	}
	if slices.Contains(opts.Cmd, "--resume") {
		t.Fatalf("harness resume fell back to claude --resume: %v", opts.Cmd)
	}
	if opts.HarnessProvider != "deepseek" || opts.HarnessSessionID != "sess-1" {
		t.Fatalf("harness identity not carried: %+v", opts)
	}

	// A shell pane that ran the harness by hand has no managed launch to
	// replay: restart the shell fresh, and never `claude --resume` a stale id.
	shell := PaneSnapshot{
		ID: "pane_shell", Cwd: "/repo", Cmd: []string{"/bin/bash"},
		HarnessProvider: "deepseek", HarnessSessionID: "sess-2", ClaudeSessionID: "stale-claude",
	}
	shellOpts := resumeSpawnOpts(shell, 0, 0)
	if !slices.Equal(shellOpts.Cmd, []string{"/bin/bash"}) {
		t.Fatalf("shell harness resume cmd = %v, want a fresh shell", shellOpts.Cmd)
	}
	if slices.Contains(shellOpts.Env, "SOLU_HARNESS_RESUME_SESSION=sess-2") {
		t.Fatalf("shell pane should not get a resume hint: %v", shellOpts.Env)
	}
	if slices.Contains(shellOpts.Cmd, "claude") {
		t.Fatalf("shell harness pane was hijacked by a stale Claude id: %v", shellOpts.Cmd)
	}
}

func TestFleetRespawnEligibilityHarnessPaneSkipsStaleClaude(t *testing.T) {
	t.Parallel()
	p := deferredTestPane("pane_ds", "")
	p.Cmd = []string{"node", "/l/launch.mjs", "deepseek", "--data-dir", "/d"}
	p.HarnessProvider = "deepseek"
	p.HarnessSessionID = "sess-1"
	// A stale Claude id whose transcript is NOT on disk: without the harness
	// short-circuit this would block as "Claude transcript is not durably
	// resumable", or worse, resume Claude.
	p.ClaudeSessionID = "stale-claude"
	p.ClaudeTranscriptPath = filepath.Join(t.TempDir(), "missing.jsonl")
	eligibility := fleetRespawnEligibility(p)
	if eligibility.Blocker != "" {
		t.Fatalf("harness pane blocked by a stale Claude id: %q", eligibility.Blocker)
	}
	if eligibility.Busy {
		t.Fatal("idle harness pane reported busy")
	}
}

func TestPersistedPaneConversationIDPrefersHarnessSession(t *testing.T) {
	t.Parallel()
	info := PersistedPane{Agent: "", HarnessProvider: "deepseek", HarnessSessionID: "sess-1", ClaudeSessionID: "stale-claude"}
	if got := persistedPaneConversationID(info); got != "sess-1" {
		t.Fatalf("persisted conversation id = %q, want the harness session", got)
	}
	snap := PaneSnapshot{HarnessProvider: "deepseek", HarnessSessionID: "sess-1", ClaudeSessionID: "stale-claude"}
	if got := paneSnapshotConversationID(snap); got != "sess-1" {
		t.Fatalf("snapshot conversation id = %q, want the harness session", got)
	}
	// Without a harness id the agent conversation wins, unchanged.
	plain := PersistedPane{Agent: agentCodex, CodexThreadID: "thread-1"}
	if got := persistedPaneConversationID(plain); got != "thread-1" {
		t.Fatalf("agent conversation id = %q", got)
	}
}

func TestHarnessProviderFromCmd(t *testing.T) {
	t.Parallel()
	cases := []struct {
		cmd  []string
		want string
	}{
		{[]string{"node", "/l/launch.mjs", "deepseek", "--data-dir", "/d"}, "deepseek"},
		{[]string{"node", "/l/launch.mjs", "pi", "--data-dir", "/d"}, "pi"},
		{[]string{"node", "/l/launch.mjs", "pi-glm", "--data-dir", "/d"}, "pi-glm"},
		{[]string{"node", "/l/launch.mjs", "glm", "--data-dir", "/d"}, ""},
		{[]string{"node", "/l/deepseek-terminal.mjs", "/dsh/bin.js", "deepseek-flash"}, ""},
		{[]string{"/bin/bash"}, ""},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := harnessProviderFromCmd(tc.cmd); got != tc.want {
			t.Errorf("harnessProviderFromCmd(%v) = %q, want %q", tc.cmd, got, tc.want)
		}
	}
}

// A hand-launched harness (the user typed `deepseek` in a plain shell) has no
// managed argv to replay. With the daemon's resolver wired, resume must rebuild
// the managed launch and keep the session hint, never hand the pane to Claude.
func TestResumeSpawnOptsRelanchesHandLaunchedHarness(t *testing.T) {
	// Not parallel: it installs the package-level managed-launch resolver.
	prev := managedHarnessLaunchCmd
	defer func() { managedHarnessLaunchCmd = prev }()
	managed := []string{"node", "/data/harness-launchers/launch.mjs", "deepseek", "--data-dir", "/data"}
	managedHarnessLaunchCmd = func(provider string) ([]string, bool) {
		if provider != "deepseek" {
			return nil, false
		}
		return managed, true
	}
	shell := PaneSnapshot{
		ID: "pane_shell", Cwd: "/repo", Cmd: []string{"/bin/bash"},
		HarnessProvider: "deepseek", HarnessSessionID: "sess-2", ClaudeSessionID: "stale-claude",
	}
	opts := resumeSpawnOpts(shell, 0, 0)
	if !slices.Equal(opts.Cmd, managed) {
		t.Fatalf("hand-launched harness cmd = %v, want the managed launch %v", opts.Cmd, managed)
	}
	if !slices.Contains(opts.Env, "SOLU_HARNESS_RESUME_SESSION=sess-2") {
		t.Fatalf("hand-launched harness resume env = %v, want the session hint", opts.Env)
	}
	if slices.Contains(opts.Cmd, "claude") {
		t.Fatalf("hand-launched harness pane was hijacked by a stale Claude id: %v", opts.Cmd)
	}
}

// The client writes its sidecar asynchronously. A managed launch is
// recognisable from argv alone, so even with no recorded session the resume
// must replay it rather than falling through to the stale Claude id.
func TestResumeSpawnOptsManagedCmdWithoutSidecar(t *testing.T) {
	t.Parallel()
	snap := PaneSnapshot{
		ID: "pane_ds", Cwd: "/repo",
		Cmd:             []string{"node", "/l/launch.mjs", "deepseek", "--data-dir", "/d"},
		ClaudeSessionID: "stale-claude",
	}
	opts := resumeSpawnOpts(snap, 0, 0)
	if !slices.Equal(opts.Cmd, snap.Cmd) {
		t.Fatalf("managed cmd = %v, want %v", opts.Cmd, snap.Cmd)
	}
	if slices.Contains(opts.Cmd, "claude") {
		t.Fatalf("managed pane hijacked by a stale Claude id: %v", opts.Cmd)
	}
	if opts.HarnessProvider != "deepseek" {
		t.Fatalf("provider not recovered from argv: %q", opts.HarnessProvider)
	}
}

func TestFleetRespawnEligibilityManagedCmdNeedsNoSidecar(t *testing.T) {
	t.Parallel()
	p := deferredTestPane("pane_ds", "")
	p.Cmd = []string{"node", "/l/launch.mjs", "deepseek", "--data-dir", "/d"}
	p.ClaudeSessionID = "stale-claude"
	p.ClaudeTranscriptPath = filepath.Join(t.TempDir(), "missing.jsonl")
	eligibility := fleetRespawnEligibility(p)
	if eligibility.Blocker != "" {
		t.Fatalf("managed launch blocked before its sidecar landed: %q", eligibility.Blocker)
	}
}

func TestReapDetachedTreeKillsLivePid(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	reapDetachedTree([]int{pid})
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		<-waited
		t.Fatalf("pid %d survived reapDetachedTree", pid)
	}
}
