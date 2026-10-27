package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestCodexLiveDefaultAndExplicitOptOut(t *testing.T) {
	dir := t.TempDir()
	if codexLiveEnabled(dir) != codexLiveSupported() {
		t.Fatal("supported new terminals must default to automatic handoff")
	}
	path := filepath.Join(dir, "codex_live_auth.json")
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"enabled":false}`, false}, {`{"enabled":true}`, true}, {`{}`, false}, {`broken`, false},
	} {
		if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
			t.Fatal(err)
		}
		if codexLiveEnabled(dir) != tc.want {
			t.Fatalf("settings %s not honored", tc.body)
		}
	}
}

func TestCodexLiveVersionPreflight(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{"codex-cli 0.154.0\n", true}, {"codex-cli 0.155.2", true}, {"codex-cli 1.0.0", true},
		{"codex-cli 0.153.9", false}, {"", false}, {"Welcome\ncodex-cli 0.154.0", false},
		{"codex-cli 0.154.0-alpha", false}, {"codex-cli 0.154.-1", false}, {"other 0.154.0", false},
	} {
		if codexLiveVersionSupported(tc.version) != tc.want {
			t.Fatalf("version %q", tc.version)
		}
	}
}

func TestCodexLiveRealLaunchPreflight(t *testing.T) {
	if os.Getenv("SOLU_TEST_REAL_CODEX_LAUNCH_PREFLIGHT") != "1" {
		t.Skip("opt-in read-only check of installed CLI and existing local configuration")
	}
	if !codexLiveLaunchReady([]string{"codex"}, nil) {
		t.Fatal("installed Codex login/configuration is not eligible for automatic handoff")
	}
}

func TestCodexLiveAutomaticConfigLeavesCustomTransportsAlone(t *testing.T) {
	for _, config := range []string{`model_provider = "custom"`, `profile = "private"`, `"cli_auth_credentials_store" = "keyring"`, `model_providers.openai.base_url = "http://custom"`, "[model_providers.openai]\nbase_url = 'http://custom'"} {
		if codexLiveAutomaticConfig([]byte(config)) {
			t.Fatal("custom configuration accepted")
		}
	}
	if !codexLiveAutomaticConfig([]byte("model = \"gpt-5.6-sol\"\n# model_provider = ignored\n[features]\napps = true\n")) {
		t.Fatal("ordinary model/features settings should retain automatic handoff")
	}
}

func TestCodexLiveAutomaticConfigAcceptsCodexUIPreferences(t *testing.T) {
	for _, config := range []string{
		"[tui.model_availability_nux]\n\"gpt-5.5\" = true\n\"gpt-5.6-sol\" = true\n",
		"[notice.model_migrations] # Codex-generated\n\"gpt-5.3-codex\" = \"gpt-5.5\"\n",
	} {
		if !codexLiveAutomaticConfig([]byte(config)) {
			t.Fatal("Codex-generated UI preferences disabled automatic handoff")
		}
	}
	for _, config := range []string{
		"\"model_provider\" = 'custom'\n[tui.model_availability_nux]\n\"gpt-5.5\" = true\n",
		"[tui.model_availability_nux]\n\"gpt-5.5\" = true\n[profiles.private]\nmodel_provider = 'custom'\n",
		"[tui.model_availability_nux]\n\"gpt-5.5\" = true\n[other]\n\"cli_auth_credentials_store\" = 'keyring'\n",
		"[tui.model_availability_nux]\n\"gpt-5.5\" = true\n[model_providers.openai]\nbase_url = 'http://custom'\n",
		"note = \"\"\"\n[tui.model_availability_nux]\n\"\"\"\n\"model_provider\" = 'custom'\n",
		"[tui.model_availability_nux]\n\"gpt-5.5\\u0022\" = true\n",
	} {
		if codexLiveAutomaticConfig([]byte(config)) {
			t.Fatal("UI preference exception bypassed custom transport guard")
		}
	}
}

func TestCodexLiveLaunchPreflightPreservesUnsupportedLogins(t *testing.T) {
	if !codexLiveSupported() {
		t.Skip("Linux live bridge")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, "codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho codex-cli 0.154.0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		var err error
		bin, err = exec.LookPath("codex")
		if err != nil {
			t.Skip("Windows preflight requires installed Codex executable")
		}
	}
	cmd := []string{bin}
	if codexLiveLaunchReady(cmd, nil) {
		t.Fatal("missing login must use ordinary CLI")
	}
	for _, auth := range [][]byte{[]byte(`{}`), []byte(`{"OPENAI_API_KEY":"fixture"}`), codexAuthJSON(t, "access", "refresh", "account")} {
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), auth, 0600); err != nil {
			t.Fatal(err)
		}
		want := strings.Contains(string(auth), "account")
		if codexLiveLaunchReady(cmd, nil) != want {
			t.Fatal("login readiness misclassified")
		}
	}
	for _, env := range [][]string{{"CODEX_HOME=/custom"}, {"OPENAI_API_KEY=fixture"}, {"OPENAI_BASE_URL=http://custom"}} {
		if codexLiveLaunchReady(cmd, env) {
			t.Fatal("custom auth launch was rewritten")
		}
	}
	resume := []string{bin, "resume", "01a09bd6-ef88-7d61-8155-e75654494511"}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[tui.model_availability_nux]\n\"gpt-5.5\" = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !codexLiveLaunchReady(cmd, nil) || !codexLiveLaunchReady(resume, nil) {
		t.Fatal("Codex UI preferences blocked a supported new or migrated terminal")
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("cli_auth_credentials_store = \"keyring\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if codexLiveLaunchReady(cmd, nil) || codexLiveLaunchReady(resume, nil) {
		t.Fatal("keyring login was rewritten")
	}
}

func TestCodexLiveMigrationPreservesLaunchPolicy(t *testing.T) {
	thread := "01a09bd6-ef88-7d61-8155-e75654494511"
	flags := []string{"-m", "gpt-5.6-sol", "--sandbox", "read-only", "-a", "on-request", "-c", "developer_instructions=keep exactly", "--no-alt-screen"}
	cmd := append([]string{"codex", "resume", thread}, flags...)
	for _, input := range [][]string{cmd, append([]string{"codex"}, flags...)} {
		got, ok := codexLiveResumeCommand(input, thread)
		if !ok || !slices.Equal(got, cmd) {
			t.Fatal("launch policy was not preserved")
		}
		if wrapped, ok := tryLoginShellWrap(input, "", nil); ok {
			got, ok := codexLiveResumeCommand(wrapped, thread)
			if !ok || !slices.Equal(got, cmd) {
				t.Fatal("wrapped launch policy was not preserved")
			}
			wrapped[2] += "; echo custom"
			if _, ok := codexLiveResumeCommand(wrapped, thread); ok {
				t.Fatal("custom shell side effects accepted")
			}
		}
	}
	for _, input := range [][]string{
		{"codex", "resume", thread, "do the previous work again"},
		{"codex", "--model", "some-model", "initial prompt"},
		{"codex", "resume", "--last"},
		{"codex", "--profile=custom"},
		{"codex", "-pprivate"},
		{"codex", "--config=model_provider = 'other'"},
		{"codex", "-c", "cli_auth_credentials_store = 'keyring'"},
		{"codex", "--image", "old-input.png"},
	} {
		if _, ok := codexLiveResumeCommand(input, thread); ok {
			t.Fatal("unsafe replay accepted")
		}
	}
	if codexLiveDefaultHome([]string{"CODEX_HOME=/custom"}) || !codexLiveDefaultHome([]string{"TERM=xterm"}) {
		t.Fatal("custom login home was not isolated")
	}
}

func TestCodexLiveMigrationPreservesDraftsAndBackgroundWork(t *testing.T) {
	footer := "\n\n Context 12% used · gpt-6-astra high · ~/work\n"
	for _, screen := range []string{"›" + footer, "› Ask Codex to do anything" + footer, "›⠁Ask Codex to do anything⡀" + footer} {
		if !codexLiveMigrationScreenSafe(screen) {
			t.Fatalf("empty composer rejected: %q", screen)
		}
	}
	for _, screen := range []string{"› unfinished draft" + footer, "›\nsecond draft line" + footer, "1 background terminal running\n›" + footer, "• Working (1m • esc to interrupt)\n›" + footer, "›\nunknown footer", "›" + footer + "user@host:~$"} {
		if codexLiveMigrationScreenSafe(screen) {
			t.Fatalf("unsafe migration accepted: %q", screen)
		}
	}
}
