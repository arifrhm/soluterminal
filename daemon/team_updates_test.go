package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"soluterminal-shared/releases"
)

const (
	testTeamCloud       = "https://cloud.test"
	testTeamUpdatesPath = "/api/test-team-updates/"
	testMachineToken    = "machine-token-for-tests"
)

// setTeamUpdatesPath swaps the team build's -ldflags stamp for one test.
func setTeamUpdatesPath(t *testing.T, path string) {
	t.Helper()
	prev := teamUpdatesPath
	teamUpdatesPath = path
	t.Cleanup(func() { teamUpdatesPath = prev })
}

// serveRelease publishes a signed release of flavor at version under
// releasesBase (".../releases/"): the latest alias and the pinned pair.
func serveRelease(bodies map[string][]byte, priv ed25519.PrivateKey, releasesBase, flavor, version string) {
	body := flavorManifest(flavor, version)
	bodies[releasesBase+"latest/download/update.json"] = body
	pinned := releasesBase + "download/v" + version + "/"
	bodies[pinned+"update.json"] = body
	bodies[pinned+"update.json.sig"] = signB64(priv, body)
}

// teamCloudUpdater follows the team channel through testTeamCloud.
func teamCloudUpdater(t *testing.T, rt *recordingRoundTripper) *Updater {
	t.Helper()
	setTeamUpdatesPath(t, testTeamUpdatesPath)
	t.Setenv("SOLU_UPDATE_MANIFEST_URL", "")
	setBuildFlavorStamp(t, "solu-build-flavor=team")
	u := NewUpdater("1.0.0", "", "windows-amd64", nil, nil)
	u.homeDir = t.TempDir()
	u.SetCloudCreds(testTeamCloud, testMachineToken)
	u.httpClient = &http.Client{Transport: &teamUpdatesAuth{next: rt, creds: u.cloudCreds}}
	return u
}

func TestUnstampedBuildHasNoTeamEndpoint(t *testing.T) {
	if teamUpdatesPath != "" || teamUpdatesConfigured() {
		t.Fatalf("an unstamped build has the team endpoint %q", teamUpdatesPath)
	}
	if _, viaCloud, ok := channelSource(releases.FlavorTeam); ok || viaCloud {
		t.Fatal("an unstamped build has a team update source")
	}
	if teamUpdatesManifestURL(testTeamCloud) != "" {
		t.Fatal("an unstamped build builds team URLs")
	}
}

func TestTeamUpdatesURLs(t *testing.T) {
	setTeamUpdatesPath(t, testTeamUpdatesPath)
	if got := teamUpdatesManifestURL(testTeamCloud + "/"); got != "https://cloud.test/api/test-team-updates/releases/latest/download/update.json" {
		t.Fatalf("manifest URL = %q", got)
	}
	for _, ok := range []string{"http://127.0.0.1:8080", "http://localhost:8080", "http://[::1]:8080"} {
		if teamUpdatesBase(ok) == "" {
			t.Errorf("loopback cloud %q refused", ok)
		}
	}
	for _, bad := range []string{"", "cloud.test", "ftp://cloud.test", "https://cloud.test/x", "https://cloud.test?q=1", "https://", "http://cloud.test", "http://10.0.0.5:8080"} {
		if got := teamUpdatesBase(bad); got != "" {
			t.Errorf("cloud %q gave base %q", bad, got)
		}
	}
	for _, bad := range []string{"api/x", "/api/../x", "/api/x?y", "/api//x", "/api/x y", "/"} {
		setTeamUpdatesPath(t, bad)
		if teamUpdatesConfigured() {
			t.Errorf("stamp %q accepted", bad)
		}
	}
	setTeamUpdatesPath(t, testTeamUpdatesPath)

	for raw, want := range map[string]string{
		"https://github.com/owner/team-releases/releases/download/v3.0.0/solud-linux-amd64":                "https://cloud.test/api/test-team-updates/releases/download/v3.0.0/solud-linux-amd64",
		"https://github.com/owner/team-releases/releases/download/v2.9.0/soluterminal_2.9.0_x64-setup.exe": "https://cloud.test/api/test-team-updates/releases/download/v2.9.0/soluterminal_2.9.0_x64-setup.exe",
	} {
		got, ok := teamAssetURL(raw, testTeamCloud)
		if !ok || got != want {
			t.Errorf("teamAssetURL(%s) = %q %v, want %q", raw, got, ok, want)
		}
	}
	for _, raw := range []string{
		"http://github.com/o/r/releases/download/v1/x",
		"https://cdn.test/solud",
		"https://github.com/o/r/releases/download/v1/x?token=1",
		"https://github.com/o/r/releases/latest/download/x",
		"https://github.com/o/r/releases/download/v1/../x",
		"https://github.com/o/r/releases/download/v1/a/b",
		"https://github.com/o/r/releases/download/../x",
		"https://evil.test/o/r/releases/download/v1/x",
	} {
		if got, ok := teamAssetURL(raw, testTeamCloud); ok {
			t.Errorf("teamAssetURL(%s) mapped to %q", raw, got)
		}
	}
}

