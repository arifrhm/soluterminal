//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func codexShellReconnectPlanAt(procRoot, paneID, thread string, pids []int) ([]string, string, bool) {
	fg, ok := codexForegroundAt(procRoot, paneID, 0, thread, pids)
	return fg.args, fg.cwd, ok && fg.args != nil
}

func codexForegroundContextAt(procRoot, paneID string, root int, pids []int) (string, bool, bool) {
	fg, ok := codexForegroundAt(procRoot, paneID, root, reconnectTestThread, pids)
	return fg.cwd, fg.underShell, ok
}

func TestInteractiveShellProcess(t *testing.T) {
	for argv, want := range map[string]bool{
		"bash":                     true,
		"bash -i":                  true,
		"-bash":                    true,
		"zsh -l":                   true,
		"fish --login":             true,
		"bash -lc codex":           false,
		"bash -c codex":            false,
		"sh /home/u/bin/launch":    false,
		"bash ./run-codex.sh":      false,
		"fish --command codex":     false,
		"fish --command=codex":     false,
		"bash --rcfile /tmp/rc -i": false,
	} {
		args := strings.Fields(argv)
		comm := strings.TrimPrefix(args[0], "-")
		if got := interactiveShellProcess(comm, args); got != want {
			t.Fatalf("%q => %v, want %v", argv, got, want)
		}
	}
	if interactiveShellProcess("node", []string{"node"}) {
		t.Fatal("non-shell treated as a prompt")
	}
}

// fakeCodexProc lays out the /proc entries codexShellReconnectPlanAt reads.
func fakeCodexProc(t *testing.T, root string, pid int, comm string, foreground bool, argv, env []string, cwd string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	tpgid := pid
	if !foreground {
		tpgid = pid + 1
	}
	stat := strconv.Itoa(pid) + " (" + comm + ") S 1 " + strconv.Itoa(pid) + " " + strconv.Itoa(pid) + " 34816 " + strconv.Itoa(tpgid) + " 0"
	_ = os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "environ"), []byte(strings.Join(env, "\x00")+"\x00"), 0o644)
	if cwd != "" {
		_ = os.Symlink(cwd, filepath.Join(dir, "cwd"))
	}
	exe := "/usr/bin/" + comm
	if comm == "codex" && argv[0] == "codex" {
		exe = "/usr/local/share/soluterminal/solud.old.1 (deleted)" // the pane's codex shim
	} else if comm == "codex" {
		exe = "/opt/codex/bin/codex (deleted)"
	}
	_ = os.Symlink(exe, filepath.Join(dir, "exe"))
}

// setProcParent rewrites a fake process's parent pid.
func setProcParent(t *testing.T, root string, pid, ppid int) {
	t.Helper()
	path := filepath.Join(root, strconv.Itoa(pid), "stat")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	end := strings.LastIndex(s, ")")
	fields := strings.Fields(s[end+1:])
	fields[1] = strconv.Itoa(ppid)
	if err := os.WriteFile(path, []byte(s[:end+1]+" "+strings.Join(fields, " ")), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCodexForegroundContextUsesTheRunningDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("procfs")
	}
	work := t.TempDir()
	env := []string{"SOLU_PANE_ID=pane_x"}
	layout := func(shellArgv []string) (string, []int) {
		root := t.TempDir()
		fakeCodexProc(t, root, 100, "solud", false, []string{"solud", "agent"}, env, "/")
		fakeCodexProc(t, root, 101, "bash", false, shellArgv, env, "/")
		fakeCodexProc(t, root, 102, "MainThread", true, []string{"node", "codex"}, env, work)
		fakeCodexProc(t, root, 103, "codex", true, []string{"/opt/codex", "resume", reconnectTestThread}, env, work)
		setProcParent(t, root, 101, 100)
		setProcParent(t, root, 102, 101)
		setProcParent(t, root, 103, 102)
		return root, []int{101, 102, 103}
	}
	// Exit-to-shell: `bash -lc` exec'd into `bash -i`, then codex relaunched in another directory.
	root, pids := layout([]string{"bash", "-i"})
	cwd, underShell, ok := codexForegroundContextAt(root, "pane_x", 100, pids)
	if !ok || cwd != work || !underShell {
		t.Fatalf("exit-to-shell => cwd=%q underShell=%v ok=%v", cwd, underShell, ok)
	}
	// A login-shell launch wrapper is not a prompt the reconnect would take away.
	root, pids = layout([]string{"bash", "-lc", "codex resume x; exec bash -i"})
	if _, underShell, ok := codexForegroundContextAt(root, "pane_x", 100, pids); !ok || underShell {
		t.Fatalf("launch wrapper treated as a prompt: underShell=%v ok=%v", underShell, ok)
	}
	if _, _, ok := codexForegroundContextAt(root, "pane_other", 100, pids); ok {
		t.Fatal("another pane's Codex accepted")
	}
	// A deleted working directory is reported, so reconnect refuses instead of
	// silently resuming in the pane's recorded directory.
	gone := filepath.Join(t.TempDir(), "gone")
	_ = os.Remove(filepath.Join(root, "103", "cwd"))
	_ = os.Symlink(gone+" (deleted)", filepath.Join(root, "103", "cwd"))
	if cwd, _, ok := codexForegroundContextAt(root, "pane_x", 100, pids); !ok || cwd != gone+" (deleted)" {
		t.Fatalf("deleted cwd => %q %v", cwd, ok)
	}
}
