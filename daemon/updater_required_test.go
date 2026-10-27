package main

// Tests for mandatory-minimum ("required update") enforcement — the
// min_daemon_version / min_shell_version floors in the signed control file.
// The load-bearing invariants:
//   - versionBelowFloor exempts empty/"dev" currents (a `go run` daemon must
//     never be permanently flagged) and an empty floor means no floor,
//   - a below-floor machine BYPASSES the staged-rollout cohort (a canary
//     percentage must not strand machines the operator ordered to update)
//     but paused/halt_* still always win,
//   - Check() derives Required/ShellRequired from the same effective control
//     as the apply gate, fails OPEN (no control → never required), and fires
//     the bus on transitions in BOTH directions,
//   - a freshly-installed shell clears ShellRequired on the next Status()
//     read (recomputeShellLocked), not the next 30s Check tick.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionBelowFloor(t *testing.T) {
	cases := []struct {
		current, floor string
		want           bool
	}{
		{"0.0.700", "0.0.800", true},      // below → required
		{"0.0.800", "0.0.800", false},     // at the floor → fine
		{"0.0.801", "0.0.800", false},     // above → fine
		{"0.0.700", "", false},            // no floor
		{"", "0.0.800", false},            // unknown current → exempt
		{"dev", "0.0.800", false},         // dev build → exempt (would parse as 0)
		{"0.0.9", "0.0.10", true},         // numeric, not lexicographic
		{"0.0.700-beta", "0.0.800", true}, // prerelease segment still compares
	}
	for _, tc := range cases {
		if got := versionBelowFloor(tc.current, tc.floor); got != tc.want {
			t.Errorf("versionBelowFloor(%q, %q) = %v, want %v", tc.current, tc.floor, got, tc.want)
		}
	}
}

func TestControlRolloutBypassWhenBelowFloor(t *testing.T) {
	// A machine below min_daemon_version must apply even at rollout_percent=0:
	// "everyone must reach at least X" beats "ship to nobody new".
	c := &UpdateControl{MinDaemonVer: "0.0.800", RolloutPercent: intPtr(0)}
	if blocked, reason := c.blocksApply("0.0.900", "0.0.700", "machine-1"); blocked {
		t.Fatalf("below-floor machine blocked by rollout cohort (reason=%q); floor must bypass rollout", reason)
	}
	// At/above the floor the cohort gate applies normally.
	if blocked, _ := c.blocksApply("0.0.900", "0.0.800", "machine-1"); !blocked {
		t.Fatal("at-floor machine must still respect rollout_percent=0")
	}
	// The kill switch always wins — even for a below-floor machine, a paused
	// or quarantined release must never apply.
	paused := &UpdateControl{Paused: true, MinDaemonVer: "0.0.800"}
	if blocked, _ := paused.blocksApply("0.0.900", "0.0.700", "machine-1"); !blocked {
		t.Fatal("paused must win over the below-floor rollout bypass")
	}
	halted := &UpdateControl{HaltAtOrAbove: "0.0.900", MinDaemonVer: "0.0.800"}
	if blocked, _ := halted.blocksApply("0.0.900", "0.0.700", "machine-1"); !blocked {
		t.Fatal("halt_at_or_above must win over the below-floor rollout bypass")
	}
}

func TestCheckSetsRequiredFromControl(t *testing.T) {
	disableControlSignature(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	// Manifest advertises the SAME version (no update available) — Required
	// must still flip: it reflects the floor, not update availability.
	manifest := []byte(`{"flavor":"public","version":"0.0.700","platforms":{"linux-amd64":{"url":"https://example.test/solud","sha256":"abc"}}}`)
	control := []byte(`{"schema":1,"min_daemon_version":"0.0.800","message":"critical session fix"}`)

	bus := newUIBus()
	u := NewUpdater("0.0.700", "", "", bus, nil)
	u.homeDir = t.TempDir()
	u.manifestURL = "https://updates.example.test/update.json"
	transport := staticRoundTripper{
		"https://updates.example.test/update.json": manifest,
		u.controlURL(): control,
	}
	u.httpClient = &http.Client{Transport: transport}

	st, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Required {
		t.Fatal("Required = false with min_daemon_version above current, want true")
	}
	if st.RequiredReason == "" || !strings.Contains(st.RequiredReason, "0.0.800") || !strings.Contains(st.RequiredReason, "critical session fix") {
		t.Fatalf("RequiredReason = %q, want the floor and the operator message", st.RequiredReason)
	}
	if got := countBusEvents(bus, "update-available"); got != 1 {
		t.Fatalf("update-available events after required flip = %d, want 1", got)
	}

	// Operator clears the floor → Required drops and the bus fires again so
	// the UI can take the modal down without waiting for its own poll.
	transport[u.controlURL()] = []byte(`{"schema":1}`)
	st, err = u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Required {
		t.Fatal("Required = true after the floor was cleared, want false")
	}
	if st.RequiredReason != "" {
		t.Fatalf("RequiredReason = %q after clear, want empty", st.RequiredReason)
	}
	if got := countBusEvents(bus, "update-available"); got != 2 {
		t.Fatalf("update-available events after required clear = %d, want 2 (both transitions notify)", got)
	}
}

func TestCheckNotRequiredWithoutControl(t *testing.T) {
	// Fail-open bootstrap case: no control file published (404) → never
	// required, no matter what versions are in play.
	disableControlSignature(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	manifest := []byte(`{"flavor":"public","version":"0.0.900","platforms":{"linux-amd64":{"url":"https://example.test/solud","sha256":"abc"}}}`)
	u := NewUpdater("0.0.700", "", "", nil, nil)
	u.homeDir = t.TempDir()
	u.manifestURL = "https://updates.example.test/update.json"
	u.httpClient = &http.Client{Transport: staticRoundTripper{
		"https://updates.example.test/update.json": manifest,
	}}

	st, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Required || st.ShellRequired {
		t.Fatalf("Required=%v ShellRequired=%v with no control file, want both false", st.Required, st.ShellRequired)
	}
}

func TestShellRequiredClearsOnStatusAfterInstall(t *testing.T) {
	disableControlSignature(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".solu"), 0o755); err != nil {
		t.Fatal(err)
	}
	infoPath := filepath.Join(dir, ".solu", "shell-info.json")

	manifest := []byte(`{"flavor":"public","version":"0.0.700","platforms":{"linux-amd64":{"url":"https://example.test/solud","sha256":"abc"}}}`)
	control := []byte(`{"schema":1,"min_shell_version":"0.0.800"}`)

	u := NewUpdater("0.0.700", "0.0.700", "windows-amd64", nil, nil)
	u.homeDir = dir
	u.manifestURL = "https://updates.example.test/update.json"
	u.httpClient = &http.Client{Transport: staticRoundTripper{
		"https://updates.example.test/update.json": manifest,
		u.controlURL(): control,
	}}

	st, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.ShellRequired {
		t.Fatal("ShellRequired = false with shell 0.0.700 below floor 0.0.800, want true")
	}
	if st.Required {
		t.Fatal("daemon Required = true, want false (only the shell floor is set)")
	}

	// The user runs the installer: the relaunched shell rewrites
	// shell-info.json. The very next Status() read — NOT a 30s Check tick —
	// must drop ShellRequired, or the blocking modal lingers after a
	// successful update.
	if err := os.WriteFile(infoPath, []byte(`{"version":"0.0.800","platform":"windows-amd64"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := u.Status(); st.ShellRequired {
		t.Fatal("ShellRequired = true on Status() after the shell was upgraded to the floor, want false")
	}
}
