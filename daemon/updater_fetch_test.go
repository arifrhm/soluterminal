package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"

	"soluterminal-shared/releases"
)

// releaseRepoUpdater polls the default (current-owner) latest alias while the
// fixture serves the release from repo only.
func releaseRepoUpdater(t *testing.T, repo string, body, signedBody []byte) *Updater {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOLU_UPDATE_MANIFEST_PUBKEY", base64.StdEncoding.EncodeToString(pub))
	t.Setenv("SOLU_UPDATE_MANIFEST_URL", "")
	t.Setenv("SOLU_UPDATE_CONTROL_URL", "")
	u := NewUpdater("0.0.1", "", "", nil, nil)
	if u.manifestURL != releases.LatestManifestURL(releases.Repo) {
		t.Fatalf("default manifest URL = %q, want the current releases repo", u.manifestURL)
	}
	pinned := releases.DownloadPrefix(repo) + "v9.9.9/"
	u.httpClient = &http.Client{Transport: staticRoundTripper{
		releases.LatestManifestURL(repo): body,
		pinned + "update.json":           body,
		pinned + "update.json.sig":       []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, signedBody))),
	}}
	return u
}

func signedReleaseBody(repo string) []byte {
	return []byte(`{"version":"9.9.9","platforms":{"linux-amd64":{"url":"` +
		releases.DownloadPrefix(repo) + `v9.9.9/solud-linux-amd64","sha256":"abc"}}}`)
}

// Before the releases repo moves to its new owner, the new URLs 404 and the
// daemon reads the same files from the legacy owner.
func TestUpdaterFallsBackToLegacyReleasesOwner(t *testing.T) {
	body := signedReleaseBody(releases.LegacyRepo)
	u := releaseRepoUpdater(t, releases.LegacyRepo, body, body)
	st, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Latest != "9.9.9" || st.LastError != "" {
		t.Fatalf("status = %+v", st)
	}
}

func TestUpdaterUsesCurrentReleasesOwner(t *testing.T) {
	body := signedReleaseBody(releases.Repo)
	u := releaseRepoUpdater(t, releases.Repo, body, body)
	if st, err := u.Check(context.Background()); err != nil || st.Latest != "9.9.9" {
		t.Fatalf("Check = %+v, %v", st, err)
	}
}

// The fallback is no way around the signature: a manifest changed after
// signing is refused under the legacy owner too.
func TestUpdaterLegacyFallbackRejectsTamperedManifest(t *testing.T) {
	signed := signedReleaseBody(releases.LegacyRepo)
	tampered := []byte(strings.Replace(string(signed), `"sha256":"abc"`, `"sha256":"evil"`, 1))
	u := releaseRepoUpdater(t, releases.LegacyRepo, tampered, signed)
	st, err := u.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "signature verification failed") {
		t.Fatalf("Check error = %v, want a signature failure", err)
	}
	if u.manifest != nil {
		t.Fatal("tampered manifest was stored")
	}
	if !strings.Contains(st.LastError, "signature") {
		t.Fatalf("LastError = %q", st.LastError)
	}
}

// LastError reaches the version popover and the fleet summary, so it names the
// file and status but never the release URL or its owner.
func TestUpdaterErrorsNeverShowReleaseURLs(t *testing.T) {
	u := releaseRepoUpdater(t, "nobody/nothing", nil, nil)
	st, err := u.Check(context.Background())
	if err == nil {
		t.Fatal("Check succeeded with nothing published")
	}
	for _, text := range []string{err.Error(), st.LastError} {
		if !strings.Contains(text, "update.json: HTTP 404") {
			t.Fatalf("error %q does not name the asset and status", text)
		}
		for _, banned := range []string{"https://", "github", releases.Repo, releases.LegacyRepo} {
			if strings.Contains(text, banned) {
				t.Fatalf("error %q contains %q", text, banned)
			}
		}
	}
}

type failingRoundTripper struct{}

func (failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial tcp: connection refused")
}

func TestUpdaterNetworkErrorsDropTheURL(t *testing.T) {
	_, err := getReleaseAsset(context.Background(), &http.Client{Transport: failingRoundTripper{}},
		releases.LatestManifestURL(releases.Repo))
	if err == nil || strings.Contains(err.Error(), "https://") || !strings.Contains(err.Error(), "update.json: ") ||
		!strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v", err)
	}
}

func TestRedactUpdateURLs(t *testing.T) {
	in := `download https://github.com/` + releases.LegacyRepo + `/releases/download/v1/solud-linux-amd64: HTTP 404; see "http://x.test/a/update.json.sig"`
	got := redactUpdateURLs(in)
	if strings.Contains(got, "://") || !strings.Contains(got, "solud-linux-amd64: HTTP 404") || !strings.Contains(got, "update.json.sig") {
		t.Fatalf("redactUpdateURLs = %q", got)
	}
}
