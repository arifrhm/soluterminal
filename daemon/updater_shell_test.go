package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPruneShellInstallersKeepsTwoNewestAndLaunchLog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Now().UTC()
	paths := []string{
		filepath.Join(dir, "installer-old.exe"),
		filepath.Join(dir, "installer-middle.exe"),
		filepath.Join(dir, "installer-new.exe"),
	}
	for index, path := range paths {
		if err := os.WriteFile(path, []byte(path), 0o600); err != nil {
			t.Fatal(err)
		}
		at := now.Add(time.Duration(index) * time.Minute)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	launchLog := filepath.Join(dir, "launch.log")
	if err := os.WriteFile(launchLog, []byte("audit"), 0o600); err != nil {
		t.Fatal(err)
	}
	token := paths[0] + shellDowngradeTokenSuffix
	if err := os.WriteFile(token, []byte("single use"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pruneShellInstallers(dir, paths[2]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
		t.Fatalf("oldest installer survived: %v", err)
	}
	for _, path := range []string{paths[1], paths[2], launchLog, token} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("kept file %s: %v", path, err)
		}
	}
}

func TestPruneShellInstallersFewerThanTwo(t *testing.T) {
	t.Parallel()
	for _, count := range []int{0, 1} {
		count := count
		t.Run(fmt.Sprintf("count_%d", count), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			protectedPath := filepath.Join(dir, "installer.exe")
			if count == 1 {
				if err := os.WriteFile(protectedPath, []byte("installer"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := pruneShellInstallers(dir, protectedPath); err != nil {
				t.Fatal(err)
			}
			if count == 1 {
				if _, err := os.Stat(protectedPath); err != nil {
					t.Fatalf("only installer was not preserved: %v", err)
				}
			}
		})
	}
}

func shellApplyTestUpdater(t *testing.T) (*Updater, string, string) {
	t.Helper()
	payload := []byte("verified NSIS installer bytes")
	sum := sha256.Sum256(payload)
	url := "https://updates.example.test/soluterminal-setup.exe"
	u := NewUpdater("0.0.1", "0.0.1", "windows-amd64", nil, nil)
	u.manifest = &Manifest{
		Version: "0.0.2",
		Shell: &ManifestShell{
			Version: "0.0.2",
			Platforms: map[string]ManifestPlatform{
				"windows-amd64": {URL: url, SHA256: hex.EncodeToString(sum[:])},
			},
		},
	}
	u.httpClient = &http.Client{Transport: staticRoundTripper{url: payload}}
	u.homeDir = t.TempDir()
	u.shellDisplayPath = func(path, shellPlatform string) (string, error) { return path, nil }
	dataDir := t.TempDir()
	return u, dataDir, filepath.Join(dataDir, "shell-update", "soluterminal-setup.exe")
}

type shellCountingRoundTripper struct {
	responses staticRoundTripper
	calls     int
}

func (s *shellCountingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	s.calls++
	return s.responses.RoundTrip(req)
}

// CurrentShellVersion must report the *installed* shell from shell-info.json,
// not a stale --shell-version launch flag forwarded across daemon auto-update
// swaps. This is the 0.0.485(flag)-vs-0.0.621(installed) mismatch that made a
// current shell look stuck in /api/health.
func TestCurrentShellVersionPrefersFresherShellInfoFile(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".solu"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeInfo := func(v string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(home, ".solu", "shell-info.json"),
			[]byte(`{"version":"`+v+`","platform":"windows-amd64"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	u := NewUpdater("0.0.1", "0.0.485", "windows-amd64", nil, nil)
	u.homeDir = home

	// Fresher installed shell → report it, not the stale flag.
	writeInfo("0.0.621")
	if got := u.CurrentShellVersion(); got != "0.0.621" {
		t.Fatalf("CurrentShellVersion = %q, want 0.0.621 (installed file beats stale flag)", got)
	}

	// An older file must never downgrade the in-memory shellCurrent.
	writeInfo("0.0.400")
	if got := u.CurrentShellVersion(); got != "0.0.485" {
		t.Fatalf("CurrentShellVersion = %q, want 0.0.485 (older file must not downgrade)", got)
	}
}

// When solud is launched without --shell-version (dev runs of `./solud`,
// or pre-v0.0.330 Tauri shells), shellCurrent is empty. The detector
// must still light up the badge as long as our inferred shellPlatform
// matches one of the manifest's installer platforms — the platform
// match is what protects cloud Linux deployments from spuriously
// flagging shell updates.
func TestCheckShellAvailableWhenShellCurrentEmptyAndPlatformPresent(t *testing.T) {
	_ = os.Unsetenv("SOLU_UPDATE_MANIFEST_PUBKEY")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	old := defaultUpdateManifestPubKey
	defaultUpdateManifestPubKey = ""
	defer func() { defaultUpdateManifestPubKey = old }()

	body := []byte(`{
		"version":"0.0.1",
		"platforms":{"linux-amd64":{"url":"https://example.test/solud","sha256":"abc"}},
		"shell":{
			"version":"0.0.411",
			"platforms":{"windows-amd64":{"url":"https://example.test/installer.exe","sha256":"def"}}
		}
	}`)

	u := NewUpdater("0.0.1", "", "windows-amd64", nil, nil)
	u.manifestURL = "https://updates.example.test/update.json"
	u.httpClient = &http.Client{Transport: staticRoundTripper{
		"https://updates.example.test/update.json": body,
	}}

	st, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.ShellAvailable {
		t.Fatalf("ShellAvailable = false, want true (shellCurrent empty, manifest has windows-amd64)")
	}
	if st.ShellLatest != "0.0.411" {
		t.Fatalf("ShellLatest = %q, want 0.0.411", st.ShellLatest)
	}
}

// LastSuccessMs must advance only on a SUCCESSFUL check. LastCheckedMs
// advances on every attempt and Latest/Notes are last-known-good cache, so
// without a separate success timestamp a machine that hasn't reached the
// update server in days renders as "Up to date · checked 30s ago" (the
// dana support case, three screenshots running).
func TestCheckTracksLastSuccessSeparatelyFromLastChecked(t *testing.T) {
	_ = os.Unsetenv("SOLU_UPDATE_MANIFEST_PUBKEY")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	old := defaultUpdateManifestPubKey
	defaultUpdateManifestPubKey = ""
	defer func() { defaultUpdateManifestPubKey = old }()

	body := []byte(`{
		"version":"0.0.2",
		"platforms":{"linux-amd64":{"url":"https://example.test/solud","sha256":"abc"}}
	}`)
	u := NewUpdater("0.0.1", "", "linux-amd64", nil, nil)
	u.manifestURL = "https://updates.example.test/update.json"
	u.httpClient = &http.Client{Transport: staticRoundTripper{
		"https://updates.example.test/update.json": body,
	}}

	st, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.LastSuccessMs == 0 {
		t.Fatalf("LastSuccessMs = 0 after a successful check, want set")
	}
	if st.LastError != "" {
		t.Fatalf("LastError = %q after success, want empty", st.LastError)
	}
	successAt := st.LastSuccessMs

	// Manifest disappears (404 → non-transient, surfaces immediately). The
	// attempt timestamp advances; the success timestamp must NOT.
	u.httpClient = &http.Client{Transport: staticRoundTripper{}}
	st, err = u.Check(context.Background())
	if err == nil {
		t.Fatalf("expected check error on missing manifest")
	}
	if st.LastError == "" {
		t.Fatalf("LastError empty after failed check, want set")
	}
	if st.LastSuccessMs != successAt {
		t.Fatalf("LastSuccessMs = %d after failed check, want unchanged %d", st.LastSuccessMs, successAt)
	}
	if st.LastCheckedMs < successAt {
		t.Fatalf("LastCheckedMs = %d, want >= %d (attempts keep advancing)", st.LastCheckedMs, successAt)
	}
}

// Cloud Linux daemons whose inferred shellPlatform is linux-amd64
// must stay dark even when shellCurrent is empty — the manifest
// has no linux-amd64 installer entry, so platform-match fails.
func TestCheckShellUnavailableWhenPlatformMissing(t *testing.T) {
	_ = os.Unsetenv("SOLU_UPDATE_MANIFEST_PUBKEY")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	old := defaultUpdateManifestPubKey
	defaultUpdateManifestPubKey = ""
	defer func() { defaultUpdateManifestPubKey = old }()

	body := []byte(`{
		"version":"0.0.1",
		"platforms":{"linux-amd64":{"url":"https://example.test/solud","sha256":"abc"}},
		"shell":{
			"version":"0.0.411",
			"platforms":{"windows-amd64":{"url":"https://example.test/installer.exe","sha256":"def"}}
		}
	}`)

	u := NewUpdater("0.0.1", "", "linux-amd64", nil, nil)
	u.manifestURL = "https://updates.example.test/update.json"
	u.httpClient = &http.Client{Transport: staticRoundTripper{
		"https://updates.example.test/update.json": body,
	}}

	st, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.ShellAvailable {
		t.Fatalf("ShellAvailable = true, want false (no linux-amd64 in shell.platforms)")
	}
}

// When shellCurrent is populated and already at the latest version,
// no update should fire even if the platform matches.
func TestCheckShellUnavailableWhenAlreadyAtLatest(t *testing.T) {
	_ = os.Unsetenv("SOLU_UPDATE_MANIFEST_PUBKEY")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	old := defaultUpdateManifestPubKey
	defaultUpdateManifestPubKey = ""
	defer func() { defaultUpdateManifestPubKey = old }()

	body := []byte(`{
		"version":"0.0.1",
		"platforms":{"linux-amd64":{"url":"https://example.test/solud","sha256":"abc"}},
		"shell":{
			"version":"0.0.411",
			"platforms":{"windows-amd64":{"url":"https://example.test/installer.exe","sha256":"def"}}
		}
	}`)

	u := NewUpdater("0.0.1", "0.0.411", "windows-amd64", nil, nil)
	u.manifestURL = "https://updates.example.test/update.json"
	u.httpClient = &http.Client{Transport: staticRoundTripper{
		"https://updates.example.test/update.json": body,
	}}

	st, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.ShellAvailable {
		t.Fatalf("ShellAvailable = true, want false (already at 0.0.411)")
	}
}

// Keep the historical test name while pinning the replacement policy that
// superseded the unsafe daemon-first sequencing rule.
func TestApplyShellDaemonFirstGuard(t *testing.T) {
	t.Run("refuses when the installer would leave the daemon behind", func(t *testing.T) {
		u := NewUpdater("0.0.3", "0.0.2", "windows-amd64", nil, nil)
		u.homeDir = t.TempDir()
		u.manifest = &Manifest{
			Version: "0.0.3",
			Shell: &ManifestShell{Version: "0.0.2", Platforms: map[string]ManifestPlatform{
				"windows-amd64": {URL: "https://example.test/installer.exe", SHA256: "abc"},
			}},
		}
		_, err := u.ApplyShell(context.Background(), t.TempDir(), false)
		var refusal *shellDaemonDowngradeError
		if !errors.As(err, &refusal) {
			t.Fatalf("ApplyShell error = %v, want typed downgrade refusal", err)
		}
		if strings.Contains(err.Error(), "must be applied first") {
			t.Fatalf("ApplyShell retained the unsafe daemon-first refusal: %v", err)
		}
	})

	t.Run("allows when the installer carries the latest daemon", func(t *testing.T) {
		u := NewUpdater("0.0.1", "0.0.1", "windows-amd64", nil, nil)
		u.homeDir = t.TempDir()
		u.manifest = &Manifest{
			Version: "0.0.2",
			Shell: &ManifestShell{Version: "0.0.2", Platforms: map[string]ManifestPlatform{
				"windows-amd64": {URL: "https://example.test/installer.exe", SHA256: "abc"},
			}},
		}
		u.httpClient = &http.Client{Transport: staticRoundTripper{}}
		_, err := u.ApplyShell(context.Background(), t.TempDir(), false)
		var refusal *shellDaemonDowngradeError
		if errors.As(err, &refusal) || (err != nil && strings.Contains(err.Error(), "must be applied first")) {
			t.Fatalf("ApplyShell refused a monotonic same-release installer: %v", err)
		}
	})

	t.Run("admits an installer newer than the installed daemon even when a daemon-only publish is newer still", func(t *testing.T) {
		u := NewUpdater("0.0.1", "0.0.1", "windows-amd64", nil, nil)
		u.homeDir = t.TempDir()
		u.manifest = &Manifest{
			Version: "0.0.3",
			Shell: &ManifestShell{Version: "0.0.2", Platforms: map[string]ManifestPlatform{
				"windows-amd64": {URL: "https://example.test/installer.exe", SHA256: "abc"},
			}},
		}
		u.httpClient = &http.Client{Transport: staticRoundTripper{}}
		_, err := u.ApplyShell(context.Background(), t.TempDir(), false)
		var refusal *shellDaemonDowngradeError
		if errors.As(err, &refusal) || (err != nil && strings.Contains(err.Error(), "must be applied first")) {
			t.Fatalf("ApplyShell refused a monotonic carried installer because manifest.version was newer: %v", err)
		}
	})
}

// A daemon-only publish intentionally has manifest.version newer than the
// carried shell.version. The carried shell version is also the bundled daemon
// version, so admission must compare 0.0.916 with what is actually installed.
// This is the exact Fakhrul incident: the old daemon-first flow applied 0.0.932
// and then launched the carried 0.0.916 installer, which silently overwrote it.
func TestApplyShellMonotonicAdmissionMatrix(t *testing.T) {
	payload := []byte("verified NSIS installer 0.0.916")
	sum := sha256.Sum256(payload)
	url := "https://updates.example.test/soluterminal-0.0.916-setup.exe"

	cases := []struct {
		name          string
		currentDaemon string
		currentShell  string
		force         bool
		wantRefused   bool
		wantToken     bool
	}{
		{name: "fresh install", currentDaemon: "", currentShell: ""},
		{name: "same-version reinstall", currentDaemon: "0.0.916", currentShell: "0.0.916"},
		{name: "newer-available upgrade", currentDaemon: "0.0.900", currentShell: "0.0.900"},
		{name: "0.0.932 to carried 0.0.916 is refused", currentDaemon: "0.0.932", currentShell: "0.0.916", wantRefused: true},
		{name: "forced 0.0.932 to 0.0.916 downgrade", currentDaemon: "0.0.932", currentShell: "0.0.916", force: true, wantToken: true},
		{name: "dev daemon still respects newer installed shell", currentDaemon: "dev", currentShell: "0.0.932", wantRefused: true},
		{name: "dev daemon without installed shell is admitted", currentDaemon: "dev", currentShell: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := NewUpdater(tc.currentDaemon, tc.currentShell, "windows-amd64", nil, nil)
			u.homeDir = t.TempDir()
			u.manifest = &Manifest{
				Version: "0.0.932",
				Shell: &ManifestShell{
					Version: "0.0.916",
					Platforms: map[string]ManifestPlatform{
						"windows-amd64": {URL: url, SHA256: hex.EncodeToString(sum[:])},
					},
				},
			}
			transport := &shellCountingRoundTripper{responses: staticRoundTripper{url: payload}}
			u.httpClient = &http.Client{Transport: transport}
			u.shellDisplayPath = func(path, _ string) (string, error) { return path, nil }
			launches := 0
			u.launchShell = func(path, shellPlatform string, lg *shellLaunchLog) (launchResult, error) {
				launches++
				return launchResult{Method: "test", PID: "123", Session: "1"}, nil
			}
			var audit string
			u.shellLogf = func(format string, args ...any) { audit = fmt.Sprintf(format, args...) }
			dataDir := t.TempDir()
			u.mu.Lock()
			u.status.ShellLaunch = &ShellLaunchState{State: "launch-failed", StagedPath: "stale"}
			u.mu.Unlock()

			_, err := u.ApplyShell(context.Background(), dataDir, tc.force)
			if tc.wantRefused {
				var refusal *shellDaemonDowngradeError
				if !errors.As(err, &refusal) {
					t.Fatalf("ApplyShell error = %v, want typed downgrade refusal", err)
				}
				if transport.calls != 0 || launches != 0 {
					t.Fatalf("refusal happened after side effects: downloads=%d launches=%d", transport.calls, launches)
				}
				if got := u.Status().ShellLaunch; got != nil {
					t.Fatalf("refusal retained stale ShellLaunch: %+v", got)
				}
				if u.shellApplying {
					t.Fatal("refusal left shellApplying true")
				}
				for _, want := range []string{"0.0.932", "0.0.916", "?force=1"} {
					if !strings.Contains(audit, want) {
						t.Fatalf("refusal audit did not name %s: %q", want, audit)
					}
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("refusal error did not name %s: %v", want, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("ApplyShell returned %v", err)
			}
			if transport.calls != 1 || launches != 1 {
				t.Fatalf("admitted attempt: downloads=%d launches=%d, want 1/1", transport.calls, launches)
			}
			if got := u.Status().ShellLaunch; got == nil || got.State != "launched" || !strings.Contains(got.StagedPath, "soluterminal-0.0.916-setup.exe") {
				t.Fatalf("admitted attempt did not replace stale ShellLaunch: %+v", got)
			}
			if u.shellApplying {
				t.Fatal("admitted attempt left shellApplying true")
			}
			token := filepath.Join(dataDir, "shell-update", "soluterminal-0.0.916-setup.exe"+shellDowngradeTokenSuffix)
			_, tokenErr := os.Stat(token)
			if tc.wantToken && tokenErr != nil {
				t.Fatalf("forced attempt did not create downgrade token after verification: %v", tokenErr)
			}
			if !tc.wantToken && !os.IsNotExist(tokenErr) {
				t.Fatalf("normal attempt left a downgrade token: %v", tokenErr)
			}
		})
	}
}

func TestApplyShellDowngradeRefusalClearsStaleLaunchRecovery(t *testing.T) {
	u, dataDir, staged := shellApplyTestUpdater(t)
	u.current = "0.0.932"
	u.shellCurrent = "0.0.900"
	u.manifest.Version = "0.0.932"
	u.manifest.Shell.Version = "0.0.916"
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("previously staged installer"), 0o700); err != nil {
		t.Fatal(err)
	}
	u.mu.Lock()
	u.status.ShellLaunch = &ShellLaunchState{
		State: "launch-failed", StagedPath: staged, Error: "previous launch failed",
	}
	u.mu.Unlock()

	_, err := u.ApplyShell(context.Background(), dataDir, false)
	var refusal *shellDaemonDowngradeError
	if !errors.As(err, &refusal) {
		t.Fatalf("ApplyShell error = %v, want typed downgrade refusal", err)
	}
	if got := u.Status().ShellLaunch; got != nil {
		t.Fatalf("downgrade refusal retained stale launch recovery: %+v", got)
	}
	if path, revealErr := u.revealStagedInstaller(); revealErr == nil || path != "" {
		t.Fatalf("refused installer remained revealable: path=%q err=%v", path, revealErr)
	}
}

func TestApplyShellTokenCleanupFailureQuarantinesStaleInstaller(t *testing.T) {
	u, dataDir, staged := shellApplyTestUpdater(t)
	u.current = "0.0.932"
	u.shellCurrent = "0.0.900"
	u.manifest.Version = "0.0.932"
	u.manifest.Shell.Version = "0.0.916"
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("stale authorized installer"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A non-empty directory at the token path deterministically makes Remove
	// fail while leaving the adjacent installer removable. This models Windows
	// temporarily denying deletion of an abandoned single-use token.
	tokenPath := staged + shellDowngradeTokenSuffix
	if err := os.Mkdir(tokenPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tokenPath, "locked"), []byte("lock"), 0o600); err != nil {
		t.Fatal(err)
	}
	u.mu.Lock()
	u.status.ShellLaunch = &ShellLaunchState{
		State: "launch-failed", StagedPath: staged, Error: "previous launch failed",
	}
	u.mu.Unlock()
	var audit string
	u.shellLogf = func(format string, args ...any) { audit = fmt.Sprintf(format, args...) }

	_, err := u.ApplyShell(context.Background(), dataDir, false)
	var refusal *shellDaemonDowngradeError
	if err == nil || errors.As(err, &refusal) || !strings.Contains(err.Error(), "stale shell downgrade token") {
		t.Fatalf("ApplyShell error = %v, want fail-closed token cleanup error before policy refusal", err)
	}
	if got := u.Status().ShellLaunch; got != nil {
		t.Fatalf("cleanup failure retained stale launch recovery: %+v", got)
	}
	if _, statErr := os.Stat(staged); !os.IsNotExist(statErr) {
		t.Fatalf("authorized installer survived failed token cleanup: %v", statErr)
	}
	if u.shellApplying {
		t.Fatal("cleanup failure left shellApplying true")
	}
	if !strings.Contains(audit, tokenPath) {
		t.Fatalf("cleanup audit did not name token path %q: %q", tokenPath, audit)
	}
}

func TestUpdateApplyShellDowngradeRefusalAndForce(t *testing.T) {
	payload := []byte("verified old installer")
	sum := sha256.Sum256(payload)
	url := "https://updates.example.test/setup.exe"
	u := NewUpdater("0.0.932", "0.0.916", "windows-amd64", nil, nil)
	u.homeDir = t.TempDir()
	u.manifest = &Manifest{
		Version: "0.0.932",
		Shell: &ManifestShell{Version: "0.0.916", Platforms: map[string]ManifestPlatform{
			"windows-amd64": {URL: url, SHA256: hex.EncodeToString(sum[:])},
		}},
	}
	transport := &shellCountingRoundTripper{responses: staticRoundTripper{url: payload}}
	u.httpClient = &http.Client{Transport: transport}
	u.shellDisplayPath = func(path, _ string) (string, error) { return path, nil }
	launches := 0
	u.launchShell = func(path, shellPlatform string, lg *shellLaunchLog) (launchResult, error) {
		launches++
		return launchResult{Method: "test", PID: "123", Session: "1"}, nil
	}
	a := &apiServer{updater: u, dataDir: t.TempDir()}

	refused := httptest.NewRecorder()
	a.updateApplyShell(refused, httptest.NewRequest(http.MethodPost, "/api/update/apply-shell", nil))
	if refused.Code != http.StatusConflict || transport.calls != 0 || launches != 0 {
		t.Fatalf("default response = %d %q, downloads=%d launches=%d", refused.Code, refused.Body.String(), transport.calls, launches)
	}
	var body map[string]any
	if err := json.Unmarshal(refused.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "shell_daemon_downgrade_refused" || body["installedVersion"] != "0.0.932" || body["installerVersion"] != "0.0.916" || !strings.Contains(fmt.Sprint(body["hint"]), "?force=1") {
		t.Fatalf("structured refusal = %#v", body)
	}

	nonExact := httptest.NewRecorder()
	a.updateApplyShell(nonExact, httptest.NewRequest(http.MethodPost, "/api/update/apply-shell?force=1&automatic=1", nil))
	if nonExact.Code != http.StatusConflict || transport.calls != 0 || launches != 0 {
		t.Fatalf("non-exact force query bypassed refusal: response=%d downloads=%d launches=%d", nonExact.Code, transport.calls, launches)
	}

	forced := httptest.NewRecorder()
	a.updateApplyShell(forced, httptest.NewRequest(http.MethodPost, "/api/update/apply-shell?force=1", nil))
	if forced.Code != http.StatusOK || transport.calls != 1 || launches != 1 {
		t.Fatalf("forced response = %d %q, downloads=%d launches=%d", forced.Code, forced.Body.String(), transport.calls, launches)
	}
}

// recomputeShellLocked reports the informational manifest relationship: true
// when the installer's bundled daemon (== its version) is not older than
// latest; false for an intentional daemon-only release ahead of the installer.
func TestShellCarriesLatestDaemon(t *testing.T) {
	mk := func(daemonLatest, shellLatest string) *Updater {
		u := NewUpdater("0.0.1", "0.0.1", "windows-amd64", nil, nil)
		u.manifest = &Manifest{
			Version: daemonLatest,
			Shell: &ManifestShell{
				Version:   shellLatest,
				Platforms: map[string]ManifestPlatform{"windows-amd64": {URL: "u", SHA256: "s"}},
			},
		}
		u.mu.Lock()
		u.recomputeShellLocked()
		u.mu.Unlock()
		return u
	}
	if !mk("0.0.2", "0.0.2").status.ShellCarriesLatestDaemon {
		t.Fatal("same-version release: want ShellCarriesLatestDaemon=true")
	}
	if !mk("0.0.2", "0.0.3").status.ShellCarriesLatestDaemon {
		t.Fatal("installer ahead of daemon: want true (still not behind)")
	}
	if mk("0.0.3", "0.0.2").status.ShellCarriesLatestDaemon {
		t.Fatal("daemon-only release ahead of installer: want false")
	}
}

// The WMI courier must not just confirm a PID — it must reject the invisible
// session-0 launch (exit 12) so launchWindowsInstaller falls back to a visible
// explorer launch. This guards the "installer launched but nothing appears"
// Surface symptom from silently regressing if the courier is ever simplified.
func TestWMICreateCommandRejectsInvisibleSession0(t *testing.T) {
	cmd := wmiCreateCommand(`C:\Users\x y\AppData\soluterminal-setup.exe`)
	for _, want := range []string{
		"Win32_Process",   // still uses the job-escaping WMI launch
		"ReturnValue",     // still verifies the Create succeeded
		"SessionId -eq 0", // detects the invisible non-interactive session
		"Stop-Process",    // kills the orphaned invisible process
		"exit 12",         // signals launchWindowsInstaller to use explorer
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("wmiCreateCommand missing %q; got:\n%s", want, cmd)
		}
	}
	// A path with a space must survive intact inside the single-quoted PS string.
	if !strings.Contains(cmd, `soluterminal-setup.exe`) {
		t.Fatalf("wmiCreateCommand dropped the installer path; got:\n%s", cmd)
	}
}

func TestApplyShellLaunchConfirmed(t *testing.T) {
	u, dataDir, staged := shellApplyTestUpdater(t)
	u.launchShell = func(path, shellPlatform string, lg *shellLaunchLog) (launchResult, error) {
		lg.event("dispatch", map[string]string{"path": path, "method": "explorer"})
		lg.event("verify", map[string]string{"path": path, "method": "explorer", "pid": "123", "session": "1"})
		return launchResult{Method: "explorer", PID: "123", Session: "1"}, nil
	}

	got, err := u.ApplyShell(context.Background(), dataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != staged {
		t.Fatalf("ApplyShell path = %q, want %q", got, staged)
	}
	status := u.Status()
	if status.ShellApplying {
		t.Fatal("ShellApplying remained true after ApplyShell returned")
	}
	if status.ShellLaunch == nil || status.ShellLaunch.State != "launched" || status.ShellLaunch.Method != "explorer" || status.ShellLaunch.PID != "123" {
		t.Fatalf("ShellLaunch = %+v, want confirmed explorer pid 123", status.ShellLaunch)
	}
	logBody, err := os.ReadFile(filepath.Join(dataDir, "shell-update", "launch.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{`"event":"staged"`, `"event":"launched"`} {
		if !strings.Contains(string(logBody), event) {
			t.Fatalf("launch.log missing %s:\n%s", event, logBody)
		}
	}
}

func TestApplyShellRefusesHeldInstallerBeforeStagingOrLaunch(t *testing.T) {
	u, dataDir, staged := shellApplyTestUpdater(t)
	store, _ := newUpdateRollbackStore(dataDir)
	now := time.Now()
	if err := store.SetHold(rollbackHold{Version: u.manifest.Shell.Version}, now); err != nil {
		t.Fatal(err)
	}
	u.rollback = store
	launched := false
	u.launchShell = func(string, string, *shellLaunchLog) (launchResult, error) {
		launched = true
		return launchResult{}, nil
	}
	for _, force := range []bool{false, true} {
		_, err := u.ApplyShell(context.Background(), dataDir, force)
		var held *updateHeldError
		if !errors.As(err, &held) {
			t.Fatalf("force=%v error=%v, want held", force, err)
		}
	}
	if launched {
		t.Fatal("held shell installer launched")
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("held installer was staged: %v", err)
	}
	if _, err := os.Stat(staged + shellDowngradeTokenSuffix); !os.IsNotExist(err) {
		t.Fatalf("held installer wrote downgrade token: %v", err)
	}
	recorder := httptest.NewRecorder()
	(&apiServer{updater: u, dataDir: dataDir}).updateApplyShell(recorder, httptest.NewRequest(http.MethodPost, "/api/update/apply-shell?force=1", nil))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), `"code":"update_held"`) {
		t.Fatalf("held shell response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestApplyShellLaunchFailedSurfacesStagedPath(t *testing.T) {
	u, dataDir, staged := shellApplyTestUpdater(t)
	calls := 0
	u.launchShell = func(path, shellPlatform string, lg *shellLaunchLog) (launchResult, error) {
		calls++
		if calls == 1 {
			return launchResult{}, fmt.Errorf("%w: test did not observe a process", errLaunchNotObserved)
		}
		return launchResult{Method: "explorer", PID: "456", Session: "1"}, nil
	}

	_, err := u.ApplyShell(context.Background(), dataDir, false)
	if err == nil || !strings.Contains(err.Error(), staged) || !strings.Contains(err.Error(), "open it manually") {
		t.Fatalf("ApplyShell error = %v, want staged path and manual-open recovery", err)
	}
	status := u.Status()
	if status.ShellApplying || status.ShellLaunch == nil || status.ShellLaunch.State != "launch-failed" || status.ShellLaunch.StagedPath != staged || status.ShellLaunch.Error == "" {
		t.Fatalf("status after launch failure = %+v", status)
	}
	logBody, err := os.ReadFile(filepath.Join(dataDir, "shell-update", "launch.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logBody), `"event":"launch-failed"`) {
		t.Fatalf("launch.log missing launch-failed event:\n%s", logBody)
	}
	if _, err := u.ApplyShell(context.Background(), dataDir, false); err != nil {
		t.Fatalf("second ApplyShell was not admitted: %v", err)
	}
	if got := u.Status().ShellLaunch; got == nil || got.State != "launched" || got.PID != "456" {
		t.Fatalf("second attempt did not overwrite launch state: %+v", got)
	}
}

func TestVerifyInstallerCommandShape(t *testing.T) {
	cmd := verifyInstallerCommand(`C:\Users\x\it's setup.exe`)
	for _, want := range []string{
		`Name='it''s setup.exe'`,
		"SessionId -ne 0",
		"consent.exe",
		"exit 20",
		"exit 21",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("verifyInstallerCommand missing %q; got:\n%s", want, cmd)
		}
	}
}

func TestVerifyInstallerCommandRuntimeSessionOutcomes(t *testing.T) {
	if runtime.GOOS != "windows" && (runtime.GOOS != "linux" || !isInsideWSL()) {
		t.Skip("requires native Windows or Windows PowerShell interop from WSL")
	}
	psExe, err := resolvePowerShell()
	if err != nil {
		t.Skipf("PowerShell unavailable: %v", err)
	}
	if _, err := runTestPowerShellCommand(t, t.Context(), psExe, "-NoProfile", "-NonInteractive", "-Command", "exit 0"); err != nil {
		t.Skipf("Windows PowerShell interop unavailable: %v", err)
	}
	base := verifyInstallerCommand(`C:\\Users\\x\\setup.exe`)
	base = strings.Replace(base, "$i -lt 20", "$i -lt 1", 1)
	base = strings.Replace(base, "Start-Sleep -Milliseconds 500", "Start-Sleep -Milliseconds 1", 1)
	cases := []struct {
		name       string
		mockBody   string
		wantExit   int
		wantOutput string
	}{
		{name: "interactive", mockBody: `if($Filter -eq "Name='setup.exe'"){ [pscustomobject]@{ProcessId=321;SessionId=1} }`, wantExit: 0, wantOutput: "pid=321 session=1"},
		{name: "session zero", mockBody: `if($Filter -eq "Name='setup.exe'"){ [pscustomobject]@{ProcessId=654;SessionId=0} }`, wantExit: 21, wantOutput: "SessionId 0"},
		{name: "uac", mockBody: `if($Filter -eq "Name='consent.exe'"){ [pscustomobject]@{ProcessId=987;SessionId=1} }`, wantExit: 0, wantOutput: "pid=987 session=uac"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := `function Get-CimInstance { param([string]$ClassName,[string]$Filter,[string]$ErrorAction); ` + tc.mockBody + ` }; `
			out, runErr := runTestPowerShellCommand(t, t.Context(), psExe, "-NoProfile", "-NonInteractive", "-Command", mock+base)
			exitCode := 0
			if runErr != nil {
				var exitErr *exec.ExitError
				if !errors.As(runErr, &exitErr) {
					t.Fatal(runErr)
				}
				exitCode = exitErr.ExitCode()
			}
			if exitCode != tc.wantExit || !strings.Contains(string(out), tc.wantOutput) {
				t.Fatalf("verifier runtime = exit %d, output %q; want exit %d containing %q", exitCode, out, tc.wantExit, tc.wantOutput)
			}
		})
	}
}

func TestVerifyInstallerWaitsForTerminalInvisibleVerdict(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake PowerShell executable is a POSIX script")
	}
	fakePS := filepath.Join(t.TempDir(), "slow-powershell")
	if err := os.WriteFile(fakePS, []byte("#!/bin/sh\nsleep 16\necho 'installer observed only in SessionId 0' >&2\nexit 21\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, session, err := verifyInstallerVisible(`C:\\Users\\x\\setup.exe`, false, fakePS)
	if !errors.Is(err, errLaunchInvisible) || session != "0" {
		t.Fatalf("slow terminal verifier result = session %q, err %v; want SessionId 0 / errLaunchInvisible instead of an outer-context timeout", session, err)
	}
}

func TestNativeExplorerDispatchPrecedesPowerShellAvailabilityGuard(t *testing.T) {
	body, err := os.ReadFile("updater_shell.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	start := strings.Index(source, "func launchWindowsInstaller(")
	end := strings.Index(source[start:], "\nfunc parseLaunchResult(")
	if start < 0 || end < 0 {
		t.Fatal("could not locate launchWindowsInstaller source")
	}
	fn := source[start : start+end]
	dispatch := strings.Index(fn, "launchViaExplorer(winPath, false")
	guard := strings.Index(fn, "if psErr != nil")
	if dispatch < 0 || guard < 0 || dispatch > guard {
		t.Fatalf("native explorer dispatch must be attempted before the PowerShell availability guard; function was:\n%s", fn)
	}
}

func TestShellLaunchLogAppendsAndRotates(t *testing.T) {
	dir := t.TempDir()
	lg := newShellLaunchLog(dir)
	lg.event("staged", map[string]string{"path": "setup.exe"})
	lg.event("dispatch", map[string]string{"method": "explorer"})
	body, err := os.ReadFile(lg.path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 2 {
		t.Fatalf("launch.log lines = %d, want 2", len(lines))
	}
	for _, line := range lines {
		var entry map[string]string
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("invalid JSONL line %q: %v", line, err)
		}
		if entry["ts"] == "" || entry["event"] == "" {
			t.Fatalf("missing structured fields in %v", entry)
		}
	}
	if err := os.WriteFile(lg.path, []byte(strings.Repeat("x", int(shellLaunchLogMaxBytes+1))), 0o644); err != nil {
		t.Fatal(err)
	}
	lg.event("launched", map[string]string{"pid": "123"})
	if info, err := os.Stat(lg.path + ".1"); err != nil || info.Size() <= shellLaunchLogMaxBytes {
		t.Fatalf("rotated launch.log.1 missing or too small: info=%v err=%v", info, err)
	}
	rotatedBody, err := os.ReadFile(lg.path)
	if err != nil {
		t.Fatal(err)
	}
	var entry map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(rotatedBody))), &entry); err != nil || entry["event"] != "launched" {
		t.Fatalf("fresh launch.log after rotation = %q, err=%v", rotatedBody, err)
	}
}

func TestApplyShellForceFailEnv(t *testing.T) {
	t.Setenv("SOLU_SHELL_LAUNCH_FORCE_FAIL", "1")
	u, dataDir, staged := shellApplyTestUpdater(t)
	_, err := u.ApplyShell(context.Background(), dataDir, false)
	if err == nil || !strings.Contains(err.Error(), staged) {
		t.Fatalf("ApplyShell force-fail error = %v, want staged path", err)
	}
	if got := u.Status().ShellLaunch; got == nil || got.State != "launch-failed" || got.StagedPath != staged {
		t.Fatalf("force-fail ShellLaunch = %+v", got)
	}
}

func TestUpdateApplyShellHandlerReportsStagedLaunchFailure(t *testing.T) {
	u, dataDir, staged := shellApplyTestUpdater(t)
	u.launchShell = func(path, shellPlatform string, lg *shellLaunchLog) (launchResult, error) {
		return launchResult{}, fmt.Errorf("%w: handler test", errLaunchNotObserved)
	}
	a := &apiServer{updater: u, dataDir: dataDir}
	rec := httptest.NewRecorder()
	a.updateApplyShell(rec, httptest.NewRequest(http.MethodPost, "/api/update/apply-shell", nil))
	if rec.Code < 500 || !strings.Contains(rec.Body.String(), staged) || !strings.Contains(rec.Body.String(), "open it manually") {
		t.Fatalf("apply-shell response = %d %q, want 5xx with staged recovery path", rec.Code, rec.Body.String())
	}

	statusRec := httptest.NewRecorder()
	a.updateStatus(statusRec, httptest.NewRequest(http.MethodGet, "/api/update/status", nil))
	var status UpdateStatus
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.ShellLaunch == nil || status.ShellLaunch.State != "launch-failed" || status.ShellLaunch.StagedPath != staged {
		t.Fatalf("status response ShellLaunch = %+v", status.ShellLaunch)
	}

	statusRec = httptest.NewRecorder()
	a.updateStatus(statusRec, httptest.NewRequest(http.MethodGet, "/api/update/status", nil))
	var second UpdateStatus
	if err := json.Unmarshal(statusRec.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if second.ShellLaunch == nil || second.ShellLaunch.State != "launch-failed" {
		t.Fatalf("launch failure did not persist across status reads: %+v", second.ShellLaunch)
	}
}

func TestUpdateShellRevealHandler(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test explorer shim is a POSIX script")
	}
	binDir := t.TempDir()
	explorer := filepath.Join(binDir, "explorer.exe")
	if err := os.WriteFile(explorer, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	logDir := t.TempDir()
	staged := `C:\Users\x\AppData\Local\soluterminal\shell-update\setup.exe`
	u := NewUpdater("0.0.1", "0.0.1", "linux-amd64", nil, nil)
	u.mu.Lock()
	u.status.ShellLaunch = &ShellLaunchState{State: "launch-failed", StagedPath: staged}
	u.shellLaunchLog = newShellLaunchLog(logDir)
	u.mu.Unlock()
	a := &apiServer{updater: u}

	rec := httptest.NewRecorder()
	a.updateShellReveal(rec, httptest.NewRequest(http.MethodPost, "/api/update/shell/reveal", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("reveal response = %d %q, want 200", rec.Code, rec.Body.String())
	}
	var body struct {
		OK   bool   `json:"ok"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !body.OK || body.Path != staged {
		t.Fatalf("reveal body = %+v err=%v", body, err)
	}
	logBody, err := os.ReadFile(filepath.Join(logDir, "launch.log"))
	if err != nil || !strings.Contains(string(logBody), `"event":"reveal"`) {
		t.Fatalf("reveal event missing: body=%q err=%v", logBody, err)
	}

	// With Explorer resolution broken, the error remains actionable by naming
	// the already staged path.
	t.Setenv("PATH", t.TempDir())
	rec = httptest.NewRecorder()
	a.updateShellReveal(rec, httptest.NewRequest(http.MethodPost, "/api/update/shell/reveal", nil))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), staged) {
		t.Fatalf("failed reveal response = %d %q, want path-bearing 500", rec.Code, rec.Body.String())
	}
}
