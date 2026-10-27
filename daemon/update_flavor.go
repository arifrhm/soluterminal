package main

// Update flavours: the public app and the team build never update into each
// other by accident.
//
// Every build has a flavour fixed at build time: "public" (the official app,
// built from the public source) or "team" (the public app plus the private
// account tools). Every install follows a channel, "public" or "team"
// (update_channel.go): the account decides it, and a new install starts on
// its build's flavour. The channel decides two things:
//
//  1. where the updater looks: the public channel reads the public releases
//     repo (shared/releases/repo.txt), the team channel the team releases repo
//     (team-repo.txt);
//  2. what it accepts: the signed update.json names the flavour it was
//     published for ("flavor"), and the updater refuses any manifest whose
//     flavour is missing or is not the install's channel, on every path that
//     trusts a manifest (latest, pinned, rollback). The field sits inside the
//     Ed25519-signed bytes, so it cannot be changed without failing the
//     signature check, and pointing an install at the other repo (a
//     mis-publish, or SOLU_UPDATE_MANIFEST_URL) is refused by this check too.
//
// The flavour is stamped with -ldflags "-X main.buildFlavorStamp=..." by
// scripts/release-ldflags.sh (SOLU_BUILD_FLAVOR). An unstamped build is
// PUBLIC, and so is any stamp other than exactly "team". Public is the safe
// default: a team binary that wrongly believes it is public can at worst
// start on the public build (it loses the team tools until its account moves
// it back), whereas a public binary that wrongly started on the team channel
// would install private code on a machine with no team account.
//
// The stamp carries a "solu-build-flavor=" prefix so release tooling can read
// a cross-compiled binary's flavour from its bytes: a team binary contains
// the prefix immediately followed by "team", and no public binary does (no
// non-test source spells that marker out, comments included).

import (
	"fmt"
	"strings"

	"soluterminal-shared/releases"
)

const buildFlavorMarkerPrefix = "solu-build-flavor="

var buildFlavorStamp = buildFlavorMarkerPrefix + releases.FlavorPublic

// currentBuildFlavor is this binary's flavour: "team" or "public".
func currentBuildFlavor() string {
	return releases.NormalizeFlavor(strings.TrimPrefix(strings.TrimSpace(buildFlavorStamp), buildFlavorMarkerPrefix))
}

// manifestURLForFlavor is the latest-alias manifest URL a build of flavor
// polls, or "" when this build has no releases repo for it (a team build from
// the public source export).
func manifestURLForFlavor(flavor string) string {
	repo := releases.RepoForFlavor(flavor)
	if repo == "" {
		return ""
	}
	return releases.LatestManifestURL(repo)
}

// manifestFlavorError reports a verified manifest published for another
// channel. It is never transient: retrying cannot change signed bytes.
type manifestFlavorError struct {
	want, got string
	// version is the refused manifest's version; newer reports whether it
	// is newer than the running daemon (an older or equal one cannot be
	// installed anyway, so Check does not surface it as a failure).
	version string
	newer   bool
}

func (e *manifestFlavorError) Error() string {
	if e.got == "" {
		return fmt.Sprintf("update manifest names no build flavour (published before flavours); this install follows %s releases only", e.want)
	}
	return fmt.Sprintf("update manifest is for the %s build; this install follows the %s build", e.got, e.want)
}

// checkManifestFlavor refuses a manifest that is not for channel. A missing
// field (every manifest published before flavours, 0.0.1224 and older) is
// refused as well: those releases are full-tree builds, so no flavour-aware
// install may update or roll back into one.
func checkManifestFlavor(m *Manifest, channel, current string) error {
	got := strings.TrimSpace(m.Flavor)
	want := releases.NormalizeFlavor(channel)
	if got == want {
		return nil
	}
	return &manifestFlavorError{want: want, got: got, version: m.Version, newer: isNewer(m.Version, current)}
}
