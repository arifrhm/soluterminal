package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"soluterminal-shared/providers"
)

func TestHarnessCommandNamesAreDaemonOnlyAndFailClosed(t *testing.T) {
	want := map[string]string{"deepseek": "deepseek", "glm": "zcode", "pi": "pi", "pi-glm": "pi-glm"}
	for _, slug := range providers.HarnessSlugs() {
		p, _ := providers.Get(slug)
		if got := harnessCommandName(p); got == "" || got != want[slug] {
			t.Fatalf("harness %q command = %q, want %q", slug, got, want[slug])
		}
		delete(want, slug)
	}
	if len(want) != 0 {
		t.Fatalf("command mapping contains unknown harnesses: %v", want)
	}
	for _, slug := range []string{"", "..", "../evil", "/tmp/evil", "new-provider", "claude"} {
		p := providers.Provider{Slug: slug, Harness: &providers.HarnessFacts{}}
		if harnessCommandName(p) != "" || writeHarnessCommand(p, "node", "launch", "data") == nil {
			t.Fatalf("unrecognized command accepted: %q", slug)
		}
	}
	if harnessCommandName(providers.Provider{Slug: "deepseek"}) != "" {
		t.Fatal("non-harness provider accepted")
	}
}

// TestWriteHarnessCommandSelfHeals locks the zero-touch shortcut behavior: a
// missing command is created pointing at the managed launcher, one of ours is
// refreshed on release, and an unrelated command of the same name is preserved.
func TestWriteHarnessCommandSelfHeals(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p, ok := providers.Get("deepseek")
	if !ok || p.Harness == nil {
		t.Fatal("deepseek provider missing")
	}
	launcher := "/data/harness-launchers/launch.mjs"
	if err := writeHarnessCommand(p, "/usr/bin/node", launcher, "/data"); err != nil {
		t.Fatal(err)
	}
	shortcut := filepath.Join(home, ".local", "bin", "deepseek")
	body, err := os.ReadFile(shortcut)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# soluterminal-managed-harness", "harness-shortcut.sh", launcher, "/usr/bin/node", "deepseek", "--data-dir", "/data"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("shortcut missing %q:\n%s", want, body)
		}
	}
	info, err := os.Stat(shortcut)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("mode = %v, want 0700", info.Mode().Perm())
	}
	// A release refresh rewrites our own shortcut to the new managed launcher.
	if err := writeHarnessCommand(p, "/usr/bin/node", "/data/harness-launchers/launch2.mjs", "/data"); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(shortcut)
	if !strings.Contains(string(body), "launch2.mjs") {
		t.Fatalf("shortcut not refreshed:\n%s", body)
	}
	// A foreign command is never clobbered.
	foreign := filepath.Join(home, ".local", "bin", "pi")
	if err := os.WriteFile(foreign, []byte("#!/bin/sh\necho mine\n"), 0755); err != nil {
		t.Fatal(err)
	}
	pi, _ := providers.Get("pi")
	if err := writeHarnessCommand(pi, "/usr/bin/node", launcher, "/data"); err != nil {
		t.Fatal(err)
	}
	kept, _ := os.ReadFile(foreign)
	if !strings.Contains(string(kept), "echo mine") {
		t.Fatalf("foreign shortcut was clobbered: %s", kept)
	}
}

