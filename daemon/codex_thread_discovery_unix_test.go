//go:build !windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	testCodexThreadA = "019f543c-3f11-7701-bc5e-6a73a50a9083"
	testCodexThreadB = "019f543c-aaaa-7701-bc5e-6a73a50a9084"
)

func TestDiscoverCodexRemotePairIdentity(t *testing.T) {
	for _, mode := range []string{"resume", "fresh", "opening", "server-only", "other-socket", "other-pane", "other-thread", "other-foreground", "extra-tui", "unreadable-fds", "relay", "relay-other-owner"} {
		for _, liveBridge := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/live_bridge=%t", mode, liveBridge), func(t *testing.T) {
				root := t.TempDir()
				procRoot, cwd, home := filepath.Join(root, "proc"), filepath.Join(root, "work"), filepath.Join(root, "home")
				if err := os.MkdirAll(cwd, 0700); err != nil {
					t.Fatal(err)
				}
				server := writeFakeCodexProcess(t, procRoot, "pane-a", 101, 100, []string{"codex", "app-server", "--listen", "unix:///tmp/private/app.sock"}, cwd, home)
				args := []string{"codex", "resume", testCodexThreadA, "--remote", "unix:///tmp/private/app.sock"}
				if mode == "fresh" {
					args = []string{"codex", "--remote", "unix:///tmp/private/app.sock"}
				}
				if mode == "other-socket" {
					args[len(args)-1] = "unix:///tmp/another/app.sock"
				}
				if mode == "other-thread" {
					args[2] = testCodexThreadB
				}
				pane := "pane-a"
				if mode == "other-pane" {
					pane = "pane-b"
				}
				pids := []int{101}
				if mode != "server-only" {
					group := 100
					if mode == "other-foreground" {
						group = 200
					}
					writeFakeCodexProcess(t, procRoot, pane, 102, group, args, cwd, home)
					pids = append(pids, 102)
				}
				if mode == "relay" || mode == "relay-other-owner" {
					relayDir := "/tmp/solu-codex-live-fixture"
					for _, pid := range []int{101, 102} {
						dir := filepath.Join(procRoot, strconv.Itoa(pid))
						env, err := os.ReadFile(filepath.Join(dir, "environ"))
						if err != nil {
							t.Fatal(err)
						}
						owner := relayDir
						if mode == "relay-other-owner" && pid == 102 {
							owner += "-wrong"
						}
						if err := os.WriteFile(filepath.Join(dir, "environ"), append(env, []byte("SOLU_CODEX_RELAY_DIR="+owner+"\x00")...), 0600); err != nil {
							t.Fatal(err)
						}
						argv := []string{"codex", "app-server", "--listen", "unix://" + relayDir + "/app.sock"}
						if pid == 102 {
							argv = []string{"codex", "resume", testCodexThreadA, "--remote", "unix://" + relayDir + "/terminal.sock"}
						}
						if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
				if mode != "opening" && mode != "server-only" {
					rollout := writeFakeRollout(t, home, testCodexThreadA, cwd, "cli", nil)
					if liveBridge {
						rewriteFakeRollout(t, rollout, func(p map[string]any) {
							p["originator"], p["source"] = "soluterminal_live_auth", "vscode"
						})
					}
					linkFakeFD(t, server, 52, rollout)
				}
				if mode == "extra-tui" {
					writeFakeCodexProcess(t, procRoot, "pane-a", 103, 100, args, cwd, home)
					pids = append(pids, 103)
				}
				if mode == "unreadable-fds" {
					if err := os.Rename(filepath.Join(server, "fd"), filepath.Join(server, "fd-hidden")); err != nil {
						t.Fatal(err)
					}
				}
				probe := discoverCodexIdentityAt(procRoot, "pane-a", pids)
				switch mode {
				case "resume", "fresh", "relay":
					if probe.threadID != testCodexThreadA || !probe.active || probe.blocked {
						t.Fatalf("paired identity not recovered: %+v", probe)
					}
				case "opening":
					if probe.resumeThreadID != testCodexThreadA || !probe.active || probe.blocked || probe.fresh {
						t.Fatalf("resume continuity lost during startup: %+v", probe)
					}
				default:
					if !probe.active || !probe.blocked || probe.fresh || probe.threadID != "" || probe.resumeThreadID != "" {
						t.Fatalf("ambiguous server pair trusted: %+v", probe)
					}
				}
			})
		}
	}
}

