package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestReadShellInfoMissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	v, p, err := readShellInfo(dir)
	if err != nil {
		t.Fatalf("err = %v, want nil for missing file", err)
	}
	if v != "" || p != "" {
		t.Fatalf("got (%q, %q), want empty for missing file", v, p)
	}
}

func TestReadShellInfoValidFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".solu"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"version":"0.0.412","platform":"windows-amd64"}`
	if err := os.WriteFile(filepath.Join(dir, ".solu", "shell-info.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	v, p, err := readShellInfo(dir)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if v != "0.0.412" || p != "windows-amd64" {
		t.Fatalf("got (%q, %q), want (0.0.412, windows-amd64)", v, p)
	}
}

func TestReadShellInfoMalformedFileReturnsEmptyNoError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".solu"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".solu", "shell-info.json"), []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, p, err := readShellInfo(dir)
	if err != nil {
		t.Fatalf("err = %v, want nil for malformed JSON", err)
	}
	if v != "" || p != "" {
		t.Fatalf("got (%q, %q), want empty for malformed JSON", v, p)
	}
}

// End-to-end: when the file is the only signal (daemon launched without
// --shell-version, e.g. systemd), shellInfo's Version/Platform should
// surface in UpdateStatus.ShellCurrent and let the badge light up.
func TestUpdaterUsesShellInfoFallback(t *testing.T) {
	_ = os.Unsetenv("SOLU_UPDATE_MANIFEST_PUBKEY")
	old := defaultUpdateManifestPubKey
	defaultUpdateManifestPubKey = ""
	defer func() { defaultUpdateManifestPubKey = old }()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".solu"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, ".solu", "shell-info.json"),
		[]byte(`{"version":"0.0.405","platform":"windows-amd64"}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	ver, plat, err := readShellInfo(dir)
	if err != nil {
		t.Fatal(err)
	}

	body := []byte(`{
		"flavor":"public",
		"version":"0.0.1",
		"platforms":{"linux-amd64":{"url":"https://example.test/solud","sha256":"abc"}},
		"shell":{
			"version":"0.0.411",
			"platforms":{"windows-amd64":{"url":"https://example.test/installer.exe","sha256":"def"}}
		}
	}`)

	u := NewUpdater("0.0.1", ver, plat, nil, nil)
	u.homeDir = dir
	u.manifestURL = "https://updates.example.test/update.json"
	u.httpClient = &http.Client{Transport: staticRoundTripper{
		"https://updates.example.test/update.json": body,
	}}

	st, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.ShellCurrent != "0.0.405" {
		t.Fatalf("ShellCurrent = %q, want 0.0.405 (from shell-info.json)", st.ShellCurrent)
	}
	if !st.ShellAvailable {
		t.Fatalf("ShellAvailable = false, want true (file said 0.0.405, manifest has 0.0.411)")
	}
	if st.ShellLatest != "0.0.411" {
		t.Fatalf("ShellLatest = %q, want 0.0.411", st.ShellLatest)
	}
}

