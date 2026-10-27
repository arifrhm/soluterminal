//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func fakeProcessExecutable(t *testing.T, dir, target string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "exe")); err != nil {
		t.Fatal(err)
	}
}

func TestProviderProcessNameExcludesOnlyDaemonShims(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, tc := range []struct {
			executable string
			shim       bool
		}{
			{"/app/solud", true}, {"/app/solud.old", true},
			{"/app/solud.old.2 (deleted)", true}, {"/app/solud (deleted)", true},
			{"/other/solud", false}, {"/app/solud.old.invalid", false},
			{"/app/" + provider + " (deleted)", false}, {"", false},
		} {
			t.Run(provider+tc.executable, func(t *testing.T) {
				root := t.TempDir()
				dir := writeFakeCodexProcess(t, root, "pane-a", 101, 100, []string{provider}, root, root)
				if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(provider), 0600); err != nil {
					t.Fatal(err)
				}
				fakeProcessExecutable(t, filepath.Join(root, "self"), "/app/solud")
				if tc.executable != "" {
					fakeProcessExecutable(t, dir, tc.executable)
				}
				want := provider
				if tc.shim {
					want = "solud"
				}
				if got := providerProcessNameAt(root, 101); got != want {
					t.Fatalf("name=%q want %q", got, want)
				}
			})
		}
	}
}

func TestDiscoverShellCodexWithDaemonShim(t *testing.T) {
	for _, mode := range []string{"fresh", "resume", "remote", "opening", "two-real-clients"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			proc, home := filepath.Join(root, "proc"), filepath.Join(root, "home")
			fakeProcessExecutable(t, filepath.Join(proc, "self"), "/app/solud")
			args := []string{"codex", "--no-daemon", "--no-alt-screen"}
			if mode == "resume" {
				args = []string{"codex", "resume", testCodexThreadA}
			}
			shim := writeFakeCodexProcess(t, proc, "pane-a", 101, 100, args, root, home)
			fakeProcessExecutable(t, shim, "/app/solud.old.2 (deleted)")
			if mode == "remote" {
				args = []string{"codex", "--remote", "unix:///tmp/pane.sock"}
			}
			cli := writeFakeCodexProcess(t, proc, "pane-a", 103, 100, args, root, home)
			fakeProcessExecutable(t, cli, "/npm/vendor/bin/codex (deleted)")
			pids := []int{101, 102, 103}
			node := writeFakeCodexProcess(t, proc, "pane-a", 102, 100, []string{"node", "codex.js"}, root, home)
			if err := os.WriteFile(filepath.Join(node, "comm"), []byte("MainThread"), 0600); err != nil {
				t.Fatal(err)
			}
			owner := cli
			if mode == "remote" {
				owner = writeFakeCodexProcess(t, proc, "pane-a", 104, 100, []string{"codex", "app-server", "--listen", "unix:///tmp/pane.sock"}, root, home)
				pids = append(pids, 104)
			}
			if mode != "opening" {
				linkFakeFD(t, owner, 52, writeFakeRollout(t, home, testCodexThreadA, root, "cli", nil))
			}
			if mode == "two-real-clients" {
				writeFakeCodexProcess(t, proc, "pane-a", 105, 100, args, root, home)
				pids = append(pids, 105)
			}
			probe := discoverCodexIdentityAt(proc, "pane-a", pids)
			if mode == "two-real-clients" {
				if !probe.blocked || probe.threadID != "" {
					t.Fatalf("ambiguous clients accepted: %+v", probe)
				}
			} else if mode == "opening" {
				if !probe.active || !probe.fresh || probe.blocked {
					t.Fatalf("fresh CLI blocked by shim: %+v", probe)
				}
			} else if probe.threadID != testCodexThreadA || !probe.active || probe.blocked {
				t.Fatalf("shell CLI identity missing: %+v", probe)
			}
		})
	}
}
