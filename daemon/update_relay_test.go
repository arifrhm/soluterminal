package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// installerRig is an updater with a verified newer shell installer ready to
// download and a launcher that only counts.
func installerRig(t *testing.T) (*apiServer, *shellCountingRoundTripper, *int) {
	t.Helper()
	payload := []byte("verified newer installer")
	sum := sha256.Sum256(payload)
	url := "https://updates.example.test/setup.exe"
	u := NewUpdater("0.0.932", "0.0.900", "windows-amd64", nil, nil)
	u.homeDir = t.TempDir()
	u.manifest = &Manifest{
		Version: "0.0.932",
		Shell: &ManifestShell{Version: "0.0.932", Platforms: map[string]ManifestPlatform{
			"windows-amd64": {URL: url, SHA256: hex.EncodeToString(sum[:])},
		}},
	}
	transport := &shellCountingRoundTripper{responses: staticRoundTripper{url: payload}}
	u.httpClient = &http.Client{Transport: transport}
	u.shellDisplayPath = func(path, _ string) (string, error) { return path, nil }
	launches := 0
	u.launchShell = func(string, string, *shellLaunchLog) (launchResult, error) {
		launches++
		return launchResult{Method: "test", PID: "123", Session: "1"}, nil
	}
	return &apiServer{updater: u, dataDir: t.TempDir()}, transport, &launches
}

func assertInstallerRefused(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Fatalf("relayed response = %d %q, want 403", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["code"] != "installer_local_only" {
		t.Fatalf("relayed refusal body = %q (err %v), want code installer_local_only", rec.Body.String(), err)
	}
}

// A request that came through the cloud relay never launches an installer on
// this computer: not when the relay listener tagged it, and not when only the
// cloud's /m/<machine> prefix marks it. Nothing is downloaded either.
func TestUpdateApplyShellRefusedOverRelay(t *testing.T) {
	cases := map[string]func() (http.Handler, *http.Request){}
	a, transport, launches := installerRig(t)
	cases["relay listener"] = func() (http.Handler, *http.Request) {
		return markRelayTransport(http.HandlerFunc(a.updateApplyShell)),
			httptest.NewRequest(http.MethodPost, "/api/update/apply-shell", nil)
	}
	cases["relay listener, forced"] = func() (http.Handler, *http.Request) {
		return markRelayTransport(http.HandlerFunc(a.updateApplyShell)),
			httptest.NewRequest(http.MethodPost, "/api/update/apply-shell?force=1", nil)
	}
	cases["cloud prefix"] = func() (http.Handler, *http.Request) {
		req := httptest.NewRequest(http.MethodPost, "/api/update/apply-shell", nil)
		req.Header.Set("X-Forwarded-Prefix", "/m/machine-1")
		return http.HandlerFunc(a.updateApplyShell), req
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			h, req := mk()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			assertInstallerRefused(t, rec)
		})
	}
	if transport.calls != 0 || *launches != 0 {
		t.Fatalf("relayed request reached the installer: downloads=%d launches=%d", transport.calls, *launches)
	}
	if a.updater.shellApplying {
		t.Fatal("relayed request left shellApplying set")
	}

	// The same updater still launches for a request made on this computer.
	rec := httptest.NewRecorder()
	a.updateApplyShell(rec, httptest.NewRequest(http.MethodPost, "/api/update/apply-shell", nil))
	if rec.Code != http.StatusOK || transport.calls != 1 || *launches != 1 {
		t.Fatalf("local response = %d %q, downloads=%d launches=%d; want 200 with one launch", rec.Code, rec.Body.String(), transport.calls, *launches)
	}
}

// Revealing the staged installer opens a window on this computer, so it is
// local-only too.
func TestUpdateShellRevealRefusedOverRelay(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test explorer shim is a POSIX script")
	}
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "explorer.exe"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	u := NewUpdater("0.0.1", "0.0.1", "linux-amd64", nil, nil)
	u.mu.Lock()
	u.status.ShellLaunch = &ShellLaunchState{State: "launch-failed", StagedPath: `C:\Users\x\AppData\Local\soluterminal\shell-update\setup.exe`}
	u.shellLaunchLog = newShellLaunchLog(t.TempDir())
	u.mu.Unlock()
	a := &apiServer{updater: u}

	rec := httptest.NewRecorder()
	markRelayTransport(http.HandlerFunc(a.updateShellReveal)).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/update/shell/reveal", nil))
	assertInstallerRefused(t, rec)

	rec = httptest.NewRecorder()
	a.updateShellReveal(rec, httptest.NewRequest(http.MethodPost, "/api/update/shell/reveal", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("local reveal = %d %q, want 200", rec.Code, rec.Body.String())
	}
}
