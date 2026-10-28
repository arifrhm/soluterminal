package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"soluterminal-shared/releases"
)

// A public build has no team update path of its own. These tests cover the
// path the paired cloud names in its entitlements answer (team_updates.go,
// entitlements.go cloudTeamUpdatesPath).

const (
	cloudNamedTeamPath = "/api/cloud-named-updates/"
	otherTeamCloud     = "https://other-cloud.test"
)

// fakeTeamCloud answers GET <cloud>/api/entitlements per cloud origin (only
// with the machine token) and serves everything else from files.
type fakeTeamCloud struct {
	mu      sync.Mutex
	answers map[string]string // cloud origin -> entitlements JSON
	files   *recordingRoundTripper
}

func newFakeTeamCloud() *fakeTeamCloud {
	return &fakeTeamCloud{answers: map[string]string{}, files: &recordingRoundTripper{bodies: map[string][]byte{}}}
}

func (f *fakeTeamCloud) answer(origin, body string) {
	f.mu.Lock()
	f.answers[origin] = body
	f.mu.Unlock()
}

func (f *fakeTeamCloud) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path == "/api/entitlements" {
		f.mu.Lock()
		body, ok := f.answers[req.URL.Scheme+"://"+req.URL.Host]
		f.mu.Unlock()
		if ok && req.Header.Get("Authorization") == "Bearer "+testMachineToken {
			return (&recordingRoundTripper{bodies: map[string][]byte{req.URL.String(): []byte(body)}}).RoundTrip(req)
		}
		return (&recordingRoundTripper{status: map[string]int{req.URL.String(): http.StatusUnauthorized}}).RoundTrip(req)
	}
	return f.files.RoundTrip(req)
}

// requests returns and forgets the update requests seen so far.
func (f *fakeTeamCloud) requests() (urls []string, auth map[string]string) {
	f.files.mu.Lock()
	defer f.files.mu.Unlock()
	urls, auth = f.files.seen, f.files.auth
	f.files.seen, f.files.auth = nil, nil
	return urls, auth
}

// signedControl publishes a signed control file under releasesBase.
func signedControl(bodies map[string][]byte, priv ed25519.PrivateKey, releasesBase, body string) {
	u := releasesBase + "download/control/update-control.json"
	bodies[u] = []byte(body)
	bodies[u+".sig"] = signB64(priv, []byte(body))
}

type publicTeamRig struct {
	u      *Updater
	e      *teamEntitlements
	cloud  *fakeTeamCloud
	priv   ed25519.PrivateKey
	data   string
	team   string // the cloud's team releases base for testTeamCloud
	public string
}

// publicBuildOnTeamMachine is a PUBLIC binary (no team path stamped) on a
// machine paired with testTeamCloud, whose data dir holds the cached verdict
// a team build (0.0.1224 and older) wrote: team, from the machine token, and
// no team update path. No update_channel.json yet: this is the first start
// after the migration.
func publicBuildOnTeamMachine(t *testing.T) *publicTeamRig {
	t.Helper()
	t.Setenv("SOLU_UPDATE_MANIFEST_URL", "")
	t.Setenv("SOLU_UPDATE_CHANNEL", "")
	t.Setenv("SOLU_UPDATE_CONTROL_URL", "")
	setBuildFlavorStamp(t, "solu-build-flavor=public")
	setTeamUpdatesPath(t, "")
	r := &publicTeamRig{priv: flavorKey(t), cloud: newFakeTeamCloud(), data: t.TempDir()}
	checked := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	if err := os.WriteFile(filepath.Join(r.data, entitlementsFile), []byte(`{"team":true,"tier":"team","source":"machine","checkedAt":"`+checked+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	creds := struct {
		sync.Mutex
		base string
	}{base: testTeamCloud}
	r.e = newTeamEntitlements(r.data, func() (string, string) {
		creds.Lock()
		defer creds.Unlock()
		return creds.base, testMachineToken
	})
	r.e.client = &http.Client{Transport: r.cloud}

	r.u = NewUpdater("1.0.0", "", "windows-amd64", nil, nil)
	r.u.homeDir = t.TempDir()
	r.u.SetCloudCreds(testTeamCloud, testMachineToken)
	r.u.SetCloudTeamUpdates(r.e.cloudTeamUpdatesPath)
	r.u.httpClient = &http.Client{Transport: &teamUpdatesAuth{next: r.cloud, creds: r.u.cloudCreds, path: r.u.teamUpdatesPathFor}}
	r.u.channelStore = newUpdateChannelStore(r.data)
	r.u.channelEvidence = func() channelEvidence { return entitlementChannelEvidence(r.e) }

	r.public = releases.ReleasesBase(releases.Repo)
	r.team = teamUpdatesBase(testTeamCloud, cloudNamedTeamPath) + "releases/"
	serveRelease(r.cloud.files.bodies, r.priv, r.public, releases.FlavorPublic, "2.0.0")
	serveRelease(r.cloud.files.bodies, r.priv, r.team, releases.FlavorTeam, "3.0.0")
	signedControl(r.cloud.files.bodies, r.priv, r.public, `{"message":"public-control"}`)
	signedControl(r.cloud.files.bodies, r.priv, r.team, `{"message":"team-control","min_daemon_version":"3.0.0"}`)
	return r
}

func (r *publicTeamRig) check(t *testing.T, wantChannel, wantLatest string) *UpdateStatus {
	t.Helper()
	st, err := r.u.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if st.UpdateChannel != wantChannel || st.Latest != wantLatest || !st.Available {
		t.Fatalf("status = channel %q latest %q available %v, want %s %s", st.UpdateChannel, st.Latest, st.Available, wantChannel, wantLatest)
	}
	return st
}

func (r *publicTeamRig) storedChannel(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.data, updateChannelFile))
	if err != nil {
		t.Fatal(err)
	}
	var rec updateChannelRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	return rec.Channel
}

