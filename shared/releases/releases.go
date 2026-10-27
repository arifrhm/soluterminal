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
package releases

import (
	_ "embed"
	"strings"
)

//go:embed repo.txt
var repoFile string

//go:embed legacy-repo.txt
var legacyRepoFile string

// Repo is "<owner>/<name>" of the current releases repo.
var Repo = strings.TrimSpace(repoFile)

// LegacyRepo is "<owner>/<name>" of the repo's previous home.
var LegacyRepo = strings.TrimSpace(legacyRepoFile)

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
