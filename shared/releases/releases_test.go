package releases

import (
	"regexp"
	"strings"
	"testing"
)

var repoRE = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

func TestRepoFilesAreWellFormed(t *testing.T) {
	for _, r := range []string{Repo, LegacyRepo} {
		if !repoRE.MatchString(r) {
			t.Fatalf("repo %q is not <owner>/<name>", r)
		}
	}
	if Repos()[0] != Repo {
		t.Fatalf("current repo must come first: %v", Repos())
	}
}

func TestURLs(t *testing.T) {
	if got, want := LatestManifestURL("o/r"), "https://github.com/o/r/releases/latest/download/update.json"; got != want {
		t.Fatalf("LatestManifestURL = %q, want %q", got, want)
	}
	if got, want := DownloadPrefix("o/r"), "https://github.com/o/r/releases/download/"; got != want {
		t.Fatalf("DownloadPrefix = %q, want %q", got, want)
	}
}

func TestIsDownloadURLAcceptsBothOwners(t *testing.T) {
	for _, r := range Repos() {
		if !IsDownloadURL(DownloadPrefix(r) + "v1.0.0/solud-linux-amd64") {
			t.Fatalf("%s download URL rejected", r)
		}
	}
	for _, raw := range []string{
		"https://github.com/someone-else/soluterminal-releases/releases/download/v1/x",
		"https://github.com/" + Repo + "/releases/latest/download/x",
		"http://github.com/" + Repo + "/releases/download/v1/x",
	} {
		if IsDownloadURL(raw) {
			t.Fatalf("%s accepted", raw)
		}
	}
}

func TestLegacyFallback(t *testing.T) {
	if LegacyRepo == "" || strings.EqualFold(LegacyRepo, Repo) {
		// A build with no previous home (the public source copy): nothing
		// falls back.
		if _, ok := LegacyFallback(DownloadPrefix(Repo) + "v1.2.3/update.json"); ok {
			t.Fatal("no legacy repo, yet a URL fell back")
		}
		return
	}
	got, ok := LegacyFallback(DownloadPrefix(Repo) + "v1.2.3/update.json")
	if !ok || got != DownloadPrefix(LegacyRepo)+"v1.2.3/update.json" {
		t.Fatalf("LegacyFallback = %q, %v", got, ok)
	}
	if _, ok := LegacyFallback(DownloadPrefix(LegacyRepo) + "v1/update.json"); ok {
		t.Fatal("a legacy URL must not fall back again")
	}
	if _, ok := LegacyFallback("http://127.0.0.1:1/releases/latest/download/update.json"); ok {
		t.Fatal("a custom URL must not fall back")
	}
}
