// Package releases names the public GitHub repo that hosts SoluTerminal
// releases and builds the URLs the updaters, the cloud and the release
// scripts use.
//
// The owner/repo lives in repo.txt, the one place to change it. The desktop
// shell (desktop/src-tauri/src/shell_update.rs) and the release scripts read
// the same file, so everything moves together.
//
// legacy-repo.txt is the repo's previous home. GitHub redirects it to the new
// owner after a transfer, and installs from before the move poll it forever,
// so URLs under either owner are accepted, and a 404 under the new owner
// (the transfer has not happened yet) falls back to the legacy owner. Every
// byte fetched either way is still checked against the signed manifest.
//
// team-repo.txt is the separate repo that hosts the team build (the public
// app plus the private account tools). Each build has a flavour, "public" or
// "team", fixed at build time; it reads only its own flavour's repo, and the
// signed manifest names the flavour it was published for, so a public install
// never takes the team build and a team install never takes the public one.
// The public source export empties team-repo.txt: a build from public source
// has no team repo at all.
package releases

import (
	_ "embed"
	"strings"
)

//go:embed repo.txt
var repoFile string

//go:embed legacy-repo.txt
var legacyRepoFile string

//go:embed team-repo.txt
var teamRepoFile string

// Repo is "<owner>/<name>" of the current releases repo.
var Repo = strings.TrimSpace(repoFile)

// LegacyRepo is "<owner>/<name>" of the repo's previous home.
var LegacyRepo = strings.TrimSpace(legacyRepoFile)

// TeamRepo is "<owner>/<name>" of the team build's releases repo, or "" in a
// build from the public source export.
var TeamRepo = strings.TrimSpace(teamRepoFile)

// Build flavours. Every name other than FlavorTeam means FlavorPublic.
const (
	FlavorPublic = "public"
	FlavorTeam   = "team"
)

// NormalizeFlavor maps a stamped flavour to FlavorTeam only when it is exactly
// "team"; anything else (empty, a typo, a future name) is FlavorPublic. A
// mis-stamped build can therefore only ever be public: the safe direction,
// because the team build carries private code that must never reach a public
// install, while a team install that lands on the public build only loses
// the team tools until it is reinstalled.
func NormalizeFlavor(flavor string) string {
	if strings.TrimSpace(flavor) == FlavorTeam {
		return FlavorTeam
	}
	return FlavorPublic
}

// RepoForFlavor is the releases repo a build of flavor reads: TeamRepo
// ("" when this build has none) for the team build, Repo otherwise.
func RepoForFlavor(flavor string) string {
	if NormalizeFlavor(flavor) == FlavorTeam {
		return TeamRepo
	}
	return Repo
}

// ReposForFlavor lists the repos a build of flavor accepts URLs under: the
// team repo alone for the team build (it has no legacy home), Repos() for the
// public build. Empty for a team build without a team repo.
func ReposForFlavor(flavor string) []string {
	if NormalizeFlavor(flavor) == FlavorTeam {
		if TeamRepo == "" {
			return nil
		}
		return []string{TeamRepo}
	}
	return Repos()
}

// Repos lists the current repo first, then the legacy one.
func Repos() []string {
	if LegacyRepo == "" || strings.EqualFold(LegacyRepo, Repo) {
		return []string{Repo}
	}
	return []string{Repo, LegacyRepo}
}

const githubOrigin = "https://github.com/"

// ReleasesBase is "https://github.com/<repo>/releases/".
func ReleasesBase(repo string) string {
	return githubOrigin + repo + "/releases/"
}

// LatestManifestURL is the latest-alias update.json URL for repo.
func LatestManifestURL(repo string) string {
	return ReleasesBase(repo) + "latest/download/update.json"
}

// DownloadPrefix is the immutable per-tag asset prefix for repo,
// "https://github.com/<repo>/releases/download/".
func DownloadPrefix(repo string) string {
	return ReleasesBase(repo) + "download/"
}

// RepoPathPrefixes returns "/<repo>/releases/" for each accepted repo, for
// checks against a parsed URL's path.
func RepoPathPrefixes() []string {
	repos := Repos()
	out := make([]string, 0, len(repos))
	for _, r := range repos {
		out = append(out, "/"+r+"/releases/")
	}
	return out
}

// IsDownloadURL reports whether raw is an immutable per-tag asset URL under
// the current or the legacy releases repo.
func IsDownloadURL(raw string) bool {
	for _, r := range Repos() {
		if strings.HasPrefix(raw, DownloadPrefix(r)) {
			return true
		}
	}
	return false
}

// LegacyFallback maps a URL under the current repo's releases to the same
// path under the legacy repo. ok is false for any other URL, and when there
// is no distinct legacy repo.
func LegacyFallback(raw string) (string, bool) {
	if LegacyRepo == "" || strings.EqualFold(LegacyRepo, Repo) {
		return "", false
	}
	prefix := ReleasesBase(Repo)
	if !strings.HasPrefix(raw, prefix) {
		return "", false
	}
	return ReleasesBase(LegacyRepo) + strings.TrimPrefix(raw, prefix), true
}