// The live-auth bridge initializes app-server with this clientInfo.name;
// Codex writes it as the rollout originator (not codex-tui or soluterminal).
func TestDiscoverCodexLiveBridgeRootIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
		known  bool
	}{
		{name: "live bridge", known: true},
		{name: "unknown origin", mutate: func(p map[string]any) { p["originator"] = "unknown" }},
		{name: "origin prefix spoof", mutate: func(p map[string]any) { p["originator"] = "soluterminal_live_auth_untrusted" }},
		{name: "wrong source", mutate: func(p map[string]any) { p["source"] = "cli" }},
		{name: "missing source", mutate: func(p map[string]any) { delete(p, "source") }},
		{name: "subagent", mutate: func(p map[string]any) { p["thread_source"] = "subagent" }},
		{name: "missing thread source", mutate: func(p map[string]any) { delete(p, "thread_source") }},
		{name: "parent thread", mutate: func(p map[string]any) { p["parent_thread_id"] = testCodexThreadB }},
		{name: "mismatched session", mutate: func(p map[string]any) { p["session_id"] = testCodexThreadB }},
		{name: "wrong cwd", mutate: func(p map[string]any) { p["cwd"] = filepath.Dir(p["cwd"].(string)) }},
	} {
		for _, paired := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/paired=%t", tc.name, paired), func(t *testing.T) {
				root := t.TempDir()
				procRoot, cwd, home := filepath.Join(root, "proc"), filepath.Join(root, "work"), filepath.Join(root, "home")
				if err := os.MkdirAll(cwd, 0700); err != nil {
					t.Fatal(err)
				}
				args := []string{"codex"}
				if paired {
					args = []string{"codex", "app-server", "--listen", "unix:///tmp/private/app.sock"}
				}
				owner := writeFakeCodexProcess(t, procRoot, "pane-a", 101, 100, args, cwd, home)
				pids := []int{101}
				if paired {
					writeFakeCodexProcess(t, procRoot, "pane-a", 102, 100, []string{"codex", "--remote", "unix:///tmp/private/app.sock"}, cwd, home)
					pids = append(pids, 102)
				}
				rollout := writeFakeRollout(t, home, testCodexThreadA, cwd, "vscode", nil)
				rewriteFakeRollout(t, rollout, func(p map[string]any) {
					p["originator"] = "soluterminal_live_auth"
					if tc.mutate != nil {
						tc.mutate(p)
					}
				})
				linkFakeFD(t, owner, 52, rollout)
				probe := discoverCodexIdentityAt(procRoot, "pane-a", pids)
				if !tc.known {
					if !probe.active || !probe.blocked || probe.fresh || probe.threadID != "" || probe.resumeThreadID != "" {
						t.Fatalf("unsafe bridge identity accepted: %+v", probe)
					}
					return
				}
				if !probe.active || probe.blocked || probe.threadID != testCodexThreadA {
					t.Fatalf("live bridge identity not recovered: %+v", probe)
				}
				pane := &Pane{Agent: agentCodex, CodexThreadID: testCodexThreadA}
				pane.codexIdentityBlocked.Store(true)
				adoptDiscoveredCodexThread(pane, probe.threadID)
				if pane.codexIdentityBlocked.Load() || pane.Snapshot().CodexThreadID != testCodexThreadA {
					t.Fatal("verified bridge identity did not clear block and preserve session")
				}
			})
		}
	}
}

