//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverMuseIdentityRequiresExactForegroundSession(t *testing.T) {
	t.Parallel()
	for _, variant := range []string{"valid", "custom-data-root", "different-pane", "background", "wrong-pid", "missing-lock", "missing-transcript", "conflicting-session", "unreadable-fd", "wrong-process"} {
		t.Run(variant, func(t *testing.T) {
			root := t.TempDir()
			procRoot, home := filepath.Join(root, "proc"), filepath.Join(root, "home")
			pidDir := writeFakeCodexProcess(t, procRoot, "pane-muse", 101, 100, []string{"muse"}, root, home)
			write := func(path, body string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(pidDir, "comm"), "muse-bin-1.1.1-\n")
			museRoot := filepath.Join(home, ".local", "share", "muse")
			if variant == "custom-data-root" {
				// A plausible default root must not mask the active process's
				// explicit data directory or supply a different old conversation.
				write(filepath.Join(museRoot, "sessions", "2026", "09", "11", testCodexThreadB, "session.jsonl"), "old default conversation\n")
				dataRoot := filepath.Join(root, "custom-data")
				museRoot = filepath.Join(dataRoot, "muse")
				write(filepath.Join(pidDir, "environ"), "SOLU_PANE_ID=pane-muse\x00HOME="+home+"\x00XDG_DATA_HOME="+dataRoot+"\x00")
			}
			sessionDir := filepath.Join(museRoot, "sessions", "2026", "09", "11", testCodexThreadA)
			lock := filepath.Join(sessionDir, ".session.lock")
			write(lock, "")
			if variant != "missing-lock" {
				linkFakeFD(t, pidDir, 16, lock)
			}
			if variant != "missing-transcript" {
				write(filepath.Join(sessionDir, "session.jsonl"), "durable existing conversation\n")
			}
			runtimeID := testCodexThreadA
			if variant == "conflicting-session" {
				runtimeID = testCodexThreadB
			}
			generation := "pid=101"
			if variant == "wrong-pid" {
				generation = "pid=202"
			}
			runtimePath := filepath.Join(museRoot, "runtime", "muse", "sessions", runtimeID+".json")
			write(runtimePath, fmt.Sprintf(`{"session_id":%q,"process_generation_hint":%q}`, runtimeID, generation))
			linkFakeFD(t, pidDir, 51, runtimePath)
			if variant == "different-pane" {
				write(filepath.Join(pidDir, "environ"), "SOLU_PANE_ID=other\x00HOME="+home+"\x00")
			}
			if variant == "background" {
				write(filepath.Join(pidDir, "stat"), "101 (muse) S 1 100 100 34818 999 0 0\n")
			}
			if variant == "wrong-process" {
				write(filepath.Join(pidDir, "comm"), "bash\n")
			}
			if variant == "unreadable-fd" {
				write(filepath.Join(pidDir, "fd", "99"), "not a symlink")
			}
			id, active := discoverMuseIdentityAt(procRoot, "pane-muse", []int{101})
			if variant == "valid" || variant == "custom-data-root" {
				if id != testCodexThreadA || !active {
					t.Fatalf("got %q/%v, want exact session", id, active)
				}
			} else if id != "" {
				t.Fatalf("unsafe %s accepted session %q", variant, id)
			} else if variant != "background" && variant != "wrong-process" && !active {
				t.Fatal("unverified foreground process must block old identity")
			}
		})
	}
}
