package main

// Tests for external-downgrade detection (updater_downgrade.go) — the heal for
// the NSIS installer silently overwriting a self-updated solud.exe with its
// older bundled copy. Observed in the fleet: a machine self-updated
// 0.0.869→0.0.932, then reported 0.0.916 (the installer's copy) days later,
// which also re-exposed it to bugs fixed in between. The invariants:
//   - binary older than its own marker → downgrade detected, local floor armed
//   - marker missing or older → no detection, marker raised to current
//     ("marker = newest version that ran here", seeded on first boot)
//   - dev builds exempt entirely (would parse as 0 → every marker looks newer)
//   - in Check(), the local floor flips Required ONLY when an update is
//     actually Available — unlike the operator floor, it must never trap a
//     user behind a modal with nothing to offer
//   - the operator floor's reason wins when both fire

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectExternalDowngrade(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "solud")

	// No marker → not a downgrade; marker seeded to current.
	if got := detectExternalDowngrade(bin, "0.0.900"); got != "" {
		t.Fatalf("no marker: got %q, want no detection", got)
	}
	if m := readVersionMarker(bin); m != "0.0.900" {
		t.Fatalf("marker after seed = %q, want 0.0.900", m)
	}

	// Normal advance (self-update wrote the binary, marker behind) → raise.
	if got := detectExternalDowngrade(bin, "0.0.910"); got != "" {
		t.Fatalf("advance: got %q, want no detection", got)
	}
	if m := readVersionMarker(bin); m != "0.0.910" {
		t.Fatalf("marker after advance = %q, want 0.0.910", m)
	}

	// The installer overwrite: binary now OLDER than the marker.
	if got := detectExternalDowngrade(bin, "0.0.905"); got != "0.0.910" {
		t.Fatalf("downgrade: got %q, want the marker 0.0.910", got)
	}
	// Detection must NOT lower the marker — it is the evidence.
	if m := readVersionMarker(bin); m != "0.0.910" {
		t.Fatalf("marker after downgrade detection = %q, want 0.0.910 preserved", m)
	}

	// Same version → nothing. (The boot after the heal applies.)
	if got := detectExternalDowngrade(bin, "0.0.910"); got != "" {
		t.Fatalf("at-marker: got %q, want no detection", got)
	}

	// Dev builds never detect and never scribble markers.
	devBin := filepath.Join(t.TempDir(), "solud-dev")
	if got := detectExternalDowngrade(devBin, "dev"); got != "" {
		t.Fatalf("dev build: got %q, want no detection", got)
	}
	if _, err := os.Stat(devBin + ".version"); err == nil {
		t.Fatal("dev build must not write a marker")
	}
}

// A boot-guard rollback rewrites the marker DOWN to the restored version on
// purpose (updater_bootguard.go) — the heal must not then re-force the release
// the guard just rolled back. Simulate the post-rollback state.
func TestDetectExternalDowngradeRespectsRollback(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "solud")
	// After a failed 0.0.920 update rolled back to 0.0.910, the bootguard
	// leaves marker = 0.0.910 and the binary is 0.0.910 again.
	writeVersionMarker(bin, "0.0.910")
	if got := detectExternalDowngrade(bin, "0.0.910"); got != "" {
		t.Fatalf("post-rollback boot: got %q, want no detection (would re-force the rolled-back release)", got)
	}
}

// The Check() integration: an armed local floor flips Required — but only when
// an update is Available, and the operator floor's reason wins when both fire.
func TestCheckLocalFloorRequiresOnlyWhenAvailable(t *testing.T) {
	disableControlSignature(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	// Case 1: update available + local floor armed → Required, with the
	// downgrade explanation.
	manifest := []byte(`{"flavor":"public","version":"0.0.930","platforms":{"linux-amd64":{"url":"https://example.test/solud","sha256":"abc"}}}`)
	u := NewUpdater("0.0.916", "", "", nil, nil)
	u.homeDir = t.TempDir()
	u.localFloor = "0.0.932"
	u.manifestURL = "https://updates.example.test/update.json"
	u.httpClient = &http.Client{Transport: staticRoundTripper{
		"https://updates.example.test/update.json": manifest,
	}}
	st, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Available {
		t.Fatal("precondition: update should be available")
	}
	if !st.Required {
		t.Fatal("local floor + available update: Required = false, want true (heal must override autoApply=off)")
	}
	if !strings.Contains(st.RequiredReason, "0.0.932") || !strings.Contains(st.RequiredReason, "downgraded") {
		t.Fatalf("RequiredReason = %q, want the downgrade explanation with the previous version", st.RequiredReason)
	}

	// Case 2: NO update available (manifest at our own version) → the local
	// floor must stay silent; blocking a user who has nothing to click is
	// worse than staying downgraded until a release appears.
	same := []byte(`{"flavor":"public","version":"0.0.916","platforms":{"linux-amd64":{"url":"https://example.test/solud","sha256":"abc"}}}`)
	u2 := NewUpdater("0.0.916", "", "", nil, nil)
	u2.homeDir = t.TempDir()
	u2.localFloor = "0.0.932"
	u2.manifestURL = "https://updates.example.test/update.json"
	u2.httpClient = &http.Client{Transport: staticRoundTripper{
		"https://updates.example.test/update.json": same,
	}}
	st2, err := u2.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st2.Required {
		t.Fatal("local floor with NO available update must not flip Required (no modal with nothing to offer)")
	}

	// Case 3: operator floor AND local floor both fire → the operator's
	// reason (with their message) wins.
	control := []byte(`{"schema":1,"min_daemon_version":"0.0.930","message":"operator says jump"}`)
	u3 := NewUpdater("0.0.916", "", "", nil, nil)
	u3.homeDir = t.TempDir()
	u3.localFloor = "0.0.932"
	u3.manifestURL = "https://updates.example.test/update.json"
	u3.httpClient = &http.Client{Transport: staticRoundTripper{
		"https://updates.example.test/update.json": manifest,
		u3.controlURL(): control,
	}}
	st3, err := u3.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st3.Required || !strings.Contains(st3.RequiredReason, "operator says jump") {
		t.Fatalf("both floors: Required=%v reason=%q, want operator reason to win", st3.Required, st3.RequiredReason)
	}
}
