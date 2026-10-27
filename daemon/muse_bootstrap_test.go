package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestResolveMuseAuthed pins the sign-in signal: a valid live auth.json
// envelope OR META_API_KEY counts; unlike Claude there is no macOS Keychain
// caveat (Muse keeps its login in a file on every platform), so no darwin
// exception.
func TestResolveMuseAuthed(t *testing.T) {
	cases := []struct {
		name                 string
		installed, cred, key bool
		want                 bool
	}{
		{"not installed", false, true, true, false},
		{"oauth cred", true, true, false, true},
		{"api key only", true, false, true, true},
		{"neither", true, false, false, false},
	}
	for _, c := range cases {
		if got := resolveMuseAuthed(c.installed, c.cred, c.key); got != c.want {
			t.Fatalf("%s: resolveMuseAuthed(%v,%v,%v)=%v, want %v",
				c.name, c.installed, c.cred, c.key, got, c.want)
		}
	}
}

// TestMuseAPIKeyMarkerDoesNotPolluteVersion: the key marker is printed BEFORE
// the version fence; version parsing must be unaffected whether or not it's
// present, and the two markers must be distinct.
func TestMuseAPIKeyMarkerDoesNotPolluteVersion(t *testing.T) {
	out := museAPIKeyMarker + museVersionMarker + "Muse Code 1.3.0\n"
	if got := versionAfterMarker(out, museVersionMarker); got != "Muse Code 1.3.0" {
		t.Fatalf("version with key marker = %q", got)
	}
	if !strings.Contains(out, museAPIKeyMarker) {
		t.Fatal("key marker not detectable in probe output")
	}
	if strings.Contains(museVersionMarker, museAPIKeyMarker) ||
		strings.Contains(museAPIKeyMarker, museVersionMarker) {
		t.Fatal("markers must not contain each other")
	}
}

// TestMuseInstallCommandForOS: the installer spec points at the official
// installer with a disclosure string. Unix yields a spawnable argv sourcing
// the shared init (so the post-install shell resolves ~/.local/bin/muse);
// Windows host-native is unsupported (Muse publishes a macOS/Linux
// installer only) but must still disclose the official command.
func TestMuseInstallCommandForOS(t *testing.T) {
	spec := museInstallCommandForOS()
	if spec.Display == "" {
		t.Fatal("install spec has empty display string (no disclosure)")
	}
	if !strings.Contains(spec.Display, "https://dev.meta.ai/install.sh") {
		t.Fatalf("install spec display must name the official installer; got %+v", spec)
	}
	if runtime.GOOS == "windows" {
		if spec.Supported {
			t.Fatalf("windows install must be unsupported; got %+v", spec)
		}
	} else {
		if !spec.Supported {
			t.Fatalf("install not supported on test platform %s/%s", runtime.GOOS, runtime.GOARCH)
		}
		if len(spec.Cmd) == 0 || spec.Cmd[0] != "bash" {
			t.Fatalf("unix install spec wrong: %+v", spec)
		}
		if joined := strings.Join(spec.Cmd, " "); !strings.Contains(joined, "$HOME/.local/bin") {
			t.Fatalf("unix install cmd missing ~/.local/bin PATH wiring: %s", joined)
		}
	}
}

// TestCurrentMuseStatusDefault: before the first probe, status is unchecked
// (UI shows "checking…", not a premature "not installed").
func TestCurrentMuseStatusDefault(t *testing.T) {
	a := &apiServer{}
	st := a.currentMuseStatus()
	if st.checked || st.installed || st.authed || st.version != "" {
		t.Fatalf("zero-value status should be unchecked/empty; got %+v", st)
	}
}