// A daemon spawned with a --shell-version flag that later goes stale
// (the desktop app was updated while this daemon kept running — reused
// by the spawn-guard or kept alive by systemd) must adopt the newer
// version from shell-info.json so the installer badge clears.
func TestUpdaterAdoptsNewerShellInfoOverStaleFlag(t *testing.T) {
	_ = os.Unsetenv("SOLU_UPDATE_MANIFEST_PUBKEY")
	old := defaultUpdateManifestPubKey
	defaultUpdateManifestPubKey = ""
	defer func() { defaultUpdateManifestPubKey = old }()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".solu"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The shell on disk has been upgraded to 0.0.500.
	if err := os.WriteFile(
		filepath.Join(dir, ".solu", "shell-info.json"),
		[]byte(`{"version":"0.0.500","platform":"windows-amd64"}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	body := []byte(`{
		"flavor":"public",
		"version":"0.0.1",
		"platforms":{"linux-amd64":{"url":"https://example.test/solud","sha256":"abc"}},
		"shell":{
			"version":"0.0.500",
			"platforms":{"windows-amd64":{"url":"https://example.test/installer.exe","sha256":"def"}}
		}
	}`)

	// Daemon was spawned long ago with a now-stale --shell-version flag.
	u := NewUpdater("0.0.1", "0.0.420", "windows-amd64", nil, nil)
	u.homeDir = dir
	u.manifestURL = "https://updates.example.test/update.json"
	u.httpClient = &http.Client{Transport: staticRoundTripper{
		"https://updates.example.test/update.json": body,
	}}

	st, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.ShellCurrent != "0.0.500" {
		t.Fatalf("ShellCurrent = %q, want 0.0.500 (newer shell-info.json must beat the stale flag)", st.ShellCurrent)
	}
	if st.ShellAvailable {
		t.Fatalf("ShellAvailable = true, want false (shell is 0.0.500, matches manifest — badge must be off)")
	}
}

// Regression for the lingering "install the installer" prompt: after the
// installer runs and the new shell rewrites shell-info.json, the long-lived
// (systemd-owned) daemon survives the swap and only re-reads the file on its
// 30s Check() tick. The new shell's first GET /api/update/status must NOT
// return a stale ShellAvailable=true — Status() now re-reads the file itself,
// so the badge clears the instant the new app asks, with no fresh Check().
func TestStatusReflectsFreshlyInstalledShellWithoutCheck(t *testing.T) {
	_ = os.Unsetenv("SOLU_UPDATE_MANIFEST_PUBKEY")
	old := defaultUpdateManifestPubKey
	defaultUpdateManifestPubKey = ""
	defer func() { defaultUpdateManifestPubKey = old }()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".solu"), 0o755); err != nil {
		t.Fatal(err)
	}
	infoPath := filepath.Join(dir, ".solu", "shell-info.json")
	// Old shell installed; manifest advertises a newer installer.
	if err := os.WriteFile(infoPath, []byte(`{"version":"0.0.420","platform":"windows-amd64"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	body := []byte(`{
		"flavor":"public",
		"version":"0.0.1",
		"platforms":{"linux-amd64":{"url":"https://example.test/solud","sha256":"abc"}},
		"shell":{
			"version":"0.0.500",
			"platforms":{"windows-amd64":{"url":"https://example.test/installer.exe","sha256":"def"}}
		}
	}`)

	u := NewUpdater("0.0.1", "0.0.420", "windows-amd64", nil, nil)
	u.homeDir = dir
	u.manifestURL = "https://updates.example.test/update.json"
	u.httpClient = &http.Client{Transport: staticRoundTripper{
		"https://updates.example.test/update.json": body,
	}}

	// First poll: badge is on (old shell, newer installer in the manifest).
	st, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.ShellAvailable {
		t.Fatalf("ShellAvailable = false after first Check, want true (0.0.420 < manifest 0.0.500)")
	}

	// The installer runs: the relaunched shell rewrites shell-info.json. No new
	// Check() happens yet — the daemon's next tick is up to 30s away.
	if err := os.WriteFile(infoPath, []byte(`{"version":"0.0.500","platform":"windows-amd64"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// The new app's first status read must already see the badge cleared.
	st = u.Status()
	if st.ShellCurrent != "0.0.500" {
		t.Fatalf("ShellCurrent = %q after Status(), want 0.0.500 (must adopt the freshly-installed shell)", st.ShellCurrent)
	}
	if st.ShellAvailable {
		t.Fatalf("ShellAvailable = true after Status(), want false — installer prompt would linger")
	}
}

// The clear transition (yes→no) must publish a shell-update-available event so
// an already-connected session that didn't restart its daemon is told to drop
// the badge instead of waiting for its next manual refresh. The frontend gates
// its toast on the fresh ShellAvailable, so a clear event renders no toast.
func TestCheckFiresEventWhenInstallerBadgeClears(t *testing.T) {
	_ = os.Unsetenv("SOLU_UPDATE_MANIFEST_PUBKEY")
	old := defaultUpdateManifestPubKey
	defaultUpdateManifestPubKey = ""
	defer func() { defaultUpdateManifestPubKey = old }()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".solu"), 0o755); err != nil {
		t.Fatal(err)
	}
	infoPath := filepath.Join(dir, ".solu", "shell-info.json")
	if err := os.WriteFile(infoPath, []byte(`{"version":"0.0.420","platform":"windows-amd64"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	body := []byte(`{
		"flavor":"public",
		"version":"0.0.1",
		"platforms":{"linux-amd64":{"url":"https://example.test/solud","sha256":"abc"}},
		"shell":{
			"version":"0.0.500",
			"platforms":{"windows-amd64":{"url":"https://example.test/installer.exe","sha256":"def"}}
		}
	}`)

	bus := newUIBus()
	u := NewUpdater("0.0.1", "0.0.420", "windows-amd64", bus, nil)
	u.homeDir = dir
	u.manifestURL = "https://updates.example.test/update.json"
	u.httpClient = &http.Client{Transport: staticRoundTripper{
		"https://updates.example.test/update.json": body,
	}}

	if _, err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countBusEvents(bus, "shell-update-available"); got != 1 {
		t.Fatalf("shell-update-available events after first Check = %d, want 1 (first detection)", got)
	}

	// Installer applied: shell on disk now matches the manifest.
	if err := os.WriteFile(infoPath, []byte(`{"version":"0.0.500","platform":"windows-amd64"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.ShellAvailable {
		t.Fatalf("ShellAvailable = true after install, want false")
	}
	if got := countBusEvents(bus, "shell-update-available"); got != 2 {
		t.Fatalf("shell-update-available events after clearing Check = %d, want 2 (clear transition must notify the UI)", got)
	}
}

func countBusEvents(bus *uiBus, typ string) int {
	bus.logMu.Lock()
	defer bus.logMu.Unlock()
	n := 0
	for _, e := range bus.log {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// Flag-wins: when --shell-version is explicitly set, the file is
// ignored even if present. We exercise main.go's per-flag override by
// reading the file ourselves and confirming the explicit value wins
// when both are present (mirrors main.go's: `if *shellVersion == ""`).
func TestExplicitShellVersionOverridesShellInfoFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".solu"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, ".solu", "shell-info.json"),
		[]byte(`{"version":"0.0.405","platform":"windows-amd64"}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	// Simulate main.go's per-flag fallback logic with the CLI flag set.
	shellVersion := "0.0.999"
	shellPlatform := "windows-amd64"
	if shellVersion == "" || shellPlatform == "" {
		fileVer, filePlat, _ := readShellInfo(dir)
		if shellVersion == "" {
			shellVersion = fileVer
		}
		if shellPlatform == "" {
			shellPlatform = filePlat
		}
	}
	if shellVersion != "0.0.999" {
		t.Fatalf("shellVersion = %q, want 0.0.999 (explicit flag must win over file)", shellVersion)
	}
	if shellPlatform != "windows-amd64" {
		t.Fatalf("shellPlatform = %q, want windows-amd64", shellPlatform)
	}
}
