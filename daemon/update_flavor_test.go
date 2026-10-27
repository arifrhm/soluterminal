package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"soluterminal-shared/releases"
)

// setBuildFlavorStamp swaps the -ldflags stamp for one test.
func setBuildFlavorStamp(t *testing.T, stamp string) {
	t.Helper()
	prev := buildFlavorStamp
	buildFlavorStamp = stamp
	t.Cleanup(func() { buildFlavorStamp = prev })
}

// flavorKey installs a fresh manifest signing key for one test.
func flavorKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOLU_UPDATE_MANIFEST_PUBKEY", base64.StdEncoding.EncodeToString(pub))
	return priv
}

func signB64(priv ed25519.PrivateKey, body []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body)))
}

// flavorManifest is a signed-shape manifest for flavor ("" = published before
// flavours). It offers a daemon binary and a shell installer.
func flavorManifest(flavor, version string) []byte {
	field := ""
	if flavor != "" {
		field = `"flavor":"` + flavor + `",`
	}
	return []byte(`{` + field + `"version":"` + version + `","platforms":{"` + platformKeyCurrent() +
		`":{"url":"https://cdn.test/solud-` + version + `","sha256":"abc"}},"shell":{"version":"` + version +
		`","platforms":{"windows-amd64":{"url":"https://cdn.test/setup-` + version + `.exe","sha256":"def"}}}}`)
}

// recordingRoundTripper serves fixed bodies and records every URL requested
// and the Authorization header it carried.
type recordingRoundTripper struct {
	mu     sync.Mutex
	bodies map[string][]byte
	status map[string]int // overrides the status for a URL
	seen   []string
	auth   map[string]string
}

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, req.URL.String())
	if r.auth == nil {
		r.auth = map[string]string{}
	}
	r.auth[req.URL.String()] = req.Header.Get("Authorization")
	body, ok := r.bodies[req.URL.String()]
	status := http.StatusOK
	if !ok {
		status = http.StatusNotFound
	}
	if code, ok := r.status[req.URL.String()]; ok {
		status = code
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}, nil
}

func directFlavorUpdater(t *testing.T, buildFlavor string, body, sig []byte) *Updater {
	t.Helper()
	u := NewUpdater("1.0.0", "", "windows-amd64", nil, nil)
	u.channel = buildFlavor
	u.homeDir = t.TempDir()
	u.manifestURL = "https://updates.test/update.json"
	rt := staticRoundTripper{"https://updates.test/update.json": body}
	if sig != nil {
		rt["https://updates.test/update.json.sig"] = sig
	}
	u.httpClient = &http.Client{Transport: rt}
	return u
}

func TestBuildFlavorDefaultsToPublicAndOnlyExactTeamIsTeam(t *testing.T) {
	// The compiled-in default (no -ldflags stamp) is public.
	if got := currentBuildFlavor(); got != releases.FlavorPublic {
		t.Fatalf("unstamped build flavour = %q, want public", got)
	}
	for stamp, want := range map[string]string{
		"solu-build-flavor=team":   releases.FlavorTeam,
		"team":                     releases.FlavorTeam,
		" solu-build-flavor=team ": releases.FlavorTeam,
		"solu-build-flavor=public": releases.FlavorPublic,
		"":                         releases.FlavorPublic,
		"solu-build-flavor=":       releases.FlavorPublic,
		"solu-build-flavor=Team":   releases.FlavorPublic,
		"solu-build-flavor=teams":  releases.FlavorPublic,
		"solu-build-flavor=dev":    releases.FlavorPublic,
		"release":                  releases.FlavorPublic,
	} {
		setBuildFlavorStamp(t, stamp)
		if got := currentBuildFlavor(); got != want {
			t.Fatalf("stamp %q: flavour %q, want %q", stamp, got, want)
		}
		if got := buildProvenance()["flavor"]; got != want {
			t.Fatalf("stamp %q: provenance flavor %v, want %q", stamp, got, want)
		}
	}
}

func TestPublicSourceNeverSpellsTheTeamMarker(t *testing.T) {
	// Release tooling reads a cross-compiled binary's flavour from its bytes:
	// a team binary contains the marker, a public one never may. Only a
	// non-test source file could put it in a public binary.
	marker := buildFlavorMarkerPrefix + releases.FlavorTeam
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(src, []byte(marker)) {
			t.Fatalf("%s spells %q; public binaries would carry the team marker", name, marker)
		}
	}
}