// TestClassifyMuseProbeOutput pins the outcome mapping: version marker →
// found, absent marker → definitively absent, neither → error (the shell died
// before answering — NO verdict).
func TestClassifyMuseProbeOutput(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantOutcome museProbeOutcome
		wantVersion string
	}{
		{"found with version", museVersionMarker + "Muse Code 1.3.0\n", museProbeFound, "Muse Code 1.3.0"},
		{"found, --version died before printing", museVersionMarker, museProbeFound, ""},
		{"found behind login banner", "Welcome to Ubuntu\n" + museVersionMarker + "Muse Code 1.3.0", museProbeFound, "Muse Code 1.3.0"},
		{"definitively absent", museAbsentMarker, museProbeAbsent, ""},
		{"absent behind login banner", "MOTD noise\n" + museAbsentMarker, museProbeAbsent, ""},
		{"shell died — no marker at all", "Welcome to Ubuntu\npartial noise", museProbeError, ""},
		{"empty output", "", museProbeError, ""},
		{"key marker alone is not an answer", museAPIKeyMarker, museProbeError, ""},
	}
	for _, c := range cases {
		outcome, version := classifyMuseProbeOutput(c.in)
		if outcome != c.wantOutcome || version != c.wantVersion {
			t.Fatalf("%s: classifyMuseProbeOutput(%q) = (%v, %q), want (%v, %q)",
				c.name, c.in, outcome, version, c.wantOutcome, c.wantVersion)
		}
	}
}

// TestCorroborateMuseAbsent: only a hard contradiction (binary on disk, or a
// live process with no binary) downgrades a completed "absent" to no-verdict;
// found/error pass through, and a full miss stays absent.
func TestCorroborateMuseAbsent(t *testing.T) {
	yes := func() bool { return true }
	no := func() bool { return false }
	if got := corroborateMuseAbsent(museProbeFound, yes, yes); got != museProbeFound {
		t.Fatalf("found must pass through; got %v", got)
	}
	if got := corroborateMuseAbsent(museProbeError, yes, yes); got != museProbeError {
		t.Fatalf("error must pass through; got %v", got)
	}
	if got := corroborateMuseAbsent(museProbeAbsent, yes, no); got != museProbeError {
		t.Fatalf("on-disk binary must downgrade absent; got %v", got)
	}
	if got := corroborateMuseAbsent(museProbeAbsent, no, yes); got != museProbeError {
		t.Fatalf("live process must downgrade absent; got %v", got)
	}
	if got := corroborateMuseAbsent(museProbeAbsent, no, no); got != museProbeAbsent {
		t.Fatalf("full miss must stay absent; got %v", got)
	}
}

// TestCorroborateMuseAbsentIsLazy: the disk/process checks run only for an
// absent outcome — found/error must not touch the filesystem or /proc.
func TestCorroborateMuseAbsentIsLazy(t *testing.T) {
	called := false
	spy := func() bool { called = true; return true }
	corroborateMuseAbsent(museProbeFound, spy, spy)
	corroborateMuseAbsent(museProbeError, spy, spy)
	if called {
		t.Fatal("corroboration checks must be lazy (not evaluated for found/error)")
	}
}

// TestMuseBinaryUnderHome: the disk cross-check covers the manifest's
// HomeSubdirs (~/.local/bin, .asdf/shims) and nothing node-managed.
func TestMuseBinaryUnderHome(t *testing.T) {
	home := t.TempDir()
	if museBinaryUnderHome(home) {
		t.Fatal("empty home reported a muse binary")
	}
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bin, "muse"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !museBinaryUnderHome(home) {
		t.Fatal("~/.local/bin/muse not detected")
	}
}

// TestRefreshMuseStatusAbsentOverwritesOnlyWhenDurable: absents inside the
// window must not flip a confirmed install; past the window the flip lands.
func TestRefreshMuseStatusAbsentOverwritesOnlyWhenDurable(t *testing.T) {
	a := &apiServer{}
	a.museStat = museStatus{checked: true, installed: true, version: "Muse Code 1.3.0", authed: true}
	a.detectMuse = func() (museProbeOutcome, string, bool) { return museProbeAbsent, "", false }
	clock := time.Unix(1_750_000_000, 0)
	a.nowFn = func() time.Time { return clock }

	for i := 0; i < museAbsentConfirmProbes; i++ {
		a.refreshMuseStatus()
		if st := a.currentMuseStatus(); !st.installed {
			t.Fatalf("absent #%d inside the window flipped a confirmed install: %+v", i+1, st)
		}
		clock = clock.Add(15 * time.Second)
	}

	clock = clock.Add(museAbsentConfirmWindow)
	a.refreshMuseStatus()
	st := a.currentMuseStatus()
	if !st.checked || st.installed || st.version != "" || st.authed {
		t.Fatalf("durable absence not applied: %+v", st)
	}
}

