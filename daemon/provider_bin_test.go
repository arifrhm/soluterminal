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

func writeProviderBinFixture(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestResolveProviderBinLayouts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("provider home layouts and login-shell resolution are Unix-only")
	}
	runtimes := []struct {
		id, name, absent string
		login, globs     bool
	}{
		{"claude-headless", "claude", "claude not found (PATH, ~/.local/bin, nvm, asdf, volta, login shell)", true, true},
		{"codex-headless", "codex", "codex not found (PATH, ~/.local/bin, nvm, asdf, volta, login shell)", true, true},
		{"gemini-headless", "gemini", "gemini not found (PATH, ~/.local/bin, nvm, asdf, volta, login shell)", true, true},
		// Muse has the login-shell fallback but no home glob (see muse.go).
		{"muse-headless", "muse", "muse not found (PATH, ~/.local/bin, asdf, volta, login shell)", true, false},
	}
	for _, rt := range runtimes {
		t.Run(rt.id, func(t *testing.T) {
			type binCase struct {
				name, location string
				login          bool
			}
			cases := []binCase{
				{"path", "path", false},
				{"local-bin", ".local/bin", false},
				{"asdf-shim", ".asdf/shims", false},
				{"volta", ".volta/bin", false},
				{"absent", "", false},
			}
			if rt.globs {
				cases = append(cases,
					binCase{"nvm", ".nvm/versions/node/v9.9.9/bin", false},
					binCase{"asdf-install", ".asdf/installs/nodejs/1.0/bin", false},
				)
			}
			if rt.login {
				cases = append(cases, binCase{"login-shell", "", true})
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					prepareWorkerBinLoginShellFallback(t)
					home := t.TempDir()
					pathDir := t.TempDir()
					t.Setenv("HOME", home)
					t.Setenv("PATH", pathDir)
					var want string
					if tc.location == "path" {
						want = filepath.Join(pathDir, rt.name)
						writeProviderBinFixture(t, want)
					} else if tc.location != "" {
						want = filepath.Join(home, filepath.FromSlash(tc.location), rt.name)
						writeProviderBinFixture(t, want)
					}
					if tc.login {
						want = filepath.Join(t.TempDir(), rt.name)
						writeProviderBinFixture(t, want)
						loginShellCommandOutput = func(_ context.Context, argv []string) ([]byte, error) {
							if !strings.Contains(argv[2], "command -v -- '"+rt.name+"'") {
								t.Fatalf("resolver script = %q", argv[2])
							}
							return []byte(want + "\n"), nil
						}
					} else if rt.login {
						loginShellCommandOutput = func(context.Context, []string) ([]byte, error) {
							return nil, errors.New("shell fixture miss")
						}
					} else {
						loginShellCommandOutput = func(context.Context, []string) ([]byte, error) {
							t.Fatal("muse resolver spawned a login shell")
							return nil, nil
						}
					}

					got, err := resolveProviderBin(rt.id)
					if tc.location == "" && !tc.login {
						if got != "" || err == nil || err.Error() != rt.absent {
							t.Fatalf("resolveProviderBin(%q) = %q, %v; want exact error %q", rt.id, got, err, rt.absent)
						}
						return
					}
					if err != nil || got != want {
						t.Fatalf("resolveProviderBin(%q) = %q, %v; want %q", rt.id, got, err, want)
					}
				})
			}
		})
	}
}

func TestResolveGeminiBinFromNVM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("provider home layouts are Unix-only")
	}
	prepareWorkerBinLoginShellFallback(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	want := filepath.Join(home, ".nvm", "versions", "node", "v24.2.0", "bin", "gemini")
	writeProviderBinFixture(t, want)
	if got, err := resolveGeminiBin(); err != nil || got != want {
		t.Fatalf("resolveGeminiBin()=(%q,%v), want (%q,nil)", got, err, want)
	}
}