func TestEachFlavourPollsOnlyItsOwnSource(t *testing.T) {
	setTeamUpdatesPath(t, testTeamUpdatesPath)
	t.Setenv("SOLU_UPDATE_MANIFEST_URL", "")
	teamBase := teamUpdatesBase(testTeamCloud)
	for _, tc := range []struct {
		stamp, flavor, base string
	}{
		{"solu-build-flavor=public", releases.FlavorPublic, releases.ReleasesBase(releases.Repo)},
		{"solu-build-flavor=team", releases.FlavorTeam, teamBase + "releases/"},
	} {
		t.Run(tc.flavor, func(t *testing.T) {
			setBuildFlavorStamp(t, tc.stamp)
			priv := flavorKey(t)
			u := NewUpdater("1.0.0", "", "windows-amd64", nil, nil)
			u.homeDir = t.TempDir()
			u.SetCloudCreds(testTeamCloud, testMachineToken)
			if u.channel != tc.flavor {
				t.Fatalf("updater channel = %q, want %q", u.channel, tc.flavor)
			}
			if want := tc.base + "latest/download/update.json"; u.manifestURLNow() != want {
				t.Fatalf("manifest URL = %q, want %q", u.manifestURLNow(), want)
			}
			if !strings.HasPrefix(u.controlURL(), tc.base) {
				t.Fatalf("control URL %q is outside %s", u.controlURL(), tc.base)
			}

			// Serve BOTH sources, each with a newer release of its own
			// flavour, and run a real check: the build must only ever request
			// its own source and must end up on its own flavour's release.
			bodies := map[string][]byte{}
			serveRelease(bodies, priv, releases.ReleasesBase(releases.Repo), releases.FlavorPublic, "2.0.0")
			serveRelease(bodies, priv, teamBase+"releases/", releases.FlavorTeam, "3.0.0")
			rt := &recordingRoundTripper{bodies: bodies}
			u.httpClient = &http.Client{Transport: &teamUpdatesAuth{next: rt, creds: u.cloudCreds}}
			st, err := u.Check(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{releases.FlavorPublic: "2.0.0", releases.FlavorTeam: "3.0.0"}[tc.flavor]
			if st.Latest != want || !st.Available || st.ShellLatest != want {
				t.Fatalf("status = latest %q available %v shell %q, want own release %s", st.Latest, st.Available, st.ShellLatest, want)
			}
			for _, url := range rt.seen {
				if !strings.HasPrefix(url, tc.base) &&
					!(tc.flavor == releases.FlavorPublic && strings.HasPrefix(url, releases.ReleasesBase(releases.LegacyRepo))) {
					t.Fatalf("%s build requested %s outside its source", tc.flavor, url)
				}
				// The machine token goes to the cloud's team endpoint only.
				wantAuth := ""
				if tc.flavor == releases.FlavorTeam {
					wantAuth = "Bearer " + testMachineToken
				}
				if got := rt.auth[url]; got != wantAuth {
					t.Fatalf("%s: Authorization %q, want %q", url, got, wantAuth)
				}
			}
		})
	}
}

func TestBuildWithoutTheTeamEndpointNeverPollsTeam(t *testing.T) {
	setTeamUpdatesPath(t, "")
	t.Setenv("SOLU_UPDATE_MANIFEST_URL", "")
	setBuildFlavorStamp(t, "solu-build-flavor=team")
	u := NewUpdater("1.0.0", "", "", nil, nil)
	u.SetCloudCreds(testTeamCloud, testMachineToken)
	if u.manifestURLNow() != "" || u.teamViaCloudNow() {
		t.Fatalf("a build without the team endpoint polls %q", u.manifestURLNow())
	}
	u.StartBackgroundPoll(context.Background())
	u.mu.Lock()
	reason := u.fleetPollDisabled
	u.mu.Unlock()
	if reason != "no-releases-repo" {
		t.Fatalf("poll disabled reason = %q", reason)
	}
}

func TestManifestFlavourAcceptRejectMatrix(t *testing.T) {
	for _, build := range []string{releases.FlavorPublic, releases.FlavorTeam} {
		for _, manifest := range []string{releases.FlavorPublic, releases.FlavorTeam, "", "Team", "PUBLIC", "nightly"} {
			t.Run(build+"/"+manifest, func(t *testing.T) {
				priv := flavorKey(t)
				body := flavorManifest(manifest, "2.0.0")
				u := directFlavorUpdater(t, build, body, signB64(priv, body))
				m, err := u.fetchManifest(context.Background())
				if manifest == build {
					if err != nil || m.Flavor != build {
						t.Fatalf("own flavour refused: %v", err)
					}
					return
				}
				var fe *manifestFlavorError
				if !errors.As(err, &fe) || m != nil {
					t.Fatalf("manifest flavour %q accepted by the %s build (err %v)", manifest, build, err)
				}
				if isTransientUpdateErr(err) {
					t.Fatal("a flavour mismatch must never be retried silently")
				}
			})
		}
	}
}

func TestMismatchedFlavourSurfacesWithoutChangingStatus(t *testing.T) {
	disableControlSignature(t)
	u := directFlavorUpdater(t, releases.FlavorPublic, flavorManifest(releases.FlavorPublic, "2.0.0"), nil)
	if st, err := u.Check(context.Background()); err != nil || st.Latest != "2.0.0" || !st.Available || !st.ShellAvailable {
		t.Fatalf("own flavour: %+v, %v", st, err)
	}
	// The repo now serves a newer TEAM manifest (a mis-publish, or a URL
	// override pointing at the team repo).
	u.httpClient = &http.Client{Transport: staticRoundTripper{
		"https://updates.test/update.json": flavorManifest(releases.FlavorTeam, "3.0.0"),
	}}
	st, err := u.Check(context.Background())
	var fe *manifestFlavorError
	if !errors.As(err, &fe) {
		t.Fatalf("team manifest accepted by a public build: %v", err)
	}
	if st.Latest != "2.0.0" || st.ShellLatest != "2.0.0" || !strings.Contains(st.LastError, "team build") {
		t.Fatalf("status after refusal = %+v", st)
	}
	if u.manifest == nil || u.manifest.Version != "2.0.0" || u.manifest.Flavor != releases.FlavorPublic {
		t.Fatalf("cached manifest replaced by the refused one: %+v", u.manifest)
	}
}

func TestTamperedFlavourFailsTheSignature(t *testing.T) {
	for _, tc := range []struct{ signed, served, build string }{
		{releases.FlavorTeam, releases.FlavorPublic, releases.FlavorPublic},
		{releases.FlavorPublic, releases.FlavorTeam, releases.FlavorTeam},
	} {
		priv := flavorKey(t)
		signed := flavorManifest(tc.signed, "2.0.0")
		served := bytes.Replace(signed, []byte(`"flavor":"`+tc.signed+`"`), []byte(`"flavor":"`+tc.served+`"`), 1)
		if bytes.Equal(signed, served) {
			t.Fatal("fixture did not change the flavour")
		}
		u := directFlavorUpdater(t, tc.build, served, signB64(priv, signed))
		m, err := u.fetchManifest(context.Background())
		if err == nil || m != nil || !strings.Contains(err.Error(), "signature verification failed") {
			t.Fatalf("%s manifest re-labelled %s was not refused by its signature: %v", tc.signed, tc.served, err)
		}
	}
}

func TestPinnedPathRefusesOtherFlavourAndKeepsCacheClean(t *testing.T) {
	priv := flavorKey(t)
	body := flavorManifest(releases.FlavorTeam, "2.0.0")
	u := NewUpdater("1.0.0", "", "", nil, nil)
	u.channel = releases.FlavorPublic
	u.manifestURL = "https://updates.test/releases/latest/download/update.json"
	base := "https://updates.test/releases/download/v2.0.0/"
	u.httpClient = &http.Client{Transport: staticRoundTripper{
		u.manifestURL:            body,
		base + "update.json":     body,
		base + "update.json.sig": signB64(priv, body),
	}}
	var fe *manifestFlavorError
	if _, err := u.fetchManifest(context.Background()); !errors.As(err, &fe) {
		t.Fatalf("pinned team manifest accepted by a public build: %v", err)
	}
	if u.verifiedManifest != nil {
		t.Fatal("the refused manifest reached the verified-manifest cache")
	}
}

func TestRollbackRefusesLegacyAndOtherFlavourReleases(t *testing.T) {
	for _, flavor := range []string{"", releases.FlavorTeam} {
		name := flavor
		if name == "" {
			name = "pre-flavour"
		}
		t.Run(name, func(t *testing.T) {
			candidate := capabilityScript("1.0.0", `{}`)
			u, store, bin, rt := rollbackFixture(t, "2.0.0", "1.0.0", candidate)
			// Re-sign the pinned 1.0.0 manifest with a key of our own, now
			// naming another flavour (or none, like every release published
			// before flavours).
			priv := flavorKey(t)
			hash := sha256.Sum256(candidate)
			field := ""
			if flavor != "" {
				field = `"flavor":"` + flavor + `",`
			}
			body := []byte(`{` + field + `"version":"1.0.0","platforms":{"` + platformKeyCurrent() +
				`":{"url":"https://cdn.test/solud","sha256":"` + hex.EncodeToString(hash[:]) + `"}}}`)
			base := "https://updates.test/releases/download/v1.0.0/"
			rt[base+"update.json"] = body
			rt[base+"update.json.sig"] = signB64(priv, body)

			err := u.Rollback(context.Background(), rollbackRequest{TargetVersion: "1.0.0"}, func(int) {})
			var fe *manifestFlavorError
			if !errors.As(err, &fe) {
				t.Fatalf("rollback into a %s release was not refused for its flavour: %v", name, err)
			}
			if store.Snapshot().Hold != nil {
				t.Fatal("a refused rollback wrote an update hold")
			}
			if got, _ := os.ReadFile(bin); string(got) != "current" {
				t.Fatalf("a refused rollback changed the installed binary: %q", got)
			}
		})
	}
}

func TestHoldAndDowngradeFloorIgnoreOtherFlavourReleases(t *testing.T) {
	disableControlSignature(t)
	t.Run("hold", func(t *testing.T) {
		store, _ := newUpdateRollbackStore(t.TempDir())
		_ = store.SetHold(rollbackHold{Version: "2.0.0", Reason: "rolled back"}, time.Now())
		u := directFlavorUpdater(t, releases.FlavorPublic, flavorManifest(releases.FlavorPublic, "2.0.0"), nil)
		u.rollback = store
		st, err := u.Check(context.Background())
		if err != nil || st.Available || st.HoldVersion != "2.0.0" {
			t.Fatalf("held own release: %+v, %v", st, err)
		}
		// A newer release of the OTHER flavour must neither lift the hold
		// nor become available; the hold stays exactly as the user left it.
		u.httpClient = &http.Client{Transport: staticRoundTripper{
			"https://updates.test/update.json": flavorManifest(releases.FlavorTeam, "3.0.0"),
		}}
		st, err = u.Check(context.Background())
		var fe *manifestFlavorError
		if !errors.As(err, &fe) || st.Available || st.Latest != "2.0.0" {
			t.Fatalf("other-flavour release while held: %+v, %v", st, err)
		}
		if hold, ok := store.ActiveHold(time.Now()); !ok || hold.Version != "2.0.0" {
			t.Fatalf("hold changed by a refused manifest: %+v %v", hold, ok)
		}
		if err := u.Apply(context.Background(), func(int) {}, true); err == nil {
			t.Fatal("apply proceeded on a held release after a refused manifest")
		}
	})
	t.Run("installer downgrade floor", func(t *testing.T) {
		// An installer downgraded this daemon; only the other flavour has a
		// newer release. The local floor must not force anything.
		u := directFlavorUpdater(t, releases.FlavorTeam, flavorManifest(releases.FlavorPublic, "3.0.0"), nil)
		u.localFloor = "2.0.0"
		st, err := u.Check(context.Background())
		var fe *manifestFlavorError
		if !errors.As(err, &fe) || st.Available || st.Required {
			t.Fatalf("downgraded team daemon offered the public release: %+v, %v", st, err)
		}
		if err := u.Apply(context.Background(), func(int) {}, true); err == nil {
			t.Fatal("apply proceeded with only another flavour's manifest")
		}
	})
}

func TestShellUpdateIgnoresOtherFlavourInstaller(t *testing.T) {
	disableControlSignature(t)
	for _, tc := range []struct{ build, manifest string }{
		{releases.FlavorPublic, releases.FlavorTeam},
		{releases.FlavorTeam, releases.FlavorPublic},
		{releases.FlavorPublic, ""},
	} {
		u := directFlavorUpdater(t, tc.build, flavorManifest(tc.manifest, "9.0.0"), nil)
		u.shellCurrent = "1.0.0"
		st, err := u.Check(context.Background())
		var fe *manifestFlavorError
		if !errors.As(err, &fe) || st.ShellAvailable || st.ShellLatest != "" {
			t.Fatalf("%s build offered the %q installer: %+v, %v", tc.build, tc.manifest, st, err)
		}
		if _, err := u.ApplyShell(context.Background(), t.TempDir(), false); err == nil {
			t.Fatalf("%s build staged the %q installer", tc.build, tc.manifest)
		}
	}
}