// TestWriteHarnessCommandDoesNotStealAcrossDataDirs locks the side-by-side
// guard: one shortcut name is shared by every data dir on the machine, so a
// test/dev install must never repoint the command a user relies on to reach
// the real account, while the default data dir may always self-heal.
func TestWriteHarnessCommandDoesNotStealAcrossDataDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	prod := filepath.Join(home, ".solu")
	side := filepath.Join(home, ".solu-collaboration-test")
	p, ok := providers.Get("deepseek")
	if !ok || p.Harness == nil {
		t.Fatal("deepseek provider missing")
	}
	shortcut := filepath.Join(home, ".local", "bin", "deepseek")

	// The default data dir creates and owns the shortcut.
	if err := writeHarnessCommand(p, "/usr/bin/node", "/prod/launch.mjs", prod); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(shortcut)
	if !strings.Contains(string(body), prod) {
		t.Fatalf("default dir did not claim the shortcut:\n%s", body)
	}

	// A side-by-side data dir must leave it alone.
	if err := writeHarnessCommand(p, "/usr/bin/node", "/side/launch.mjs", side); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(shortcut)
	if !strings.Contains(string(body), prod) || strings.Contains(string(body), side) {
		t.Fatalf("side-by-side install stole the shortcut:\n%s", body)
	}

	// A side-by-side dir may refresh a shortcut that already points at it.
	owned := "#!/bin/sh\n# soluterminal-managed-harness\nexec /usr/bin/node '/side/launch.mjs' 'deepseek' --data-dir '" + side + "' \"$@\"\n"
	if err := os.WriteFile(shortcut, []byte(owned), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeHarnessCommand(p, "/usr/bin/node", "/side/launch2.mjs", side); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(shortcut)
	if !strings.Contains(string(body), "/side/launch2.mjs") {
		t.Fatalf("side-by-side install did not refresh its own shortcut:\n%s", body)
	}

	// The default data dir reclaims a shortcut a side-by-side install hijacked.
	if err := writeHarnessCommand(p, "/usr/bin/node", "/side/launch.mjs", side); err != nil {
		t.Fatal(err)
	}
	if err := writeHarnessCommand(p, "/usr/bin/node", "/prod/launch.mjs", prod); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(shortcut)
	if !strings.Contains(string(body), prod) {
		t.Fatalf("default dir did not reclaim a hijacked shortcut:\n%s", body)
	}
}

func TestManagedShortcutDataDir(t *testing.T) {
	cases := map[string]string{
		"exec node launch deepseek --data-dir '/home/u/.solu' \"$@\"": "/home/u/.solu",
		`exec node launch deepseek --data-dir "/home/u/.solu" "$@"`:   "/home/u/.solu",
		"exec node launch deepseek --data-dir /home/u/.solu \"$@\"":   "/home/u/.solu",
		"exec node launch deepseek \"$@\"":                            "",
	}
	for script, want := range cases {
		if got := managedShortcutDataDir(script); got != want {
			t.Errorf("managedShortcutDataDir(%q) = %q, want %q", script, got, want)
		}
	}
}

