package main

// External-downgrade detection: notice when something OTHER than the updater
// replaced our binary with an older version, and treat it as a machine-local
// required update so the fleet heals itself.
//
// The root incident combined three individually valid/legacy behaviors: a
// daemon-only publish advanced manifest.version to 0.0.932 while intentionally
// carrying shell.version 0.0.916; the old web flow applied the daemon-first leg
// and then launched that carried installer; and the unguarded NSIS payload
// killed solud.exe and copied its bundled 0.0.916 over the installed 0.0.932.
// Current entry points and the NSIS payload now refuse that downgrade before
// process termination or copying unless it is explicitly forced. This detector
// remains defense-in-depth recovery for already-published legacy installers;
// it is not the primary fix and must not become a hard-coded version floor.
//
// The detector is the <binary>.version marker the updater already maintains:
// every successful swap writes the new version, every rollback rewrites the
// version it restored (updater_apply.go, updater_bootguard.go). Nothing else
// wrote it before this file, and nothing read it — it existed purely as a
// breadcrumb. Its maintained invariant is therefore "the version the updater
// last left at this path". A binary OLDER than its own marker means an outside
// force regressed us; a marker that is missing or older just means we advanced
// normally (or were never self-updated), so we raise it and move on. A genuine
// boot-guard rollback rewrites the marker DOWN to the restored version on
// purpose, which is what keeps this heal from re-forcing a release the guard
// just rolled back.
//
// The heal itself deliberately rides the operator-floor machinery: the marker
// becomes a machine-local min_daemon_version, flipping status.Required so the
// embedded client force-applies even for users with auto-apply off (that
// override is the whole point — the user who runs stale installers is exactly
// the user who never opted into auto-apply). Unlike the operator floor it is
// gated on an update actually being Available: an operator floor blocks the UI
// until satisfied by design, but nobody asked this machine's user for anything,
// so with no update to offer (offline, halted release, empty manifest) it stays
// silent instead of trapping them behind a modal with no button that works.

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// readVersionMarker returns the trimmed contents of binPath's ".version"
// sidecar, or "" when it is missing/unreadable.
func readVersionMarker(binPath string) string {
	b, err := os.ReadFile(binPath + ".version")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// detectExternalDowngrade compares current against binPath's version marker.
// It returns the marker version when the running binary is OLDER than the
// marker (an external downgrade), and "" otherwise. When the binary is at or
// ahead of the marker it raises the marker to current, so the invariant
// "marker = newest version that ran here" holds even for installs that have
// never self-updated (their first boot seeds it).
func detectExternalDowngrade(binPath, current string) string {
	current = strings.TrimSpace(current)
	if current == "" || current == "dev" || binPath == "" {
		// Dev builds parse as version 0 — every marker would look like a
		// downgrade and the heal would fight every `go run`. Same exemption
		// versionBelowFloor makes.
		return ""
	}
	marker := readVersionMarker(binPath)
	if marker != "" && compareVersions(marker, current) > 0 {
		return marker
	}
	if marker == "" || compareVersions(current, marker) > 0 {
		writeVersionMarker(binPath, current)
	}
	return ""
}

// DetectExternalDowngrade runs the detection against our own executable and
// arms the local floor on the updater. Called once at daemon startup — not in
// NewUpdater, so tests constructing updaters don't scribble marker files next
// to the go test binary.
func (u *Updater) DetectExternalDowngrade() {
	self, err := os.Executable()
	if err != nil {
		return
	}
	if rp, err := filepath.EvalSymlinks(self); err == nil {
		self = rp
	}
	floor := detectExternalDowngrade(self, u.current)
	if floor == "" {
		return
	}
	slog.Warn("daemon binary is older than its own version marker — an installer downgraded it; arming a local required-update floor",
		"running", u.current, "marker", floor, "binary", self)
	u.mu.Lock()
	u.localFloor = floor
	u.mu.Unlock()
}