// The machine token goes to the paired cloud's team endpoint and nowhere
// else: not GitHub, not another cloud path, not another host.
func TestTeamUpdatesAuthAttachesTheTokenOnlyToTheEndpoint(t *testing.T) {
	setTeamUpdatesPath(t, testTeamUpdatesPath)
	rt := &recordingRoundTripper{bodies: map[string][]byte{}}
	auth := &teamUpdatesAuth{next: rt, creds: func() (string, string) { return testTeamCloud, testMachineToken }}
	client := &http.Client{Transport: auth}
	for url, want := range map[string]string{
		"https://cloud.test/api/test-team-updates/releases/latest/download/update.json": "Bearer " + testMachineToken,
		"https://cloud.test/api/test-team-updates/releases/download/v1.0.0/x":           "Bearer " + testMachineToken,
		"https://cloud.test/api/entitlements":                                           "",
		"https://cloud.test/api/test-team-updates-other/x":                              "",
		"http://cloud.test/api/test-team-updates/releases/latest/download/update.json":  "",
		"https://evil.test/api/test-team-updates/releases/latest/download/update.json":  "",
		"https://github.com/owner/repo/releases/download/v1.0.0/x":                      "",
	} {
		resp, err := client.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := rt.auth[url]; got != want {
			t.Errorf("%s: Authorization %q, want %q", url, got, want)
		}
	}
	// Without a token (not paired, or after Disconnect) nothing is attached.
	auth.creds = func() (string, string) { return testTeamCloud, "" }
	url := "https://cloud.test/api/test-team-updates/releases/latest/download/update.json"
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := rt.auth[url]; got != "" {
		t.Fatalf("Authorization without a token: %q", got)
	}
}

// End to end: a team install resolves, pins and verifies the signed team
// manifest through the cloud, and downloads the binary the manifest names on
// the private repo through the cloud too, checked against the signed digest.
func TestTeamChannelUpdatesThroughTheCloud(t *testing.T) {
	priv := flavorKey(t)
	rt := &recordingRoundTripper{bodies: map[string][]byte{}}
	u := teamCloudUpdater(t, rt)
	base := teamUpdatesBase(testTeamCloud)
	serveRelease(rt.bodies, priv, base+"releases/", releases.FlavorTeam, "3.0.0")
	st, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Latest != "3.0.0" || !st.Available || st.UpdateChannel != releases.FlavorTeam {
		t.Fatalf("status = %+v", st)
	}

	payload := []byte("team daemon bytes")
	sum := sha256.Sum256(payload)
	rt.bodies[base+"releases/download/v3.0.0/solud-linux-amd64"] = payload
	dst := filepath.Join(t.TempDir(), "solud")
	signedURL := "https://github.com/owner/team-releases/releases/download/v3.0.0/solud-linux-amd64"
	if err := u.download(context.Background(), signedURL, hex.EncodeToString(sum[:]), dst); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); string(got) != string(payload) {
		t.Fatalf("downloaded %q", got)
	}
	// A cloud that serves other bytes is caught by the signed digest.
	rt.bodies[base+"releases/download/v3.0.0/solud-linux-amd64"] = []byte("tampered")
	if err := u.download(context.Background(), signedURL, hex.EncodeToString(sum[:]), dst+"2"); err == nil {
		t.Fatal("bytes that do not match the signed digest were accepted")
	}
	for _, url := range rt.seen {
		if !strings.HasPrefix(url, base) {
			t.Fatalf("the team channel requested %s outside the cloud endpoint", url)
		}
		if rt.auth[url] != "Bearer "+testMachineToken {
			t.Fatalf("%s went without the machine token", url)
		}
	}
}