// Muse declares no home glob, so nvm/asdf installs are not picked up directly;
// it resolves them through the login-shell fallback (parity with Claude/Codex).
func TestResolveMuseBinIgnoresGlobsAndUsesLoginShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("provider home layouts are Unix-only")
	}
	prepareWorkerBinLoginShellFallback(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	writeProviderBinFixture(t, filepath.Join(home, ".nvm", "versions", "node", "v9.9.9", "bin", "muse"))
	writeProviderBinFixture(t, filepath.Join(home, ".asdf", "installs", "nodejs", "1.0", "bin", "muse"))
	want := filepath.Join(t.TempDir(), "muse")
	writeProviderBinFixture(t, want)
	loginShellCommandOutput = func(_ context.Context, argv []string) ([]byte, error) {
		if !strings.Contains(argv[2], "command -v -- 'muse'") {
			t.Fatalf("resolver script = %q", argv[2])
		}
		return []byte(want + "\n"), nil
	}
	if got, err := resolveProviderBin("muse-headless"); err != nil || got != want {
		t.Fatalf("resolveProviderBin(muse-headless) = %q, %v; want %q", got, err, want)
	}
}

func TestResolveProviderBinUnknownRuntime(t *testing.T) {
	for _, id := range []string{"fake", "nope-headless"} {
		if _, err := resolveProviderBin(id); err == nil {
			t.Fatalf("resolveProviderBin(%q) unexpectedly succeeded", id)
		}
	}
}

func TestResolveMuseBinWithoutHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("HOME lookup semantics differ on Windows")
	}
	t.Setenv("HOME", "")
	t.Setenv("PATH", t.TempDir())
	if got, err := resolveProviderBin("muse-headless"); got != "" || err == nil || err.Error() != "muse not on PATH and no home dir" {
		t.Fatalf("resolveProviderBin(muse-headless) = %q, %v", got, err)
	}
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// writeNodeVersionShim writes a fake `node` that execs the node-shebang script
// it is handed under /bin/sh, tagging the interpreter so a test can prove which
// nvm version actually ran.
func writeNodeVersionShim(t *testing.T, path, tag string) {
	t.Helper()
	writeExecutable(t, path, "#!/bin/sh\nNODE_IMPL="+tag+" exec /bin/sh \"$@\"\n")
}

// TestProviderChildEnvNodeShebangUnderMinimalPath proves the actual defect: a
// codex resolved from an nvm bin directory is a `#!/usr/bin/env node` script,
// and with a minimal incoming PATH it must run with ITS OWN sibling node, not
// the differently resolved latest node the generic worker env carries.
func TestProviderChildEnvNodeShebangUnderMinimalPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("node-shebang and nvm layouts are POSIX-only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	// An empty PATH: no node anywhere except the version-manager layouts below.
	minimal := t.TempDir()
	t.Setenv("PATH", minimal)

	v18bin := filepath.Join(home, ".nvm", "versions", "node", "v18.0.0", "bin")
	v20bin := filepath.Join(home, ".nvm", "versions", "node", "v20.0.0", "bin")
	writeNodeVersionShim(t, filepath.Join(v18bin, "node"), "v18")
	writeNodeVersionShim(t, filepath.Join(v20bin, "node"), "v20")
	codex := filepath.Join(v18bin, "codex")
	writeExecutable(t, codex, "#!/usr/bin/env node\nprintf '%s\\n' \"ran-by-${NODE_IMPL:-none}\"\n")

	resolved, err := resolveCodexBin()
	if err != nil || resolved != codex {
		t.Fatalf("resolveCodexBin() = %q, %v; want %q", resolved, err, codex)
	}

	// The generic worker env resolves the LATEST node (v20); the provider-aware
	// helper must override that with the provider's own v18 sibling.
	env := providerChildEnvForBin(resolved, workerEnv())
	path := envPathOf(env)
	if !strings.HasPrefix(path, v18bin+string(os.PathListSeparator)) {
		t.Fatalf("provider child PATH = %q; want %q first", path, v18bin)
	}
	if strings.Count(path, v18bin) != 1 {
		t.Fatalf("provider dir duplicated on PATH: %q", path)
	}

	cmd := exec.Command(resolved)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node-shebang CLI failed under minimal PATH: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "ran-by-v18" {
		t.Fatalf("CLI ran under the wrong node: %q", out)
	}

	// Re-applying the helper must not duplicate the directory.
	env = providerChildEnvForBin(resolved, env)
	if got := strings.Count(envPathOf(env), v18bin); got != 1 {
		t.Fatalf("second application duplicated the provider dir: %d entries", got)
	}
}