// A found probe between absents resets the hysteresis — repeated healed
// windows must never accumulate into a false flip.
func TestRefreshMuseStatusFoundResetsAbsentStreak(t *testing.T) {
	a := &apiServer{}
	a.museStat = museStatus{checked: true, installed: true, version: "Muse Code 1.3.0"}
	clock := time.Unix(1_750_000_000, 0)
	a.nowFn = func() time.Time { return clock }

	outcome := museProbeAbsent
	a.detectMuse = func() (museProbeOutcome, string, bool) { return outcome, "", false }

	for cycle := 0; cycle < 3; cycle++ {
		outcome = museProbeAbsent
		for i := 0; i < museAbsentConfirmProbes-1; i++ {
			a.refreshMuseStatus()
			clock = clock.Add(museAbsentConfirmWindow)
		}
		outcome = museProbeFound
		a.refreshMuseStatus()
	}
	if st := a.currentMuseStatus(); !st.installed {
		t.Fatalf("repeated healed absent episodes accumulated into a flip: %+v", st)
	}
}

// The hysteresis protects only a CONFIRMED install: the FIRST definitive
// absent must land immediately so onboarding can guide a new user.
func TestRefreshMuseStatusFirstAbsentVerdictImmediate(t *testing.T) {
	a := &apiServer{}
	a.detectMuse = func() (museProbeOutcome, string, bool) { return museProbeAbsent, "", false }

	a.refreshMuseStatus()
	st := a.currentMuseStatus()
	if !st.checked || st.installed {
		t.Fatalf("first-ever absent verdict was damped: %+v", st)
	}
}

// A probe error carries no information: the cached verdict (including authed)
// survives it wholesale.
func TestRefreshMuseStatusErrorKeepsVerdict(t *testing.T) {
	a := &apiServer{}
	a.museStat = museStatus{checked: true, installed: true, version: "Muse Code 1.3.0", authed: true}
	a.detectMuse = func() (museProbeOutcome, string, bool) { return museProbeError, "", false }

	a.refreshMuseStatus()
	if st := a.currentMuseStatus(); !st.installed || !st.authed || st.version != "Muse Code 1.3.0" {
		t.Fatalf("probe error overwrote the cached verdict: %+v", st)
	}
}

// TestMuseProbeInterval pins the adaptive ticker cadence.
func TestMuseProbeInterval(t *testing.T) {
	a := &apiServer{}
	if got := a.museProbeInterval(); got != museProbeIntervalMissing {
		t.Fatalf("unchecked/missing interval = %v, want %v", got, museProbeIntervalMissing)
	}
	a.museStat = museStatus{checked: true, installed: true}
	if got := a.museProbeInterval(); got != museProbeIntervalPresent {
		t.Fatalf("present interval = %v, want %v", got, museProbeIntervalPresent)
	}
	a.museAbsentStreak = 1
	if got := a.museProbeInterval(); got != museProbeIntervalSuspect {
		t.Fatalf("suspect interval = %v, want %v", got, museProbeIntervalSuspect)
	}
}

// Found with an empty version keeps the last known version rather than
// blanking the UI; a fresh version replaces it.
func TestRefreshMuseStatusFoundVersionBestEffort(t *testing.T) {
	a := &apiServer{}
	a.museStat = museStatus{checked: true, installed: true, version: "Muse Code 1.3.0"}
	a.detectMuse = func() (museProbeOutcome, string, bool) { return museProbeFound, "", false }
	a.refreshMuseStatus()
	if st := a.currentMuseStatus(); st.version != "Muse Code 1.3.0" {
		t.Fatalf("empty version blanked the UI: %+v", st)
	}
	a.detectMuse = func() (museProbeOutcome, string, bool) { return museProbeFound, "Muse Code 1.4.0", false }
	a.refreshMuseStatus()
	if st := a.currentMuseStatus(); st.version != "Muse Code 1.4.0" {
		t.Fatalf("fresh version not adopted: %+v", st)
	}
}
