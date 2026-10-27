package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestShellSingleQuote(t *testing.T) {
	cases := map[string]string{
		"":            "''",
		"plain":       "'plain'",
		"has space":   "'has space'",
		"can't fail":  "'can'\\''t fail'",
		"$HOME && rm": "'$HOME && rm'",
		"line\nbreak": "'line\nbreak'",
	}
	for in, want := range cases {
		if got := shellSingleQuote(in); got != want {
			t.Fatalf("shellSingleQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTryLoginShellWrapSkipsPathCommands(t *testing.T) {
	cases := [][]string{
		{"/usr/bin/claude"},
		{"./claude"},
		{`C:\Tools\claude.exe`},
	}
	for _, cmd := range cases {
		if got, ok := tryLoginShellWrap(cmd, "", nil); ok || got != nil {
			t.Fatalf("tryLoginShellWrap(%v) = (%v, %v), want (nil, false)", cmd, got, ok)
		}
	}
}

func TestTryLoginShellWrapWrapsBareCommandOnUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("login shell wrapping is Unix-only")
	}
	got, ok := tryLoginShellWrap([]string{"printf", "hello world", "can't"}, "", nil)
	if !ok {
		t.Fatal("tryLoginShellWrap returned ok=false for bare command")
	}
	if len(got) != 3 || got[1] != "-lc" {
		t.Fatalf("wrapped command = %#v, want bash -lc ...", got)
	}
	if !strings.Contains(got[2], "exec 'printf' 'hello world' 'can'\\''t'") {
		t.Fatalf("wrapped script = %q, missing quoted exec", got[2])
	}
}

func TestTryLoginShellWrapCodexAppServerPreservesProtocolStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("login shell wrapping is Unix-only")
	}
	got, ok := tryLoginShellWrap([]string{"codex", "app-server", "--stdio"}, "", nil)
	if !ok {
		t.Fatal("tryLoginShellWrap returned ok=false for Codex app-server")
	}
	if len(got) != 3 || got[1] != "-c" {
		t.Fatalf("wrapped command = %#v, want bash -c so login-profile helpers cannot consume JSON-RPC stdin", got)
	}
	if !strings.Contains(got[2], loginShellInit()) {
		t.Fatalf("Codex app-server wrapper dropped the explicit version-manager init: %q", got[2])
	}

	// Exercise the contract, not just the argv shape. A login profile is allowed
	// to run arbitrary helpers; this hostile one models the WSL interop probe
	// that inherited and consumed the app-server's initialize frame in production.
	home := t.TempDir()
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".profile"), []byte("IFS= read -r _stolen\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeCodex := "#!/bin/sh\nIFS= read -r line\nprintf '%s\\n' \"$line\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(fakeCodex), 0o755); err != nil {
		t.Fatal(err)
	}
	c := exec.Command(got[0], got[1:]...)
	c.Env = append(os.Environ(), "HOME="+home, "PATH=/usr/bin:/bin")
	c.Stdin = strings.NewReader("initialize-frame\n")
	out, err := c.Output()
	if err != nil {
		t.Fatalf("run wrapped Codex app-server: %v", err)
	}
	if strings.TrimSpace(string(out)) != "initialize-frame" {
		t.Fatalf("wrapped app-server stdin = %q, want untouched initialize frame", out)
	}
}

// A claude command runs in the FOREGROUND and hands the PTY to an interactive
// shell when it exits (`…; 'claude' …; exec <bash> -i`), instead of exec-ing
// claude — which would replace bash and leave no shell to drop back to. This is
// the "exit Claude → back to a shell" behavior.
func TestTryLoginShellWrapClaudeDropsToShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("login shell wrapping is Unix-only")
	}
	got, ok := tryLoginShellWrap([]string{"claude", "--resume", "abc"}, "", nil)
	if !ok {
		t.Fatal("tryLoginShellWrap returned ok=false for claude")
	}
	script := got[2]
	if !strings.Contains(script, "'claude' '--resume' 'abc'") {
		t.Fatalf("script = %q, missing the quoted claude command", script)
	}
	// claude itself must NOT be exec'd (that would replace the shell, killing the
	// pane on exit instead of dropping to a prompt).
	if strings.Contains(script, "exec 'claude'") {
		t.Fatalf("script = %q, claude must run in the foreground, not be exec'd", script)
	}
	// The shell takes over the PTY when claude exits.
	if !strings.Contains(script, "; exec ") || !strings.HasSuffix(script, " -i") {
		t.Fatalf("script = %q, want a trailing `; exec <bash> -i` so claude exit drops to a shell", script)
	}
}

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cases := map[string]string{
		"":         "",
		"/tmp/app": "/tmp/app",
		"~":        home,
		"~/work":   home + "/work",
		"~other":   "~other",
	}
	for in, want := range cases {
		if got := expandHome(in); got != want {
			t.Fatalf("expandHome(%q) = %q, want %q", in, got, want)
		}
	}
}
