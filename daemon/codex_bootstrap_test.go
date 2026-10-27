package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestClassifyCodexProbeOutput(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		outcome codexProbeOutcome
		version string
		loginOK bool
	}{
		{"found with banner", "Welcome!\n" + codexVersionMarker + "codex-cli 0.164.0\n", codexProbeFound, "codex-cli 0.164.0", false},
		{"found and signed in", codexVersionMarker + "codex-cli 0.164.0\n\n" + codexAuthedMarker, codexProbeFound, "codex-cli 0.164.0", true},
		{"absent", codexAbsentMarker, codexProbeAbsent, "", false},
		{"shell died", "banner only", codexProbeError, "", false},
	}
	for _, c := range cases {
		got := classifyCodexProbeOutput(c.out)
		if got.outcome != c.outcome || got.version != c.version || got.loginOK != c.loginOK {
			t.Fatalf("%s: got %+v", c.name, got)
		}
	}
}

// The login-status check must only ever contribute its exit code: all of its
// output goes to /dev/null, and it runs only when asked.
func TestCodexProbeScriptDiscardsLoginStatusOutput(t *testing.T) {
	with := codexProbeScript(true)
	if !strings.Contains(with, "command codex login status </dev/null >/dev/null 2>&1 && printf") {
		t.Fatalf("login status output is not discarded: %q", with)
	}
	if strings.Index(with, "command codex --version") > strings.Index(with, "command codex login status") {
		t.Fatal("version must be fenced before the login status runs")
	}
	if without := codexProbeScript(false); strings.Contains(without, "login status") {
		t.Fatalf("login status ran although the auth file was present: %q", without)
	}
}

// End-to-end on Unix: a fake codex in ~/.local/bin (OpenAI's install.sh
// location) that reports its login only through the exit code, the way a
// credential-store login looks with no auth.json.
func TestDetectCodexCLIFindsLocalBinAndLoginStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("login-shell probe is Unix-only")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	bin := filepath.Join(home, ".local", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = --version ]; then echo 'codex-cli 9.9.9'; exit 0; fi\n" +
		"if [ \"$1\" = login ] && [ \"$2\" = status ]; then echo 'Logged in using an API key - sk-SECRET'; [ -f \"$HOME/signed-in\" ]; exit $?; fi\n" +
		"exit 2\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	res := detectCodexCLI(true)
	if res.outcome != codexProbeFound || res.version != "codex-cli 9.9.9" || res.loginOK {
		t.Fatalf("signed-out probe = %+v", res)
	}
	if err := os.WriteFile(filepath.Join(home, "signed-in"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	res = detectCodexCLI(true)
	if res.outcome != codexProbeFound || !res.loginOK {
		t.Fatalf("signed-in probe = %+v", res)
	}
	if strings.Contains(res.version, "SECRET") {
		t.Fatal("login status output leaked into the probe result")
	}
	if res = detectCodexCLI(false); res.loginOK {
		t.Fatal("login status ran although it was not requested")
	}
}

func TestDetectCodexWindowsOutcomes(t *testing.T) {
	ctx := context.Background()
	if res := detectCodexWindows(ctx, true, func() (string, error) { return "", errors.New("missing") }); res.outcome != codexProbeAbsent {
		t.Fatalf("unresolved codex = %+v, want absent", res)
	}
	missing := filepath.Join(t.TempDir(), "codex.exe")
	if res := detectCodexWindows(ctx, true, func() (string, error) { return missing, nil }); res.outcome != codexProbeError {
		t.Fatalf("resolved but unrunnable codex = %+v, want error (no verdict)", res)
	}
}

func TestCodexBinaryUnderHome(t *testing.T) {
	home := t.TempDir()
	if codexBinaryUnderHome(home) {
		t.Fatal("empty home reported a codex binary")
	}
	writeProviderBinFixture(t, filepath.Join(home, ".nvm", "versions", "node", "v22.0.0", "bin", "codex"))
	if !codexBinaryUnderHome(home) {
		t.Fatal("nvm-managed codex not detected")
	}
}

func TestCodexInstallCommandFor(t *testing.T) {
	unix := codexInstallCommandFor("linux")
	if !unix.Supported || unix.Display != "curl -fsSL https://chatgpt.com/codex/install.sh | sh" {
		t.Fatalf("unix spec = %+v", unix)
	}
	if len(unix.Cmd) != 3 || unix.Cmd[0] != "bash" || unix.Cmd[1] != "-lc" {
		t.Fatalf("unix argv = %v", unix.Cmd)
	}
	for _, want := range []string{loginShellInit(), "CODEX_NON_INTERACTIVE=1", unix.Display, "command -v codex", "Open a new pane", "exec bash -i"} {
		if !strings.Contains(unix.Cmd[2], want) {
			t.Fatalf("unix script missing %q: %s", want, unix.Cmd[2])
		}
	}

	win := codexInstallCommandFor("windows")
	if !win.Supported || win.Display != `powershell -ExecutionPolicy ByPass -c "irm https://chatgpt.com/codex/install.ps1 | iex"` {
		t.Fatalf("windows spec = %+v", win)
	}
	ps := win.Cmd[len(win.Cmd)-1]
	if win.Cmd[0] != "powershell.exe" || !strings.Contains(strings.Join(win.Cmd, " "), "-ExecutionPolicy Bypass") {
		t.Fatalf("windows argv = %v", win.Cmd)
	}
	for _, want := range []string{"irm https://chatgpt.com/codex/install.ps1 | iex", "CODEX_NON_INTERACTIVE", "Programs\\OpenAI\\Codex\\bin\\codex.exe", "Open a new pane"} {
		if !strings.Contains(ps, want) {
			t.Fatalf("windows script missing %q: %s", want, ps)
		}
	}
	for _, spec := range []codexInstallCommandSpec{unix, win} {
		for _, banned := range []string{"CLI", "daemon", "sudo"} {
			if strings.Contains(spec.Note, banned) {
				t.Fatalf("install note uses %q: %q", banned, spec.Note)
			}
		}
	}
}

// A login shell that exits early (a profile with `exit`, as some CI images
// and broken dotfiles have) gives no verdict; detection then asks the binary
// directly instead of reporting an error.
func TestDetectCodexCLIFallsBackWhenLoginShellExitsEarly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("login-shell probe is Unix-only")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	if err := os.WriteFile(filepath.Join(home, ".bash_profile"), []byte("exit 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, ".local", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = --version ]; then echo 'codex-cli 9.9.9'; exit 0; fi\n" +
		"if [ \"$1\" = login ] && [ \"$2\" = status ]; then echo 'Logged in using an API key - sk-SECRET'; [ -f \"$HOME/signed-in\" ]; exit $?; fi\n" +
		"exit 2\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	res := detectCodexCLI(true)
	if res.outcome != codexProbeFound || res.version != "codex-cli 9.9.9" || res.loginOK {
		t.Fatalf("signed-out fallback = %+v", res)
	}
	if err := os.WriteFile(filepath.Join(home, "signed-in"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if res = detectCodexCLI(true); res.outcome != codexProbeFound || !res.loginOK {
		t.Fatalf("signed-in fallback = %+v", res)
	}
}
