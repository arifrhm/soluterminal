//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTurnProcStat(t *testing.T, dir string, pid, parent, generation int) {
	t.Helper()
	fields := []string{"S", fmt.Sprint(parent), "100", "100", "34818", "100"}
	for len(fields) < 19 {
		fields = append(fields, "0")
	}
	fields = append(fields, fmt.Sprint(generation))
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(fmt.Sprintf("%d (codex) %s\n", pid, strings.Join(fields, " "))), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestTurnProviderSourceCodexOwnershipAndRevalidation(t *testing.T) {
	for _, mode := range []string{"valid", "wrong-pane", "not-descendant", "reused-pid", "changed-fd", "closed-fd", "nested-root"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			procRoot := filepath.Join(root, "proc")
			home := filepath.Join(root, "home")
			parent := writeFakeCodexProcess(t, procRoot, "pane-a", 100, 100, []string{"bash"}, root, home)
			os.WriteFile(filepath.Join(parent, "comm"), []byte("bash\n"), 0600)
			writeTurnProcStat(t, parent, 100, 1, 123)
			process := writeFakeCodexProcess(t, procRoot, "pane-a", 101, 100, []string{"codex"}, root, home)
			writeTurnProcStat(t, process, 101, 100, 124)
			var parentID *string
			if mode == "nested-root" {
				v := testCodexThreadB
				parentID = &v
			}
			path := writeFakeRollout(t, home, testCodexThreadA, root, "cli", parentID)
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(file, providerCodexLine("task_started", "a", turnProviderStart))
			fmt.Fprintln(file, providerCodexLine("task_complete", "a", turnProviderStart+1000))
			file.Close()
			linkFakeFD(t, process, 52, path)
			if mode == "wrong-pane" {
				os.WriteFile(filepath.Join(process, "environ"), []byte("SOLU_PANE_ID=other\x00HOME="+home+"\x00"), 0600)
			}
			if mode == "not-descendant" {
				writeTurnProcStat(t, process, 101, 999, 124)
			}
			source := openTurnProviderSourceAt(procRoot, "pane-a", 100, []int{100, 101})
			if mode == "wrong-pane" || mode == "not-descendant" || mode == "nested-root" {
				if source != nil {
					source.file.Close()
					t.Fatal("unowned source accepted")
				}
				return
			}
			if source == nil {
				t.Fatal("owned Codex source unavailable")
			}
			switch mode {
			case "reused-pid":
				writeTurnProcStat(t, process, 101, 100, 999)
			case "changed-fd":
				replacement := writeFakeRollout(t, home, testCodexThreadB, root, "cli", nil)
				os.Remove(filepath.Join(process, "fd", "52"))
				linkFakeFD(t, process, 52, replacement)
			case "closed-fd":
				os.Remove(filepath.Join(process, "fd", "52"))
			}
			r := &turnProviderReader{}
			got := r.read(source, turnProviderStart+10000)
			if mode == "valid" {
				if !got.available || len(got.completed) != 1 {
					t.Fatalf("owned lifecycle: %+v", got)
				}
			} else if got.available || len(got.completed) > 0 {
				t.Fatal("changed process/source published lifecycle")
			}
		})
	}
}
func TestTurnProviderSourceClaudeRequiresUniqueMainOpenTranscript(t *testing.T) {
	for _, mode := range []string{"valid", "subagent", "multiple", "wrong-pane"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			procRoot := filepath.Join(root, "proc")
			home := filepath.Join(root, "home")
			process := writeFakeCodexProcess(t, procRoot, "pane-a", 101, 100, []string{"claude"}, root, home)
			os.WriteFile(filepath.Join(process, "comm"), []byte("claude\n"), 0600)
			writeTurnProcStat(t, process, 101, 1, 124)
			dir := filepath.Join(home, ".claude", "projects", "encoded")
			if mode == "subagent" {
				dir = filepath.Join(dir, turnProviderSession, "subagents")
			}
			os.MkdirAll(dir, 0700)
			path := filepath.Join(dir, turnProviderSession+".jsonl")
			os.WriteFile(path, []byte("{}\n"), 0600)
			linkFakeFD(t, process, 52, path)
			if mode == "multiple" {
				other := filepath.Join(dir, testCodexThreadB+".jsonl")
				os.WriteFile(other, []byte("{}\n"), 0600)
				linkFakeFD(t, process, 53, other)
			}
			if mode == "wrong-pane" {
				os.WriteFile(filepath.Join(process, "environ"), []byte("SOLU_PANE_ID=other\x00HOME="+home+"\x00"), 0600)
			}
			source := openTurnProviderSourceAt(procRoot, "pane-a", 101, []int{101})
			if source != nil {
				defer source.file.Close()
			}
			if mode == "valid" {
				if source == nil || source.agent != agentClaude || source.session != turnProviderSession {
					t.Fatal("valid main transcript missing")
				}
			} else if source != nil {
				t.Fatal("ambiguous/sidechain source accepted")
			}
		})
	}
}