// TestProviderChildEnvVoltaLayout proves the newly declared Volta bin dir is
// resolved and that its sibling node is used for a node-shebang CLI.
func TestProviderChildEnvVoltaLayout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("node-shebang and Volta layouts are POSIX-only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())

	voltaBin := filepath.Join(home, ".volta", "bin")
	writeNodeVersionShim(t, filepath.Join(voltaBin, "node"), "volta")
	codex := filepath.Join(voltaBin, "codex")
	writeExecutable(t, codex, "#!/usr/bin/env node\nprintf '%s\\n' \"ran-by-${NODE_IMPL:-none}\"\n")

	resolved, err := resolveCodexBin()
	if err != nil || resolved != codex {
		t.Fatalf("resolveCodexBin() = %q, %v; want %q", resolved, err, codex)
	}
	env := providerChildEnvForBin(resolved, []string{"PATH=" + t.TempDir()})
	cmd := exec.Command(resolved)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ran-by-volta" {
		t.Fatalf("Volta node-shebang CLI = %q, %v", out, err)
	}
}

func TestProviderChildEnvAsdfShimAndSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("node-shebang and asdf layouts are POSIX-only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())

	installBin := filepath.Join(home, ".asdf", "installs", "nodejs", "18.0.0", "bin")
	writeNodeVersionShim(t, filepath.Join(installBin, "node"), "asdf18")
	installed := filepath.Join(installBin, "codex")
	writeExecutable(t, installed, "#!/usr/bin/env node\nprintf '%s\\n' \"ran-by-${NODE_IMPL:-none}\"\n")
	shimDir := filepath.Join(home, ".asdf", "shims")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(installed, filepath.Join(shimDir, "codex")); err != nil {
		t.Fatal(err)
	}

	resolved, err := resolveCodexBin()
	if err != nil || resolved != filepath.Join(shimDir, "codex") {
		t.Fatalf("resolveCodexBin() = %q, %v; want asdf shim", resolved, err)
	}
	env := providerChildEnvForBin(resolved, []string{"PATH=" + t.TempDir()})
	if got := envPathOf(env); !strings.HasPrefix(got, installBin+string(os.PathListSeparator)) {
		t.Fatalf("symlink target's sibling node was not selected: PATH=%q", got)
	}
	cmd := exec.Command(resolved)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ran-by-asdf18" {
		t.Fatalf("asdf symlink CLI = %q, %v", out, err)
	}
}

// TestProviderChildEnvKeepsLocalBinWrapper proves the ~/.local/bin workaround
// keeps priority and keeps working: the resolver returns the wrapper, the
// helper leaves a plain shell wrapper's environment alone, and the wrapper can
// still prepend the nvm bin for the real CLI.
func TestProviderChildEnvKeepsLocalBinWrapper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("node-shebang and nvm layouts are POSIX-only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())

	v18bin := filepath.Join(home, ".nvm", "versions", "node", "v18.0.0", "bin")
	writeNodeVersionShim(t, filepath.Join(v18bin, "node"), "v18")
	realCodex := filepath.Join(v18bin, "codex")
	writeExecutable(t, realCodex, "#!/usr/bin/env node\nprintf '%s\\n' \"ran-by-${NODE_IMPL:-none}\"\n")

	wrapper := filepath.Join(home, ".local", "bin", "codex")
	writeExecutable(t, wrapper,
		"#!/bin/sh\nPATH=\""+v18bin+":$PATH\" exec node \""+realCodex+"\" \"$@\"\n")

	resolved, err := resolveCodexBin()
	if err != nil || resolved != wrapper {
		t.Fatalf("resolveCodexBin() = %q, %v; want the ~/.local/bin wrapper %q", resolved, err, wrapper)
	}
	base := []string{"PATH=" + t.TempDir()}
	if got := providerChildEnvForBin(resolved, base); !equalEnv(got, base) {
		t.Fatalf("a plain shell wrapper gained a PATH entry: %v", got)
	}

	// The wrapper itself must still make the real node-shebang CLI runnable.
	cmd := exec.Command(resolved)
	cmd.Env = base
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ran-by-v18" {
		t.Fatalf("~/.local/bin wrapper broke: %q, %v", out, err)
	}
}