// assertOnlyPublic fails unless every update request went to the public
// repo, without a token.
func (r *publicTeamRig) assertOnlyPublic(t *testing.T) {
	t.Helper()
	urls, auth := r.cloud.requests()
	for _, u := range urls {
		legacy := strings.HasPrefix(u, releases.ReleasesBase(releases.LegacyRepo))
		if !strings.HasPrefix(u, r.public) && !legacy {
			t.Fatalf("public channel requested %s", u)
		}
		if auth[u] != "" {
			t.Fatalf("%s carried Authorization", u)
		}
	}
}

// assertOnlyTeam fails unless every update request went to the cloud's team
// endpoint, with the machine token.
func (r *publicTeamRig) assertOnlyTeam(t *testing.T) {
	t.Helper()
	urls, auth := r.cloud.requests()
	if len(urls) == 0 {
		t.Fatal("no team requests")
	}
	for _, u := range urls {
		if !strings.HasPrefix(u, r.team) {
			t.Fatalf("team channel requested %s", u)
		}
		if auth[u] != "Bearer "+testMachineToken {
			t.Fatalf("%s went without the machine token", u)
		}
	}
}

// The migration on a public binary, end to end: the first start decides the
// team channel from the cached verdict, keeps following public until the
// paired cloud names its team endpoint, then fetches, verifies and downloads
// the signed team build through that cloud.
func TestPublicBuildReachesTheTeamBuildThroughTheCloudNamedPath(t *testing.T) {
	r := publicBuildOnTeamMachine(t)
	if teamUpdatesConfigured() || currentBuildFlavor() != releases.FlavorPublic {
		t.Fatal("rig is not a public build")
	}

	// First start: the channel is decided from the cached verdict and
	// stored, but this build has no team source yet.
	r.check(t, releases.FlavorPublic, "2.0.0")
	if got := r.storedChannel(t); got != releases.FlavorTeam {
		t.Fatalf("first decision stored %q, want team", got)
	}
	r.assertOnlyPublic(t)

	// The daemon's first fresh entitlements answer names the endpoint.
	r.cloud.answer(testTeamCloud, `{"team":true,"tier":"team","teamUpdates":"`+cloudNamedTeamPath+`"}`)
	r.e.refresh(context.Background(), "")
	if got := r.e.cloudTeamUpdatesPath(testTeamCloud); got != cloudNamedTeamPath {
		t.Fatalf("cached path = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(r.data, entitlementsFile)); !strings.Contains(string(got), `"teamUpdatesOrigin":"https://cloud.test"`) {
		t.Fatalf("entitlements.json = %s", got)
	}
	r.cloud.requests()

	st := r.check(t, releases.FlavorTeam, "3.0.0")
	if !st.Required {
		t.Fatalf("the team control file's floor did not apply: %+v", st)
	}
	if r.u.manifest == nil || r.u.manifest.Flavor != releases.FlavorTeam {
		t.Fatalf("manifest = %+v", r.u.manifest)
	}

	payload := []byte("team daemon bytes")
	sum := sha256.Sum256(payload)
	r.cloud.files.bodies[r.team+"download/v3.0.0/solud-linux-amd64"] = payload
	dst := filepath.Join(t.TempDir(), "solud")
	signed := "https://github.com/owner/team-releases/releases/download/v3.0.0/solud-linux-amd64"
	if err := r.u.download(context.Background(), signed, hex.EncodeToString(sum[:]), dst); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); string(got) != string(payload) {
		t.Fatalf("downloaded %q", got)
	}
	r.cloud.files.bodies[r.team+"download/v3.0.0/solud-linux-amd64"] = []byte("tampered")
	if err := r.u.download(context.Background(), signed, hex.EncodeToString(sum[:]), dst+"2"); err == nil {
		t.Fatal("bytes that do not match the signed digest were accepted")
	}
	r.assertOnlyTeam(t)
}