func TestHarnessRuntimeInstalledChecksGuard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p, _ := providers.Get("glm")
	root := filepath.Join(home, ".local", "share", "soluterminal", "harnesses", "zcode-terminal")
	entry := filepath.Join(root, "node_modules", "zcode-app-cli", "vendor", "zcode.cjs")
	if err := os.MkdirAll(filepath.Dir(entry), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if harnessRuntimeInstalled(p) {
		t.Fatal("install must not read as complete while the runtime guard is missing")
	}
	guard := filepath.Join(filepath.Dir(entry), "node_modules", "@zcode", "tui", "package.json")
	if err := os.MkdirAll(filepath.Dir(guard), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(guard, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if !harnessRuntimeInstalled(p) {
		t.Fatal("install should be complete once the guard exists")
	}
}

func TestHarnessInstallState(t *testing.T) {
	a := &apiServer{dataDir: t.TempDir()}
	if installing, errText := a.harnessInstallState("deepseek"); installing || errText != "" {
		t.Fatalf("initial state = %v/%q", installing, errText)
	}
	a.finishHarnessInstall("deepseek", errors.New("node missing"))
	if installing, errText := a.harnessInstallState("deepseek"); installing || errText != "node missing" {
		t.Fatalf("error state = %v/%q", installing, errText)
	}
	a.finishHarnessInstall("deepseek", nil)
	if installing, errText := a.harnessInstallState("deepseek"); installing || errText != "" {
		t.Fatalf("cleared state = %v/%q", installing, errText)
	}
}

// TestEnsureHarnessRuntimeSkipsInstalled proves auto-provisioning is a no-op
// once the runtime is on disk — it must never spawn npm or write a log.
func TestEnsureHarnessRuntimeSkipsInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	a := &apiServer{dataDir: t.TempDir()}
	p, _ := providers.Get("deepseek")
	lib := filepath.Join(home, ".local", "share", "soluterminal", "harnesses", "deepseek", "node_modules", "@deepseek-ai", "dsh", "lib")
	if err := os.MkdirAll(lib, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib, "bin.js"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if !harnessRuntimeInstalled(p) {
		t.Fatal("runtime should read as installed")
	}
	a.ensureHarnessRuntime("deepseek")
	if installing, _ := a.harnessInstallState("deepseek"); installing {
		t.Fatal("installed runtime must not start an install")
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, "harness-install-deepseek.log")); !os.IsNotExist(err) {
		t.Fatal("installed runtime must not create an install log")
	}
}

// TestGetHarnessAccountsDoesNotAutoInstallWhenDisabled guards the test/CI path:
// a plain apiServer leaves harnessAutoInstall off, so a status GET with a
// configured account must never spawn npm or write an install log.
func TestGetHarnessAccountsDoesNotAutoInstallWhenDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	a := &apiServer{dataDir: t.TempDir()}
	mux := http.NewServeMux()
	a.registerHarnessRoutes(mux)
	a.registerExtensionRoutes(mux)
	payload, _ := json.Marshal(harnessTestAccount("main", "key"))
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("PUT", "/api/harnesses/deepseek/accounts", strings.NewReader(string(payload))))
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest("GET", "/api/harnesses/deepseek", nil))
	if res.Code != 200 {
		t.Fatal(res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"installing":false`) {
		t.Fatalf("expected installing:false, got %s", res.Body.String())
	}
	entries, _ := os.ReadDir(a.dataDir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "harness-install-") {
			t.Fatalf("auto-install fired while disabled: %s", entry.Name())
		}
	}
}

// TestEnsureHarnessRuntimeAttemptsOnce proves the per-process bound: after one
// attempt, a provider with no runtime is not retried automatically.
func TestEnsureHarnessRuntimeAttemptsOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := &apiServer{dataDir: t.TempDir()}
	a.harnessInstallTried = map[string]bool{"deepseek": true}
	a.ensureHarnessRuntime("deepseek")
	if _, err := os.Stat(filepath.Join(a.dataDir, "harness-install-deepseek.log")); !os.IsNotExist(err) {
		t.Fatal("a provider already tried must not spawn another install")
	}
}

// TestManagedShortcutResolvesInterpreterAtRuntime locks the fix for a shortcut
// stranded by a moved or removed pinned interpreter: the installed command
// re-resolves Node at run time instead of failing on the install-time snapshot.
func TestManagedShortcutResolvesInterpreterAtRuntime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed harness shortcuts run on the WSL/Linux and macOS backends")
	}
	resolverBytes, err := harnessAssets.ReadFile("harness_assets/harness-shortcut.sh")
	if err != nil {
		t.Fatalf("embedded resolver: %v", err)
	}
	dir := t.TempDir()
	resolver := filepath.Join(dir, "harness-shortcut.sh")
	if err := os.WriteFile(resolver, resolverBytes, 0700); err != nil {
		t.Fatal(err)
	}
	// PATH for the resolver contains only the tools it needs, so `node` and
	// `bash` are genuinely absent and the resolution order is deterministic.
	tools := filepath.Join(dir, "tools")
	if err := os.MkdirAll(tools, 0755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"ls", "awk", "tail"} {
		src, lookErr := exec.LookPath(tool)
		if lookErr != nil {
			t.Fatalf("lookpath %s: %v", tool, lookErr)
		}
		if err := os.Symlink(src, filepath.Join(tools, tool)); err != nil {
			t.Fatal(err)
		}
	}
	launcher := filepath.Join(dir, "launch.mjs")
	out := filepath.Join(dir, "out")
	runResolver := func(pinned string, env []string) (string, int) {
		cmd := exec.Command("/bin/sh", resolver, pinned, launcher, "deepseek", "--data-dir", "/data", "prompt")
		cmd.Env = env
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		code := 0
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatalf("run resolver: %v", err)
		}
		return stderr.String(), code
	}

	// 1. The pinned interpreter is gone, but a Node is on PATH: resolve it and
	//    forward the launcher arguments unchanged.
	fakeBin := filepath.Join(dir, "fakebin")
	if err := os.MkdirAll(fakeBin, 0755); err != nil {
		t.Fatal(err)
	}
	fakeNode := filepath.Join(fakeBin, "node")
	fake := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + out + "\"\n"
	if err := os.WriteFile(fakeNode, []byte(fake), 0755); err != nil {
		t.Fatal(err)
	}
	if stderr, code := runResolver(filepath.Join(dir, "missing-node"), []string{"PATH=" + fakeBin + ":" + tools, "HOME=" + dir}); code != 0 {
		t.Fatalf("PATH fallback exit = %d, stderr = %q", code, stderr)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := launcher + "\ndeepseek\n--data-dir\n/data\nprompt\n"; string(got) != want {
		t.Fatalf("resolved command = %q, want %q", got, want)
	}

	// 2. A valid pinned interpreter still wins over PATH.
	pinned := filepath.Join(dir, "pinned-node")
	if err := os.WriteFile(pinned, []byte("#!/bin/sh\nprintf 'PINNED %s\\n' \"$@\" > \""+out+"\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if stderr, code := runResolver(pinned, []string{"PATH=" + fakeBin + ":" + tools, "HOME=" + dir}); code != 0 {
		t.Fatalf("pinned exit = %d, stderr = %q", code, stderr)
	}
	if got, _ = os.ReadFile(out); !strings.HasPrefix(string(got), "PINNED ") {
		t.Fatalf("pinned interpreter was not preferred: %q", got)
	}

	// 3. The newest nvm install wins over an older one, without a login shell.
	home := filepath.Join(dir, "home")
	for _, version := range []string{"v9.9.9", "v10.0.0"} {
		bin := filepath.Join(home, ".nvm", "versions", "node", version, "bin")
		if err := os.MkdirAll(bin, 0755); err != nil {
			t.Fatal(err)
		}
		script := "#!/bin/sh\nprintf 'NVM " + version + "\\n' > \"" + out + "\"\n"
		if err := os.WriteFile(filepath.Join(bin, "node"), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if stderr, code := runResolver(filepath.Join(dir, "missing-node"), []string{"PATH=" + tools, "HOME=" + home}); code != 0 {
		t.Fatalf("nvm fallback exit = %d, stderr = %q", code, stderr)
	}
	if got, _ = os.ReadFile(out); string(got) != "NVM v10.0.0\n" {
		t.Fatalf("newest nvm install = %q, want NVM v10.0.0", got)
	}

	// 4. An executable but below-floor pinned interpreter is skipped for the
	//    newer nvm install (the distro /usr/bin/node case).
	stale := filepath.Join(dir, "stale-node")
	if err := os.WriteFile(stale, []byte("#!/bin/sh\ncase \"$1\" in -e) exit 1 ;; esac\nprintf 'STALE\\n' > \""+out+"\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if stderr, code := runResolver(stale, []string{"PATH=" + tools, "HOME=" + home}); code != 0 {
		t.Fatalf("stale pin fallback exit = %d, stderr = %q", code, stderr)
	}
	if got, _ = os.ReadFile(out); string(got) != "NVM v10.0.0\n" {
		t.Fatalf("stale pin was not skipped for nvm = %q, want NVM v10.0.0", got)
	}

	// 5. Only a below-floor interpreter: name the version instead of "not found".
	stderr, code := runResolver(stale, []string{"PATH=" + tools, "HOME=" + filepath.Join(dir, "empty-home")})
	if code != 127 {
		t.Fatalf("stale-only exit = %d, want 127 (stderr %q)", code, stderr)
	}
	if !strings.Contains(stderr, "below the supported version") || !strings.Contains(stderr, "pinned interpreter") {
		t.Fatalf("stale-only stderr not actionable: %q", stderr)
	}

	// 6. Nothing usable anywhere: exit 127 with an actionable message, not a
	//    bare shell "not found".
	stderr, code = runResolver(filepath.Join(dir, "missing-node"), []string{"PATH=" + tools, "HOME=" + filepath.Join(dir, "empty-home")})
	if code != 127 {
		t.Fatalf("no-node exit = %d, want 127 (stderr %q)", code, stderr)
	}
	if !strings.Contains(stderr, "Node.js not found") || !strings.Contains(stderr, "pinned interpreter") {
		t.Fatalf("no-node stderr not actionable: %q", stderr)
	}
}

// TestHarnessActionRoutesThroughShortcutResolver locks the in-app install and
// launch commands to the shared resolver, so the provider's supported-Node gate
// picks the interpreter instead of resolveNodeBin's first-on-PATH choice.
func TestHarnessActionRoutesThroughShortcutResolver(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed harness shortcuts run on the WSL/Linux and macOS backends")
	}
	launcher := "/data/harness-launchers/launch.mjs"
	want := []string{"/bin/sh", "/data/harness-launchers/harness-shortcut.sh", "/usr/bin/node", launcher, "deepseek", "--data-dir", "/data", "--install"}
	if got := harnessShortcutCommand("/usr/bin/node", launcher, "deepseek", "/data", "--install"); !reflect.DeepEqual(got, want) {
		t.Fatalf("install argv = %q, want %q", got, want)
	}
	if got := harnessShortcutCommand("/usr/bin/node", launcher, "pi", "/data"); len(got) != 7 || got[0] != "/bin/sh" || got[len(got)-1] != "/data" {
		t.Fatalf("launch argv = %q, want the resolver shim with no extra arg", got)
	}
}