// TestProviderChildEnvKeepsPathOrderWhenNodeAlreadyRuns proves a node-shebang
// CLI outside a version-manager layout does not hoist the resolver's node
// directory over a child PATH that can already run it.
func TestProviderChildEnvKeepsPathOrderWhenNodeAlreadyRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("node-shebang layouts are POSIX-only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	daemonNodeDir := t.TempDir()
	writeNodeVersionShim(t, filepath.Join(daemonNodeDir, "node"), "daemon")
	t.Setenv("PATH", daemonNodeDir)

	globalBin := t.TempDir()
	codex := filepath.Join(globalBin, "codex")
	writeExecutable(t, codex, "#!/usr/bin/env node\nprintf ok\n")

	childNodeDir := t.TempDir()
	writeNodeVersionShim(t, filepath.Join(childNodeDir, "node"), "child")
	base := []string{"PATH=" + globalBin + string(os.PathListSeparator) + childNodeDir}
	if got := providerChildEnvForBin(codex, base); !equalEnv(got, base) {
		t.Fatalf("child PATH that already runs node was reordered: %v", got)
	}

	// Without a node on the child's PATH the resolver's node is still added.
	bare := []string{"PATH=" + globalBin}
	if got := envPathOf(providerChildEnvForBin(codex, bare)); !strings.HasPrefix(got, daemonNodeDir+string(os.PathListSeparator)) {
		t.Fatalf("node-less child PATH = %q; want %q first", got, daemonNodeDir)
	}
}

// TestProviderChildEnvForCommandUsesSpawnLookupOnly proves the pane-start seam
// resolves a bare provider command exactly like the spawn that follows
// (exec.LookPath) and never falls back to home layouts the spawn cannot use.
func TestProviderChildEnvForCommandUsesSpawnLookupOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("node-shebang and nvm layouts are POSIX-only")
	}
	prepareWorkerBinLoginShellFallback(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	onPath := t.TempDir()
	t.Setenv("PATH", onPath)

	v18bin := filepath.Join(home, ".nvm", "versions", "node", "v18.0.0", "bin")
	writeNodeVersionShim(t, filepath.Join(v18bin, "node"), "v18")
	writeExecutable(t, filepath.Join(v18bin, "codex"), "#!/usr/bin/env node\nprintf ok\n")

	base := []string{"PATH=" + onPath}
	// codex is not on PATH: the spawn would fail, so the env is left alone even
	// though the full resolver would find the nvm install.
	if got := providerChildEnvForCommand("codex", base); !equalEnv(got, base) {
		t.Fatalf("bare command absent from PATH changed env: %v", got)
	}

	// Once it is on PATH (here via a symlink into the nvm install), its own
	// sibling node directory is prepended.
	if err := os.Symlink(filepath.Join(v18bin, "codex"), filepath.Join(onPath, "codex")); err != nil {
		t.Fatal(err)
	}
	if got := envPathOf(providerChildEnvForCommand("codex", base)); !strings.HasPrefix(got, v18bin+string(os.PathListSeparator)) {
		t.Fatalf("PATH = %q; want %q first", got, v18bin)
	}
	// A non-provider command is never touched.
	if got := providerChildEnvForCommand("bash", base); !equalEnv(got, base) {
		t.Fatalf("non-provider command changed env: %v", got)
	}
}