// Without the field (an older cloud, a cloud without the team release token,
// or any non-team answer) a public build never contacts a team URL.
func TestPublicBuildWithoutTheFieldNeverContactsATeamURL(t *testing.T) {
	for name, body := range map[string]string{
		"team verdict, no field": `{"team":true,"tier":"team"}`,
		"non-team, with field":   `{"team":false,"tier":"account","teamUpdates":"` + cloudNamedTeamPath + `"}`,
		"a full URL":             `{"team":true,"tier":"team","teamUpdates":"https://evil.test/api/x/"}`,
		"a scheme-relative URL":  `{"team":true,"tier":"team","teamUpdates":"//evil.test/api/x/"}`,
		"a dot-dot path":         `{"team":true,"tier":"team","teamUpdates":"/api/../x/"}`,
		"a query":                `{"team":true,"tier":"team","teamUpdates":"/api/x?y=1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			r := publicBuildOnTeamMachine(t)
			r.cloud.answer(testTeamCloud, body)
			for i := 0; i < 3; i++ {
				r.e.refresh(context.Background(), "")
				r.check(t, releases.FlavorPublic, "2.0.0")
			}
			r.assertOnlyPublic(t)
			if p := r.e.cloudTeamUpdatesPath(testTeamCloud); p != "" {
				t.Fatalf("cached path %q", p)
			}
			if got, _ := os.ReadFile(filepath.Join(r.data, entitlementsFile)); strings.Contains(string(got), "teamUpdates") {
				t.Fatalf("entitlements.json kept a path: %s", got)
			}
		})
	}
}

// A team build keeps its stamped path whatever the cloud names.
func TestTeamBuildKeepsItsStampedPath(t *testing.T) {
	r := publicBuildOnTeamMachine(t)
	setBuildFlavorStamp(t, "solu-build-flavor=team")
	setTeamUpdatesPath(t, testTeamUpdatesPath)
	r.cloud.answer(testTeamCloud, `{"team":true,"tier":"team","teamUpdates":"`+cloudNamedTeamPath+`"}`)
	r.e.refresh(context.Background(), "")
	if got := r.u.teamUpdatesPathFor(testTeamCloud); got != testTeamUpdatesPath {
		t.Fatalf("team build path = %q, want its stamp", got)
	}
}

// The cached path counts only for the cloud that named it, only from a
// machine-token answer with a team verdict, and only while it is recent.
func TestCloudNamedPathIsBoundToItsCloud(t *testing.T) {
	r := publicBuildOnTeamMachine(t)
	r.cloud.answer(testTeamCloud, `{"team":true,"tier":"team","teamUpdates":"`+cloudNamedTeamPath+`"}`)
	r.e.refresh(context.Background(), "")
	r.check(t, releases.FlavorTeam, "3.0.0")
	r.cloud.requests()

	for _, base := range []string{testTeamCloud, testTeamCloud + "/", "HTTPS://CLOUD.TEST"} {
		if got := r.e.cloudTeamUpdatesPath(base); got != cloudNamedTeamPath {
			t.Errorf("%s: path %q", base, got)
		}
	}
	for _, base := range []string{"", otherTeamCloud, "http://cloud.test", "https://cloud.test:8443", "https://cloud.test/x", "https://user@cloud.test"} {
		if got := r.e.cloudTeamUpdatesPath(base); got != "" {
			t.Errorf("%s: path %q, want none", base, got)
		}
	}

	// Re-paired to another cloud: the cached path never sends the token
	// there (or anywhere), and the install stays on its version.
	r.u.SetCloudCreds(otherTeamCloud, testMachineToken)
	_, err := r.u.Check(context.Background())
	if !errors.Is(err, errTeamUpdatesNoSource) {
		t.Fatalf("Check after re-pairing: %v", err)
	}
	if urls, _ := r.cloud.requests(); len(urls) != 0 {
		t.Fatalf("requests after re-pairing: %v", urls)
	}
	if r.u.channelSnapshot() != releases.FlavorTeam {
		t.Fatal("re-pairing alone moved the channel")
	}

	// A stale answer from the right cloud counts no more.
	r.u.SetCloudCreds(testTeamCloud, testMachineToken)
	r.e.now = func() time.Time { return time.Now().Add(entitlementsTeamUpdatesMaxAge + time.Hour) }
	if _, err := r.u.Check(context.Background()); !errors.Is(err, errTeamUpdatesNoSource) {
		t.Fatalf("Check with a stale path: %v", err)
	}
	if urls, _ := r.cloud.requests(); len(urls) != 0 {
		t.Fatalf("requests with a stale path: %v", urls)
	}
	r.e.now = time.Now

	// Records that must never carry a usable path, even if a file says so.
	for name, rec := range map[string]entitlementRecord{
		"account answer": {Team: true, Tier: tierTeam, Source: "account", CheckedAt: time.Now(), TeamUpdates: cloudNamedTeamPath, TeamUpdatesOrigin: "https://cloud.test"},
		"non-team":       {Team: false, Tier: tierAccount, Source: "machine", CheckedAt: time.Now(), TeamUpdates: cloudNamedTeamPath, TeamUpdatesOrigin: "https://cloud.test"},
		"no origin":      {Team: true, Tier: tierTeam, Source: "machine", CheckedAt: time.Now(), TeamUpdates: cloudNamedTeamPath},
		"a URL":          {Team: true, Tier: tierTeam, Source: "machine", CheckedAt: time.Now(), TeamUpdates: "https://evil.test/x/", TeamUpdatesOrigin: "https://cloud.test"},
	} {
		r.e.mu.Lock()
		r.e.record = rec
		r.e.mu.Unlock()
		if got := r.e.cloudTeamUpdatesPath(testTeamCloud); got != "" {
			t.Errorf("%s: path %q", name, got)
		}
	}

	// The new cloud's own team answer names its endpoint for itself only.
	r.e.mu.Lock()
	r.e.cloud = func() (string, string) { return otherTeamCloud, testMachineToken }
	r.e.mu.Unlock()
	r.cloud.answer(otherTeamCloud, `{"team":true,"tier":"team","teamUpdates":"`+cloudNamedTeamPath+`"}`)
	r.e.refresh(context.Background(), "")
	if r.e.cloudTeamUpdatesPath(testTeamCloud) != "" || r.e.cloudTeamUpdatesPath(otherTeamCloud) != cloudNamedTeamPath {
		t.Fatal("the path is not bound to the cloud that named it")
	}
}

// Losing the team entitlement puts a public build back on the public channel
// cleanly, and regaining it moves it back: neither channel's manifest, pin or
// control file is applied on the other.
func TestPublicBuildLosesAndRegainsTheTeamChannelCleanly(t *testing.T) {
	r := publicBuildOnTeamMachine(t)
	team := `{"team":true,"tier":"team","teamUpdates":"` + cloudNamedTeamPath + `"}`
	r.cloud.answer(testTeamCloud, team)
	r.e.refresh(context.Background(), "")
	if st := r.check(t, releases.FlavorTeam, "3.0.0"); !st.Required || r.u.control == nil || r.u.control.Message != "team-control" {
		t.Fatalf("team: %+v control %+v", st, r.u.control)
	}
	r.assertOnlyTeam(t)

	time.Sleep(2 * time.Millisecond) // a strictly newer verdict
	r.cloud.answer(testTeamCloud, `{"team":false,"tier":"account"}`)
	r.e.refresh(context.Background(), "")
	if r.e.cloudTeamUpdatesPath(testTeamCloud) != "" {
		t.Fatal("a non-team answer kept the path")
	}
	st := r.check(t, releases.FlavorPublic, "2.0.0")
	if st.Required || r.u.control == nil || r.u.control.Message != "public-control" {
		t.Fatalf("public after losing team: %+v control %+v", st, r.u.control)
	}
	if r.u.manifest.Flavor != releases.FlavorPublic || r.storedChannel(t) != releases.FlavorPublic {
		t.Fatalf("manifest %+v stored %q", r.u.manifest, r.storedChannel(t))
	}
	r.assertOnlyPublic(t)
	// A pinned (rollback) manifest now comes from the public repo and must
	// be signed public: the team release is refused there.
	r.cloud.files.bodies[r.public+"download/v0.9.0/update.json"] = flavorManifest(releases.FlavorTeam, "0.9.0")
	r.cloud.files.bodies[r.public+"download/v0.9.0/update.json.sig"] = signB64(r.priv, flavorManifest(releases.FlavorTeam, "0.9.0"))
	var fe *manifestFlavorError
	if _, err := r.u.fetchPinnedManifest(context.Background(), "0.9.0"); !errors.As(err, &fe) {
		t.Fatalf("pinned team manifest on the public channel: %v", err)
	}
	r.assertOnlyPublic(t)

	time.Sleep(2 * time.Millisecond)
	r.cloud.answer(testTeamCloud, team)
	r.e.refresh(context.Background(), "")
	if st := r.check(t, releases.FlavorTeam, "3.0.0"); !st.Required || r.u.control.Message != "team-control" {
		t.Fatalf("team again: %+v control %+v", st, r.u.control)
	}
	// Pinned on the team channel: a public release is refused there too.
	r.cloud.files.bodies[r.team+"download/v0.9.0/update.json"] = flavorManifest(releases.FlavorPublic, "0.9.0")
	r.cloud.files.bodies[r.team+"download/v0.9.0/update.json.sig"] = signB64(r.priv, flavorManifest(releases.FlavorPublic, "0.9.0"))
	if _, err := r.u.fetchPinnedManifest(context.Background(), "0.9.0"); !errors.As(err, &fe) {
		t.Fatalf("pinned public manifest on the team channel: %v", err)
	}
	r.assertOnlyTeam(t)
}

// On the cloud-named team channel, the latest alias must still be signed for
// the team: a public manifest served there is refused.
func TestPublicBuildOnTheTeamChannelRefusesAPublicManifest(t *testing.T) {
	r := publicBuildOnTeamMachine(t)
	serveRelease(r.cloud.files.bodies, r.priv, r.team, releases.FlavorPublic, "3.0.0")
	r.cloud.answer(testTeamCloud, `{"team":true,"tier":"team","teamUpdates":"`+cloudNamedTeamPath+`"}`)
	r.e.refresh(context.Background(), "")
	_, err := r.u.Check(context.Background())
	var fe *manifestFlavorError
	if !errors.As(err, &fe) || r.u.Status().Available {
		t.Fatalf("a public manifest on the team channel: %v %+v", err, r.u.Status())
	}
}

// run() asks the cloud as soon as the machine is paired, rather than on its
// next refresh timer: at boot the cloud credentials arrive after it starts.
func TestEntitlementsRefreshWhenTheCloudCredsArrive(t *testing.T) {
	cloud := newFakeTeamCloud()
	cloud.answer(testTeamCloud, `{"team":true,"tier":"team","teamUpdates":"`+cloudNamedTeamPath+`"}`)
	var mu sync.Mutex
	base := ""
	changed := make(chan struct{})
	e := newTeamEntitlements(t.TempDir(), func() (string, string) {
		mu.Lock()
		defer mu.Unlock()
		if base == "" {
			return "", ""
		}
		return base, testMachineToken
	})
	// A cached verdict: run() would otherwise wait a full interval.
	e.record = entitlementRecord{Team: true, Tier: tierTeam, Source: "machine", CheckedAt: time.Now()}
	e.client = &http.Client{Transport: cloud}
	e.credsChanged = func() <-chan struct{} {
		mu.Lock()
		defer mu.Unlock()
		return changed
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.run(ctx)
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	base = testTeamCloud
	close(changed)
	changed = make(chan struct{})
	mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for e.cloudTeamUpdatesPath(testTeamCloud) == "" {
		if time.Now().After(deadline) {
			t.Fatal("pairing did not trigger an entitlements refresh")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