// The cloud is only a transport: the signed flavour check still decides.
func TestTeamChannelRefusesAPublicManifestFromTheCloud(t *testing.T) {
	priv := flavorKey(t)
	rt := &recordingRoundTripper{bodies: map[string][]byte{}}
	u := teamCloudUpdater(t, rt)
	serveRelease(rt.bodies, priv, teamUpdatesBase(testTeamCloud)+"releases/", releases.FlavorPublic, "3.0.0")
	_, err := u.Check(context.Background())
	var fe *manifestFlavorError
	if !errors.As(err, &fe) {
		t.Fatalf("a public manifest over the team transport: %v", err)
	}
	if u.Status().Available {
		t.Fatal("the refused manifest was offered")
	}
}

// Not paired, refused by the cloud (404), or the service down: the install
// stays on its version and its channel, says why, and never asks the public
// repo.
func TestTeamChannelStaysPutWhenTheCloudCannotServeIt(t *testing.T) {
	flavorKey(t)
	manifest := "https://cloud.test/api/test-team-updates/releases/latest/download/update.json"
	for _, tc := range []struct {
		name      string
		paired    bool
		status    int
		wantError string
		transient bool
	}{
		{"not paired", false, 0, "not connected", false},
		{"cloud answers 404", true, http.StatusNotFound, "not confirmed as a team machine", false},
		{"team service not configured", true, http.StatusServiceUnavailable, "team update service is not running", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &recordingRoundTripper{bodies: map[string][]byte{}, status: map[string]int{}}
			u := teamCloudUpdater(t, rt)
			if !tc.paired {
				u.SetCloudCreds("", "")
			}
			if tc.status != 0 {
				rt.status[manifest] = tc.status
			}
			// Background polling stays on even before pairing: creds can
			// arrive later.
			if u.noUpdateSource() {
				t.Fatal("the team channel reports no update source")
			}
			var err error
			for i := 0; i < maxTransientFailures; i++ {
				_, err = u.Check(context.Background())
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("Check error = %v, want %q", err, tc.wantError)
			}
			if isTransientUpdateErr(err) != tc.transient {
				t.Fatalf("transient = %v, want %v", isTransientUpdateErr(err), tc.transient)
			}
			st := u.Status()
			if !strings.Contains(st.LastError, tc.wantError) || strings.Contains(st.LastError, "cloud.test") {
				t.Fatalf("status error = %q", st.LastError)
			}
			if st.Available || st.UpdateChannel != releases.FlavorTeam || u.channelSnapshot() != releases.FlavorTeam {
				t.Fatalf("status = %+v", st)
			}
			for _, url := range rt.seen {
				if !strings.HasPrefix(url, teamUpdatesBase(testTeamCloud)) {
					t.Fatalf("fell back to %s", url)
				}
			}
			if !tc.paired && len(rt.seen) != 0 {
				t.Fatalf("an unpaired install made requests: %v", rt.seen)
			}
		})
	}
}

func TestTeamDownloadNeedsThePairedCloud(t *testing.T) {
	rt := &recordingRoundTripper{bodies: map[string][]byte{}}
	u := teamCloudUpdater(t, rt)
	u.SetCloudCreds("", "")
	err := u.download(context.Background(), "https://github.com/o/r/releases/download/v1.0.0/solud-linux-amd64", "00", filepath.Join(t.TempDir(), "x"))
	if !errors.Is(err, errTeamUpdatesNoCloud) || len(rt.seen) != 0 {
		t.Fatalf("download without a cloud: %v, requests %v", err, rt.seen)
	}
}