func writeFakeCodexProcess(t *testing.T, procRoot, paneID string, pid, pgrp int, argv []string, cwd, home string) string {
	t.Helper()
	pidDir := filepath.Join(procRoot, strconv.Itoa(pid))
	if err := os.MkdirAll(filepath.Join(pidDir, "fd"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(pidDir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("comm", []byte("codex\n"))
	// Fields after comm: state, ppid, pgrp, session, tty_nr, tpgid.
	mustWrite("stat", []byte(fmt.Sprintf("%d (codex) S 1 %d %d 34818 %d 0 0 0\n", pid, pgrp, pgrp, pgrp)))
	mustWrite("cmdline", []byte(joinNUL(argv)))
	mustWrite("environ", []byte("SOLU_PANE_ID="+paneID+"\x00HOME="+home+"\x00"))
	if err := os.Symlink(cwd, filepath.Join(pidDir, "cwd")); err != nil {
		t.Fatal(err)
	}
	return pidDir
}

func joinNUL(values []string) string {
	var out string
	for _, value := range values {
		out += value + "\x00"
	}
	return out
}

func writeFakeRollout(t *testing.T, home, id, cwd string, source any, parent *string) string {
	t.Helper()
	dir := filepath.Join(home, ".codex", "sessions", "2026", "07", "12")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-07-12T10-00-00-"+id+".jsonl")
	line, err := json.Marshal(map[string]any{
		"type": "session_meta",
		"payload": map[string]any{
			"id":               id,
			"session_id":       id,
			"cwd":              cwd,
			"originator":       "codex-tui",
			"source":           source,
			"parent_thread_id": parent,
			"thread_source":    map[bool]string{true: "subagent", false: "user"}[parent != nil],
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func linkFakeFD(t *testing.T, pidDir string, fd int, target string) {
	t.Helper()
	if err := os.Symlink(target, filepath.Join(pidDir, "fd", strconv.Itoa(fd))); err != nil {
		t.Fatal(err)
	}
}

func rewriteFakeRollout(t *testing.T, path string, mutate func(map[string]any)) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(b, &record); err != nil {
		t.Fatal(err)
	}
	payload, ok := record["payload"].(map[string]any)
	if !ok {
		t.Fatal("rollout payload is not an object")
	}
	mutate(payload)
	b, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverCodexThreadFromResumeArgv(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	procRoot := filepath.Join(root, "proc")
	cwd := filepath.Join(root, "work")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	pidDir := writeFakeCodexProcess(t, procRoot, "pane-a", 101, 100, []string{"/bin/codex", "resume", testCodexThreadA}, cwd, home)
	linkFakeFD(t, pidDir, 52, writeFakeRollout(t, home, testCodexThreadA, cwd, "cli", nil))

	if got, active := discoverCodexThreadIDAt(procRoot, "pane-a", []int{101}); got != testCodexThreadA || !active {
		t.Fatalf("discover resume thread = %q, want %q", got, testCodexThreadA)
	}
}

func TestDiscoverCodexResumeFromDifferentDirectory(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		argv   []string
		mutate func(map[string]any)
		known  bool
	}{
		{name: "exact resume", argv: []string{"codex", "resume", testCodexThreadA}, known: true},
		{name: "different thread", argv: []string{"codex", "resume", testCodexThreadB}},
		{name: "fresh process", argv: []string{"codex"}},
		{name: "last is not an exact identity", argv: []string{"codex", "resume", "--last"}},
		{name: "session id mismatch", argv: []string{"codex", "resume", testCodexThreadA}, mutate: func(p map[string]any) { p["session_id"] = testCodexThreadB }},
		{name: "subagent", argv: []string{"codex", "resume", testCodexThreadA}, mutate: func(p map[string]any) { p["parent_thread_id"] = testCodexThreadB }},
		{name: "untrusted origin", argv: []string{"codex", "resume", testCodexThreadA}, mutate: func(p map[string]any) { p["originator"] = "unknown" }},
		{name: "missing project directory", argv: []string{"codex", "resume", testCodexThreadA}, mutate: func(p map[string]any) { delete(p, "cwd") }},
		{name: "relative project directory", argv: []string{"codex", "resume", testCodexThreadA}, mutate: func(p map[string]any) { p["cwd"] = "project" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			procRoot, home := filepath.Join(root, "proc"), filepath.Join(root, "home")
			launchCwd, projectCwd := home, filepath.Join(home, "projects", "video")
			pidDir := writeFakeCodexProcess(t, procRoot, "pane-a", 101, 100, tc.argv, launchCwd, home)
			rollout := writeFakeRollout(t, home, testCodexThreadA, projectCwd, "cli", nil)
			if tc.mutate != nil {
				rewriteFakeRollout(t, rollout, tc.mutate)
			}
			linkFakeFD(t, pidDir, 52, rollout)
			probe := discoverCodexIdentityAt(procRoot, "pane-a", []int{101})
			if tc.known {
				if probe.threadID != testCodexThreadA || !probe.active || probe.blocked {
					t.Fatalf("exact resumed conversation not recognized: %+v", probe)
				}
				pane := &Pane{Agent: agentCodex, CodexThreadID: testCodexThreadA}
				pane.codexIdentityBlocked.Store(true)
				adoptDiscoveredCodexThread(pane, probe.threadID)
				if pane.codexIdentityBlocked.Load() || pane.Snapshot().CodexThreadID != testCodexThreadA {
					t.Fatal("verified resume did not clear the block and preserve the conversation")
				}
			} else if probe.threadID != "" || !probe.active || !probe.blocked || probe.fresh || probe.resumeThreadID != "" {
				t.Fatalf("unsafe identity accepted: %+v", probe)
			}
		})
	}
}

func TestDiscoverSoluCreatedAppServerThreadAfterTerminalSwitch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	procRoot, cwd, home := filepath.Join(root, "proc"), filepath.Join(root, "work"), filepath.Join(root, "home")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	pidDir := writeFakeCodexProcess(t, procRoot, "pane-a", 101, 100, []string{"codex", "resume", testCodexThreadA}, cwd, home)
	rollout := writeFakeRollout(t, home, testCodexThreadA, cwd, "vscode", nil)
	rewriteFakeRollout(t, rollout, func(payload map[string]any) {
		payload["originator"] = "soluterminal"
		delete(payload, "thread_source")
	})
	linkFakeFD(t, pidDir, 52, rollout)

	probe := discoverCodexIdentityAt(procRoot, "pane-a", []int{101})
	if !probe.active || probe.blocked || probe.threadID != testCodexThreadA {
		t.Fatalf("SOLU app-server thread probe = %+v, want known resumable thread", probe)
	}
}

func TestDiscoverCodexIdentitySeparatesFreshAndProvisionalResume(t *testing.T) {
	t.Parallel()
	t.Run("fresh process is safely unbound before first message", func(t *testing.T) {
		root := t.TempDir()
		procRoot, cwd, home := filepath.Join(root, "proc"), filepath.Join(root, "work"), filepath.Join(root, "home")
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFakeCodexProcess(t, procRoot, "pane-a", 101, 100, []string{"codex"}, cwd, home)
		probe := discoverCodexIdentityAt(procRoot, "pane-a", []int{101})
		if !probe.active || !probe.fresh || probe.blocked || probe.threadID != "" || probe.resumeThreadID != "" {
			t.Fatalf("fresh probe = %+v, want active verified-fresh", probe)
		}

		p := &Pane{Agent: agentCodex, CodexThreadID: testCodexThreadB}
		p.codexIdentityBlocked.Store(true)
		if !adoptFreshCodexIdentity(p) {
			t.Fatal("fresh identity was not adopted")
		}
		if snap := p.Snapshot(); snap.CodexThreadID != "" || p.codexIdentityBlocked.Load() {
			t.Fatalf("fresh adoption retained stale continuity: thread=%q blocked=%v", snap.CodexThreadID, p.codexIdentityBlocked.Load())
		}
	})

	t.Run("shell resume remains identifiable while rollout opens", func(t *testing.T) {
		root := t.TempDir()
		procRoot, cwd, home := filepath.Join(root, "proc"), filepath.Join(root, "work"), filepath.Join(root, "home")
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFakeCodexProcess(t, procRoot, "pane-a", 101, 100, []string{"codex", "resume", testCodexThreadA}, cwd, home)
		probe := discoverCodexIdentityAt(procRoot, "pane-a", []int{101})
		if !probe.active || probe.resumeThreadID != testCodexThreadA || probe.blocked || probe.fresh || probe.threadID != "" {
			t.Fatalf("provisional resume probe = %+v, want matching resume id", probe)
		}

		// A manually shell-launched `codex resume <id>` has no prior pane
		// metadata. The verified argv/process ownership is enough to adopt the
		// provisional id while the rollout is opening.
		p := &Pane{}
		p.codexIdentityBlocked.Store(true)
		if !adoptResumingCodexIdentity(p, probe.resumeThreadID) {
			t.Fatal("matching provisional resume was not adopted")
		}
		if snap := p.Snapshot(); snap.Agent != agentCodex || snap.CodexThreadID != testCodexThreadA || p.codexIdentityBlocked.Load() {
			t.Fatalf("resume adoption lost continuity: agent=%q thread=%q blocked=%v", snap.Agent, snap.CodexThreadID, p.codexIdentityBlocked.Load())
		}
	})
}

func TestResumeArgvRequiresMatchingOpenRollout(t *testing.T) {
	t.Parallel()
	t.Run("invalid or mismatched UUID", func(t *testing.T) {
		root := t.TempDir()
		procRoot, cwd, home := filepath.Join(root, "proc"), filepath.Join(root, "work"), filepath.Join(root, "home")
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
		pidDir := writeFakeCodexProcess(t, procRoot, "pane-a", 101, 100, []string{"codex", "resume", testCodexThreadB}, cwd, home)
		linkFakeFD(t, pidDir, 52, writeFakeRollout(t, home, testCodexThreadA, cwd, "cli", nil))
		if got, active := discoverCodexThreadIDAt(procRoot, "pane-a", []int{101}); got != "" || !active {
			t.Fatalf("mismatched resume discovery = (%q, %v), want empty active", got, active)
		}
	})

	t.Run("last and UUID-shaped option value use opened thread", func(t *testing.T) {
		root := t.TempDir()
		procRoot, cwd, home := filepath.Join(root, "proc"), filepath.Join(root, "work"), filepath.Join(root, "home")
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
		pidDir := writeFakeCodexProcess(t, procRoot, "pane-a", 101, 100, []string{"codex", "resume", "--last", "-p", testCodexThreadB}, cwd, home)
		linkFakeFD(t, pidDir, 52, writeFakeRollout(t, home, testCodexThreadA, cwd, "cli", nil))
		if got, active := discoverCodexThreadIDAt(procRoot, "pane-a", []int{101}); got != testCodexThreadA || !active {
			t.Fatalf("--last discovery = (%q, %v), want opened %q", got, active, testCodexThreadA)
		}
	})
}

func TestDiscoverCodexThreadFromOpenRootRollout(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	procRoot := filepath.Join(root, "proc")
	cwd := filepath.Join(root, "work")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	pidDir := writeFakeCodexProcess(t, procRoot, "pane-a", 101, 100, []string{"/bin/codex"}, cwd, home)
	rootRollout := writeFakeRollout(t, home, testCodexThreadA, cwd, "cli", nil)
	parent := testCodexThreadA
	childRollout := writeFakeRollout(t, home, testCodexThreadB, cwd, map[string]any{"subagent": "thread_spawn"}, &parent)
	linkFakeFD(t, pidDir, 52, rootRollout)
	linkFakeFD(t, pidDir, 73, childRollout)

	if got, active := discoverCodexThreadIDAt(procRoot, "pane-a", []int{101}); got != testCodexThreadA || !active {
		t.Fatalf("discover open rollout = %q, want root %q", got, testCodexThreadA)
	}
}

func TestDiscoverCodexThreadFailsClosed(t *testing.T) {
	t.Parallel()
	t.Run("multiple root candidates", func(t *testing.T) {
		root := t.TempDir()
		procRoot := filepath.Join(root, "proc")
		cwd := filepath.Join(root, "work")
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
		homeA, homeB := filepath.Join(root, "home-a"), filepath.Join(root, "home-b")
		pidA := writeFakeCodexProcess(t, procRoot, "pane-a", 101, 100, []string{"codex"}, cwd, homeA)
		pidB := writeFakeCodexProcess(t, procRoot, "pane-a", 202, 200, []string{"codex"}, cwd, homeB)
		linkFakeFD(t, pidA, 52, writeFakeRollout(t, homeA, testCodexThreadA, cwd, "cli", nil))
		linkFakeFD(t, pidB, 52, writeFakeRollout(t, homeB, testCodexThreadB, cwd, "cli", nil))
		if got, active := discoverCodexThreadIDAt(procRoot, "pane-a", []int{101, 202}); got != "" || !active {
			t.Fatalf("ambiguous discovery = %q, want empty", got)
		}
	})

	t.Run("wrong pane or background", func(t *testing.T) {
		root := t.TempDir()
		procRoot := filepath.Join(root, "proc")
		cwd := filepath.Join(root, "work")
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
		pidDir := writeFakeCodexProcess(t, procRoot, "other-pane", 101, 100, []string{"codex", "resume", testCodexThreadA}, cwd, filepath.Join(root, "home"))
		if got, active := discoverCodexThreadIDAt(procRoot, "pane-a", []int{101}); got != "" || !active {
			t.Fatalf("wrong-env descendant discovery = (%q, %v), want empty active", got, active)
		}
		// Make pgrp differ from tpgid: a background Codex must not own Focus.
		if err := os.WriteFile(filepath.Join(pidDir, "stat"), []byte("101 (codex) S 1 100 100 34818 999 0 0 0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pidDir, "environ"), []byte("SOLU_PANE_ID=pane-a\x00HOME="+filepath.Join(root, "home")+"\x00"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, active := discoverCodexThreadIDAt(procRoot, "pane-a", []int{101}); got != "" || active {
			t.Fatalf("background discovery = %q, want empty", got)
		}
	})

	t.Run("unreadable environment still invalidates stale identity", func(t *testing.T) {
		root := t.TempDir()
		procRoot, cwd, home := filepath.Join(root, "proc"), filepath.Join(root, "work"), filepath.Join(root, "home")
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
		pidDir := writeFakeCodexProcess(t, procRoot, "pane-a", 101, 100, []string{"codex"}, cwd, home)
		if err := os.Remove(filepath.Join(pidDir, "environ")); err != nil {
			t.Fatal(err)
		}
		if got, active := discoverCodexThreadIDAt(procRoot, "pane-a", []int{101}); got != "" || !active {
			t.Fatalf("unreadable-environ discovery = (%q, %v), want empty active", got, active)
		}
	})

	t.Run("transient fd inspection failure blocks without erasing", func(t *testing.T) {
		root := t.TempDir()
		procRoot, cwd, home := filepath.Join(root, "proc"), filepath.Join(root, "work"), filepath.Join(root, "home")
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
		pidDir := writeFakeCodexProcess(t, procRoot, "pane-a", 101, 100, []string{"codex"}, cwd, home)
		if err := os.Remove(filepath.Join(pidDir, "fd")); err != nil {
			t.Fatal(err)
		}
		got, active := discoverCodexThreadIDAt(procRoot, "pane-a", []int{101})
		if got != "" || !active {
			t.Fatalf("missing-fd discovery = (%q, %v), want empty active", got, active)
		}
		p := &Pane{Agent: agentCodex, CodexThreadID: testCodexThreadA, buffer: NewRingBuffer(1024)}
		adoptDiscoveredCodexThread(p, got)
		if raw := p.Snapshot().CodexThreadID; raw != testCodexThreadA {
			t.Fatalf("transient inspection erased durable thread: %q", raw)
		}
		dto := makePaneDTOWithProcessSnapshot(p, nil, nil, paneProcessSnapshot{hasCodex: true})
		if dto.CodexThreadID != "" || dto.ResumeReady {
			t.Fatalf("blocked DTO exposed stale continuity: thread=%q ready=%v", dto.CodexThreadID, dto.ResumeReady)
		}
	})
}

func TestOpenRolloutValidationFailsClosed(t *testing.T) {
	t.Parallel()
	t.Run("top-level fork remains resumable", func(t *testing.T) {
		root := t.TempDir()
		procRoot, cwd, home := filepath.Join(root, "proc"), filepath.Join(root, "work"), filepath.Join(root, "home")
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
		pidDir := writeFakeCodexProcess(t, procRoot, "pane-a", 101, 100, []string{"codex"}, cwd, home)
		rollout := writeFakeRollout(t, home, testCodexThreadA, cwd, "cli", nil)
		rewriteFakeRollout(t, rollout, func(payload map[string]any) { payload["forked_from_id"] = testCodexThreadB })
		linkFakeFD(t, pidDir, 52, rollout)
		if got, active := discoverCodexThreadIDAt(procRoot, "pane-a", []int{101}); got != testCodexThreadA || !active {
			t.Fatalf("forked-root discovery = (%q, %v), want %q active", got, active, testCodexThreadA)
		}
	})

	t.Run("session id mismatch", func(t *testing.T) {
		root := t.TempDir()
		procRoot, cwd, home := filepath.Join(root, "proc"), filepath.Join(root, "work"), filepath.Join(root, "home")
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
		pidDir := writeFakeCodexProcess(t, procRoot, "pane-a", 101, 100, []string{"codex"}, cwd, home)
		rollout := writeFakeRollout(t, home, testCodexThreadA, cwd, "cli", nil)
		rewriteFakeRollout(t, rollout, func(payload map[string]any) { payload["session_id"] = testCodexThreadB })
		linkFakeFD(t, pidDir, 52, rollout)
		if got, active := discoverCodexThreadIDAt(procRoot, "pane-a", []int{101}); got != "" || !active {
			t.Fatalf("mismatched metadata discovery = (%q, %v), want empty active", got, active)
		}
	})

	t.Run("unknown thread source", func(t *testing.T) {
		root := t.TempDir()
		procRoot, cwd, home := filepath.Join(root, "proc"), filepath.Join(root, "work"), filepath.Join(root, "home")
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
		pidDir := writeFakeCodexProcess(t, procRoot, "pane-a", 101, 100, []string{"codex"}, cwd, home)
		rollout := writeFakeRollout(t, home, testCodexThreadA, cwd, "cli", nil)
		rewriteFakeRollout(t, rollout, func(payload map[string]any) { delete(payload, "thread_source") })
		linkFakeFD(t, pidDir, 52, rollout)
		if got, _ := discoverCodexThreadIDAt(procRoot, "pane-a", []int{101}); got != "" {
			t.Fatalf("unknown thread-source discovery = %q, want empty", got)
		}
	})

	t.Run("outside CODEX_HOME", func(t *testing.T) {
		root := t.TempDir()
		procRoot, cwd := filepath.Join(root, "proc"), filepath.Join(root, "work")
		processHome, otherHome := filepath.Join(root, "home"), filepath.Join(root, "other-home")
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
		pidDir := writeFakeCodexProcess(t, procRoot, "pane-a", 101, 100, []string{"codex"}, cwd, processHome)
		linkFakeFD(t, pidDir, 52, writeFakeRollout(t, otherHome, testCodexThreadA, cwd, "cli", nil))
		if got, _ := discoverCodexThreadIDAt(procRoot, "pane-a", []int{101}); got != "" {
			t.Fatalf("outside-home discovery = %q, want empty", got)
		}
	})

	t.Run("session fd cap", func(t *testing.T) {
		root := t.TempDir()
		procRoot, cwd, home := filepath.Join(root, "proc"), filepath.Join(root, "work"), filepath.Join(root, "home")
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
		pidDir := writeFakeCodexProcess(t, procRoot, "pane-a", 101, 100, []string{"codex"}, cwd, home)
		rollout := writeFakeRollout(t, home, testCodexThreadA, cwd, "cli", nil)
		for i := 0; i <= codexSessionFDMax; i++ {
			linkFakeFD(t, pidDir, 50+i, rollout)
		}
		if got, active := discoverCodexThreadIDAt(procRoot, "pane-a", []int{101}); got != "" || !active {
			t.Fatalf("over-cap discovery = (%q, %v), want empty active", got, active)
		}
	})
}

func TestForegroundProviderIdentityIsAtomicAndReplaceable(t *testing.T) {
	t.Parallel()
	persisted := 0
	p := &Pane{Agent: "", CodexThreadID: ""}
	p.onPromptsCommit = func(*Pane) { persisted++ }
	if !adoptDiscoveredCodexThread(p, testCodexThreadA) {
		t.Fatal("first identity adoption reported unchanged")
	}
	snap := p.Snapshot()
	if snap.Agent != agentCodex || snap.CodexThreadID != testCodexThreadA {
		t.Fatalf("adopted identity = {agent:%q thread:%q}", snap.Agent, snap.CodexThreadID)
	}
	if adoptDiscoveredCodexThread(p, testCodexThreadA) {
		t.Fatal("duplicate identity adoption reported changed")
	}
	if !adoptDiscoveredCodexThread(p, testCodexThreadB) {
		t.Fatal("new foreground thread did not replace stale binding")
	}
	if got := p.Snapshot().CodexThreadID; got != testCodexThreadB {
		t.Fatalf("thread after overwrite attempt = %q", got)
	}
	if !adoptDiscoveredCodexThread(p, "") {
		t.Fatal("active unidentified Codex did not block stale thread")
	}
	if snap := p.Snapshot(); snap.Agent != agentCodex || snap.CodexThreadID != testCodexThreadB || !p.codexIdentityBlocked.Load() {
		t.Fatalf("unidentified Codex identity = {agent:%q thread:%q}", snap.Agent, snap.CodexThreadID)
	}
	p.ClaudeSessionID = "old-claude-session"
	p.ClaudeTranscriptPath = "/old/claude.jsonl"
	if !adoptForegroundClaude(p) {
		t.Fatal("foreground Claude did not reclaim shell provider")
	}
	if snap := p.Snapshot(); snap.Agent != "" || snap.CodexThreadID != "" || snap.ClaudeSessionID != "" || snap.ClaudeTranscriptPath != "" {
		t.Fatalf("Claude identity = {agent:%q thread:%q claude:%q path:%q}", snap.Agent, snap.CodexThreadID, snap.ClaudeSessionID, snap.ClaudeTranscriptPath)
	}
	if persisted != 4 {
		t.Fatalf("persistence callback count = %d, want 4", persisted)
	}
}

func TestManagerCurrentPaneLeaseRejectsStalePointer(t *testing.T) {
	t.Parallel()
	mgr := NewManager(t.TempDir())
	current := &Pane{ID: "pane-a"}
	stale := &Pane{ID: "pane-a"}
	mgr.panes[current.ID] = current
	called := false
	if mgr.withCurrentPane(stale, func() { called = true }) || called {
		t.Fatal("stale pane pointer received persistence lease")
	}
	if !mgr.withCurrentPane(current, func() { called = true }) || !called {
		t.Fatal("current pane pointer did not receive persistence lease")
	}
}

func TestAmbiguousProviderDTOHidesBothContinuityDomains(t *testing.T) {
	t.Parallel()
	p := &Pane{
		Agent:                "",
		ClaudeSessionID:      "claude-last-good",
		ClaudeTranscriptPath: "/claude/last-good.jsonl",
		CodexThreadID:        testCodexThreadA,
		buffer:               NewRingBuffer(1024),
	}
	p.codexIdentityBlocked.Store(true)
	dto := makePaneDTOWithProcessSnapshot(p, nil, nil, paneProcessSnapshot{hasClaude: true, hasCodex: true})
	if !dto.IdentityBlocked || dto.ResumeReady || dto.ClaudeSessionID != "" || dto.ClaudeTranscriptPath != "" || dto.CodexThreadID != "" {
		t.Fatalf("blocked DTO leaked continuity: blocked=%v ready=%v claude=%q path=%q codex=%q",
			dto.IdentityBlocked, dto.ResumeReady, dto.ClaudeSessionID, dto.ClaudeTranscriptPath, dto.CodexThreadID)
	}
	// Durable recovery metadata remains intact behind the DTO safety boundary.
	snap := p.Snapshot()
	if snap.ClaudeSessionID != "claude-last-good" || snap.CodexThreadID != testCodexThreadA {
		t.Fatalf("blocked DTO mutated durable metadata: claude=%q codex=%q", snap.ClaudeSessionID, snap.CodexThreadID)
	}
}