func envPathOf(env []string) string {
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			return strings.TrimPrefix(kv, "PATH=")
		}
	}
	return ""
}

func equalEnv(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// useWindowsProviderBinSeams makes resolveProviderBin take its Windows path on
// any host: a live-registry PATH lookup (fake) and a fake environment.
func useWindowsProviderBinSeams(t *testing.T, registry map[string]string, env map[string]string) {
	t.Helper()
	origGOOS, origFind, origGetenv := providerBinGOOS, providerBinFindOnWindowsPath, providerBinGetenv
	t.Cleanup(func() {
		providerBinGOOS, providerBinFindOnWindowsPath, providerBinGetenv = origGOOS, origFind, origGetenv
	})
	providerBinGOOS = "windows"
	providerBinFindOnWindowsPath = func(name string) (string, bool) {
		p, ok := registry[name]
		return p, ok
	}
	providerBinGetenv = func(k string) string { return env[k] }
}

// A Codex installed by install.ps1 after the daemon started lands only in the
// registry PATH (the daemon's own PATH is stale) and in Codex's own folder.
// Both must resolve without a restart, and the folder lookup must add .exe.
func TestResolveCodexBinWindowsFreshInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())

	t.Run("live registry PATH", func(t *testing.T) {
		want := filepath.Join(t.TempDir(), "codex.exe")
		useWindowsProviderBinSeams(t, map[string]string{"codex": want}, nil)
		if got, err := resolveCodexBin(); err != nil || got != want {
			t.Fatalf("resolveCodexBin() = %q, %v; want %q", got, err, want)
		}
	})

	t.Run("codex install folder", func(t *testing.T) {
		local := t.TempDir()
		want := filepath.Join(local, "Programs", "OpenAI", "Codex", "bin", "codex.exe")
		writeProviderBinFixture(t, want)
		useWindowsProviderBinSeams(t, nil, map[string]string{"LOCALAPPDATA": local})
		if got, err := resolveCodexBin(); err != nil || got != want {
			t.Fatalf("resolveCodexBin() = %q, %v; want %q", got, err, want)
		}
	})

	t.Run("install folder without LOCALAPPDATA", func(t *testing.T) {
		profile := t.TempDir()
		want := filepath.Join(profile, "AppData", "Local", "Programs", "OpenAI", "Codex", "bin", "codex.exe")
		writeProviderBinFixture(t, want)
		useWindowsProviderBinSeams(t, nil, map[string]string{"USERPROFILE": profile})
		if got, err := resolveCodexBin(); err != nil || got != want {
			t.Fatalf("resolveCodexBin() = %q, %v; want %q", got, err, want)
		}
	})

	t.Run("home .local/bin with .exe", func(t *testing.T) {
		want := filepath.Join(home, ".local", "bin", "codex.exe")
		writeProviderBinFixture(t, want)
		t.Cleanup(func() { _ = os.Remove(want) })
		useWindowsProviderBinSeams(t, nil, nil)
		if got, err := resolveCodexBin(); err != nil || got != want {
			t.Fatalf("resolveCodexBin() = %q, %v; want %q", got, err, want)
		}
	})

	t.Run("absent", func(t *testing.T) {
		useWindowsProviderBinSeams(t, nil, map[string]string{"LOCALAPPDATA": t.TempDir()})
		if got, err := resolveCodexBin(); err == nil {
			t.Fatalf("resolveCodexBin() = %q, want not found", got)
		}
	})
}

// The install folder is Codex-specific: other providers never pick up a
// codex.exe sitting there.
func TestResolveClaudeBinWindowsIgnoresCodexFolder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	local := t.TempDir()
	writeProviderBinFixture(t, filepath.Join(local, "Programs", "OpenAI", "Codex", "bin", "claude.exe"))
	useWindowsProviderBinSeams(t, nil, map[string]string{"LOCALAPPDATA": local})
	if got, err := resolveClaudeBin(); err == nil {
		t.Fatalf("resolveClaudeBin() = %q from the Codex folder", got)
	}
}