func TestTurnProviderClaudeClosedTranscriptExactSessionFallback(t *testing.T) {
	for _, mode := range []string{"valid", "daemon-shim", "missing-argv", "wrong-session", "wrong-pane", "conflicting-open", "outside-root", "pid-reused"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			procRoot := filepath.Join(root, "proc")
			home := filepath.Join(root, "home")
			argv := []string{"claude", "--resume", turnProviderSession}
			if mode == "missing-argv" {
				argv = []string{"claude"}
			}
			if mode == "wrong-session" {
				argv[2] = testCodexThreadB
			}
			process := writeFakeCodexProcess(t, procRoot, "pane-a", 101, 100, argv, root, home)
			os.WriteFile(filepath.Join(process, "comm"), []byte("claude\n"), 0600)
			writeTurnProcStat(t, process, 101, 1, 124)
			target := filepath.Join(root, "other")
			os.WriteFile(target, []byte(""), 0600)
			linkFakeFD(t, process, 1, target)
			dir := filepath.Join(home, ".claude", "projects", "encoded")
			os.MkdirAll(dir, 0700)
			path := filepath.Join(dir, turnProviderSession+".jsonl")
			os.WriteFile(path, []byte("{}\n"), 0600)
			if mode == "wrong-pane" {
				os.WriteFile(filepath.Join(process, "environ"), []byte("SOLU_PANE_ID=other\x00HOME="+home+"\x00"), 0600)
			}
			if mode == "conflicting-open" {
				other := filepath.Join(dir, testCodexThreadB+".jsonl")
				os.WriteFile(other, []byte("{}\n"), 0600)
				linkFakeFD(t, process, 2, other)
			}
			if mode == "outside-root" {
				path = filepath.Join(root, turnProviderSession+".jsonl")
				os.WriteFile(path, []byte("{}\n"), 0600)
			}
			pids := []int{101}
			if mode == "daemon-shim" {
				shim := writeFakeCodexProcess(t, procRoot, "pane-a", 102, 100, argv, root, home)
				if err := os.WriteFile(filepath.Join(shim, "comm"), []byte("claude"), 0600); err != nil {
					t.Fatal(err)
				}
				fakeProcessExecutable(t, filepath.Join(procRoot, "self"), "/app/solud")
				fakeProcessExecutable(t, shim, "/app/solud.old.2 (deleted)")
				pids = []int{102, 101}
			}
			source := openClaudeTurnFallbackAt(procRoot, "pane-a", 101, pids, turnProviderSession, path)
			if mode != "valid" && mode != "daemon-shim" && mode != "pid-reused" {
				if source != nil {
					source.file.Close()
					t.Fatal("unproven fallback accepted")
				}
				return
			}
			if source == nil {
				t.Fatal("exact live session fallback unavailable")
			}
			defer source.file.Close()
			if mode == "pid-reused" {
				writeTurnProcStat(t, process, 101, 1, 999)
				if source.valid() {
					t.Fatal("PID reuse retained source")
				}
			}
		})
	}
}

// One iteration is a full64-pane sweep with32FDs each. Report resource cost
// without a flaky wall-clock assertion on shared CI hosts. No provider runs.
func BenchmarkTurnProvider64Panes(b *testing.B) {
	for _, mode := range []string{"cached", "discovery", "missing-source"} {
		b.Run(mode, func(b *testing.B) {
			base := b.TempDir()
			procRoot := filepath.Join(base, "proc")
			type fixture struct {
				pane   string
				pid    int
				reader turnProviderReader
			}
			fixtures := make([]fixture, 64)
			for i := range fixtures {
				pid := 100 + i
				pane := fmt.Sprintf("pane-%d", i)
				home := filepath.Join(base, pane)
				dir := filepath.Join(procRoot, fmt.Sprint(pid))
				cwd := filepath.Join(home, "work")
				os.MkdirAll(filepath.Join(dir, "fd"), 0700)
				os.MkdirAll(cwd, 0700)
				fields := []string{"S", "1", fmt.Sprint(pid), fmt.Sprint(pid), "34818", fmt.Sprint(pid)}
				for len(fields) < 19 {
					fields = append(fields, "0")
				}
				fields = append(fields, "12345")
				for name, value := range map[string]string{"comm": "codex\n", "cmdline": "codex\x00", "environ": "SOLU_PANE_ID=" + pane + "\x00HOME=" + home + "\x00", "stat": fmt.Sprintf("%d (codex) %s\n", pid, strings.Join(fields, " "))} {
					os.WriteFile(filepath.Join(dir, name), []byte(value), 0600)
				}
				os.Symlink(cwd, filepath.Join(dir, "cwd"))
				sessions := filepath.Join(home, ".codex", "sessions", "2026", "10", "06")
				os.MkdirAll(sessions, 0700)
				path := filepath.Join(sessions, "rollout-2026-10-06T00-00-00-"+testCodexThreadA+".jsonl")
				metadata := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"session_id":%q,"cwd":%q,"originator":"codex-tui","source":"cli","thread_source":"user"}}`, testCodexThreadA, testCodexThreadA, cwd)
				os.WriteFile(path, []byte(metadata+"\n"+providerCodexLine("task_started", "turn", turnProviderStart)+"\n"), 0600)
				other := filepath.Join(home, "other")
				os.WriteFile(other, nil, 0600)
				for fd := 0; fd < 32; fd++ {
					target := other
					if fd == 31 && mode != "missing-source" {
						target = path
					}
					os.Symlink(target, filepath.Join(dir, "fd", fmt.Sprint(fd)))
				}
				fixtures[i] = fixture{pane: pane, pid: pid}
				if mode == "cached" {
					source := openTurnProviderSourceAt(procRoot, pane, pid, []int{pid})
					if source == nil {
						b.Fatal("fixture source unavailable")
					}
					fixtures[i].reader.read(source, turnProviderStart+10000)
				}
			}
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				for i := range fixtures {
					f := &fixtures[i]
					var source *turnProviderSource
					if mode == "cached" {
						source = f.reader.reopen()
					} else {
						source = openTurnProviderSourceAt(procRoot, f.pane, f.pid, []int{f.pid})
					}
					if mode == "missing-source" {
						if source != nil {
							source.file.Close()
							b.Fatal("unowned source")
						}
						continue
					}
					if source == nil || !f.reader.read(source, turnProviderStart+10000).available {
						b.Fatal("source lost")
					}
				}
			}
		})
	}
}
