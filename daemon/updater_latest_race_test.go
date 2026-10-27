package main

// Deterministic reproduction of the v0.0.986/v0.0.987 publish race: GitHub
// serves /releases/latest/download/<asset> through PER-ASSET redirect caches,
// so right after publishing v0.0.987 the latest alias served v0.0.986's
// update.json alongside v0.0.987's update.json.sig — a correctly-rejected
// mixed pair that surfaced as `manifest signature verification failed` and
// delayed activation. These tests pin the resolve-then-pin protocol that
// makes the race unobservable: the alias only resolves a version, and
// verified bytes always come from the release's immutable per-tag URLs.

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
)

// recordingRT wraps a mutable staticRoundTripper map and records every
// requested URL, so tests can assert which URLs were — and were NOT —
// fetched, and can flip the map between Check calls to simulate GitHub's
// latest-alias caches converging.
type recordingRT struct {
	responses staticRoundTripper
	statuses  map[string]int
	requested []string
}

func (r *recordingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.requested = append(r.requested, req.URL.String())
	if status := r.statuses[req.URL.String()]; status != 0 {
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     http.Header{},
		}, nil
	}
	return r.responses.RoundTrip(req)
}

func (r *recordingRT) requestedURL(url string) bool {
	for _, got := range r.requested {
		if got == url {
			return true
		}
	}
	return false
}

const (
	raceLatestURL    = "https://github.test/owner/repo/releases/latest/download/update.json"
	raceLatestSigURL = raceLatestURL + ".sig"
	racePinnedBase   = "https://github.test/owner/repo/releases/download/"
	// Check() polls the signed control file alongside the manifest; its URL
	// derives from the same repo base (controlURL), 404s in these tests, and
	// is not part of the manifest-layer request accounting.
	raceControlURL = racePinnedBase + "control/update-control.json"
)

func raceManifestBody(version string) []byte {
	return []byte(`{"flavor":"public","version":"` + version + `","platforms":{"` + platformKeyCurrent() +
		`":{"url":"https://example.test/solud","sha256":"abc"}}}`)
}

func raceSig(priv ed25519.PrivateKey, body []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body)))
}

// raceKey generates a signing key and configures its public half for the
// test's updater via SOLU_UPDATE_MANIFEST_PUBKEY.
func raceKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOLU_UPDATE_MANIFEST_PUBKEY", base64.StdEncoding.EncodeToString(pub))
	return priv
}

func newRaceUpdater(rt http.RoundTripper) *Updater {
	u := NewUpdater("0.0.1", "", "", nil, nil)
	u.manifestURL = raceLatestURL
	u.httpClient = &http.Client{Transport: rt}
	return u
}

// The incident shape: the two latest-alias caches disagree about which
// release they redirect to. The updater must still verify and offer the
// resolved release (no spurious signature failure), and must converge to
// the new release one poll after the alias does.
func TestLatestAliasMixedVersionRaceRecovers(t *testing.T) {
	priv := raceKey(t)
	b986 := raceManifestBody("0.0.986")
	b987 := raceManifestBody("0.0.987")

	rt := &recordingRT{responses: staticRoundTripper{
		raceLatestURL:    b986,
		raceLatestSigURL: raceSig(priv, b987), // the poisoned mixed pair
		// The immutable per-tag pairs are always internally consistent.
		racePinnedBase + "v0.0.986/update.json":     b986,
		racePinnedBase + "v0.0.986/update.json.sig": raceSig(priv, b986),
		racePinnedBase + "v0.0.987/update.json":     b987,
		racePinnedBase + "v0.0.987/update.json.sig": raceSig(priv, b987),
	}}
	u := newRaceUpdater(rt)

	st, err := u.Check(context.Background())
	if err != nil {
		t.Fatalf("Check during mixed-latest race: %v", err)
	}
	if !st.Available || st.Latest != "0.0.986" {
		t.Fatalf("Available=%v Latest=%q, want true/0.0.986", st.Available, st.Latest)
	}
	if rt.requestedURL(raceLatestSigURL) {
		t.Fatal("latest-alias .sig was fetched; verified bytes must come only from immutable per-tag URLs")
	}

	// The alias caches converge; recovery must take exactly one poll.
	rt.responses[raceLatestURL] = b987
	st, err = u.Check(context.Background())
	if err != nil {
		t.Fatalf("Check after alias converged: %v", err)
	}
	if !st.Available || st.Latest != "0.0.987" {
		t.Fatalf("Available=%v Latest=%q, want true/0.0.987", st.Available, st.Latest)
	}
}

// Mid-publish, the pinned tag exists but update.json.sig hasn't uploaded
// yet: that 404 must ride the transient streak (suppressed from the UI) and
// clear on the next poll once the asset lands.
func TestLatestAliasMidPublishSigMissingIsTransient(t *testing.T) {
	priv := raceKey(t)
	b987 := raceManifestBody("0.0.987")

	rt := &recordingRT{responses: staticRoundTripper{
		raceLatestURL:                           b987,
		racePinnedBase + "v0.0.987/update.json": b987,
		// pinned update.json.sig deliberately absent (404)
	}}
	u := newRaceUpdater(rt)

	st, err := u.Check(context.Background())
	if err == nil {
		t.Fatal("want error while the pinned .sig is missing")
	}
	if !isTransientUpdateErr(err) {
		t.Fatalf("mid-publish 404 on the pinned .sig must be transient, got %v", err)
	}
	if st.LastError != "" {
		t.Fatalf("LastError = %q, want empty (suppressed below maxTransientFailures)", st.LastError)
	}

	rt.responses[racePinnedBase+"v0.0.987/update.json.sig"] = raceSig(priv, b987)
	st, err = u.Check(context.Background())
	if err != nil {
		t.Fatalf("Check after sig uploaded: %v", err)
	}
	if !st.Available || st.Latest != "0.0.987" {
		t.Fatalf("Available=%v Latest=%q, want true/0.0.987", st.Available, st.Latest)
	}
	if u.transientFailures != 0 {
		t.Fatalf("transientFailures = %d, want 0 after recovery", u.transientFailures)
	}
}

// Mid-publish, the alias can resolve before the pinned update.json asset has
// uploaded. That immutable-asset 404 follows the same transient streak as a
// missing signature and clears as soon as the manifest lands.
func TestLatestAliasMidPublishManifestMissingIsTransient(t *testing.T) {
	priv := raceKey(t)
	b987 := raceManifestBody("0.0.987")

	rt := &recordingRT{responses: staticRoundTripper{
		raceLatestURL: b987,
		racePinnedBase + "v0.0.987/update.json.sig": raceSig(priv, b987),
		// pinned update.json deliberately absent (404)
	}}
	u := newRaceUpdater(rt)

	st, err := u.Check(context.Background())
	if err == nil {
		t.Fatal("want error while the pinned manifest is missing")
	}
	if !isTransientUpdateErr(err) {
		t.Fatalf("mid-publish 404 on the pinned manifest must be transient, got %v", err)
	}
	if st.LastError != "" {
		t.Fatalf("LastError = %q, want empty (suppressed below maxTransientFailures)", st.LastError)
	}
	if u.transientFailures != 1 {
		t.Fatalf("transientFailures = %d, want 1 after the missing manifest", u.transientFailures)
	}

	rt.responses[racePinnedBase+"v0.0.987/update.json"] = b987
	st, err = u.Check(context.Background())
	if err != nil {
		t.Fatalf("Check after manifest uploaded: %v", err)
	}
	if !st.Available || st.Latest != "0.0.987" {
		t.Fatalf("Available=%v Latest=%q, want true/0.0.987", st.Available, st.Latest)
	}
	if u.transientFailures != 0 {
		t.Fatalf("transientFailures = %d, want 0 after recovery", u.transientFailures)
	}
}

// A retryable status while resolving the mutable alias is a network-layer
// failure, not an authenticity failure. It remains suppressed below the
// transient threshold and recovers on the next successful resolve.
func TestLatestAliasResolveServerErrorIsTransient(t *testing.T) {
	priv := raceKey(t)
	b987 := raceManifestBody("0.0.987")

	rt := &recordingRT{
		responses: staticRoundTripper{
			raceLatestURL:                               b987,
			racePinnedBase + "v0.0.987/update.json":     b987,
			racePinnedBase + "v0.0.987/update.json.sig": raceSig(priv, b987),
		},
		statuses: map[string]int{raceLatestURL: http.StatusInternalServerError},
	}
	u := newRaceUpdater(rt)

	st, err := u.Check(context.Background())
	if err == nil {
		t.Fatal("want error while the latest alias returns 500")
	}
	if !isTransientUpdateErr(err) {
		t.Fatalf("500 while resolving the latest alias must be transient, got %v", err)
	}
	if st.LastError != "" {
		t.Fatalf("LastError = %q, want empty (suppressed below maxTransientFailures)", st.LastError)
	}
	if u.transientFailures != 1 {
		t.Fatalf("transientFailures = %d, want 1 after the resolve failure", u.transientFailures)
	}

	delete(rt.statuses, raceLatestURL)
	st, err = u.Check(context.Background())
	if err != nil {
		t.Fatalf("Check after latest alias recovered: %v", err)
	}
	if !st.Available || st.Latest != "0.0.987" {
		t.Fatalf("Available=%v Latest=%q, want true/0.0.987", st.Available, st.Latest)
	}
	if u.transientFailures != 0 {
		t.Fatalf("transientFailures = %d, want 0 after recovery", u.transientFailures)
	}
}

// A bad signature on an immutable pair can never be a publish race — it is
// tampering or misconfiguration, and must stay fail-closed: non-transient,
// surfaced on the very first Check.
func TestPinnedPairBadSignatureSurfacesImmediately(t *testing.T) {
	raceKey(t)
	b986 := raceManifestBody("0.0.986")

	rt := &recordingRT{responses: staticRoundTripper{
		raceLatestURL:                               b986,
		racePinnedBase + "v0.0.986/update.json":     b986,
		racePinnedBase + "v0.0.986/update.json.sig": []byte(base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))),
	}}
	u := newRaceUpdater(rt)

	st, err := u.Check(context.Background())
	if err == nil {
		t.Fatal("want error for a bad signature on the immutable pair")
	}
	if isTransientUpdateErr(err) {
		t.Fatalf("bad pinned signature must be non-transient, got %v", err)
	}
	if st.LastError == "" {
		t.Fatal("bad pinned signature must surface on the FIRST Check (fail-closed)")
	}
}

// The resolve body is unverified, so its version string must be strictly
// validated before it is spliced into a pinned URL: a malformed or empty
// version fails non-transiently and provably never reaches URL space.
func TestResolveVersionRejectsMalformed(t *testing.T) {
	raceKey(t)
	for _, body := range []string{`{"version":"../evil"}`, `{"version":""}`} {
		rt := &recordingRT{responses: staticRoundTripper{
			raceLatestURL: []byte(body),
		}}
		u := newRaceUpdater(rt)

		_, err := u.fetchManifest(context.Background())
		if err == nil {
			t.Fatalf("body %s: want error", body)
		}
		if isTransientUpdateErr(err) {
			t.Fatalf("body %s: malformed resolved version must be non-transient, got %v", body, err)
		}
		for _, got := range rt.requested {
			if strings.Contains(got, "evil") || strings.Contains(got, "/releases/download/") {
				t.Fatalf("body %s: unverified version reached URL space: %s", body, got)
			}
		}
	}
}

// Steady state: once a version's immutable pair has been verified, later
// polls that resolve to the same version are served from the cache off a
// single resolve GET — no re-fetch of bytes that cannot have changed.
func TestLatestAliasSteadyStateReusesVerifiedManifest(t *testing.T) {
	priv := raceKey(t)
	b986 := raceManifestBody("0.0.986")

	rt := &recordingRT{responses: staticRoundTripper{
		raceLatestURL:                               b986,
		racePinnedBase + "v0.0.986/update.json":     b986,
		racePinnedBase + "v0.0.986/update.json.sig": raceSig(priv, b986),
	}}
	u := newRaceUpdater(rt)

	if _, err := u.Check(context.Background()); err != nil {
		t.Fatalf("first Check: %v", err)
	}

	// Remove the pinned pair entirely: the steady-state poll below must not
	// need it.
	delete(rt.responses, racePinnedBase+"v0.0.986/update.json")
	delete(rt.responses, racePinnedBase+"v0.0.986/update.json.sig")
	before := len(rt.requested)

	st, err := u.Check(context.Background())
	if err != nil {
		t.Fatalf("steady-state Check: %v", err)
	}
	if !st.Available || st.Latest != "0.0.986" {
		t.Fatalf("Available=%v Latest=%q, want true/0.0.986", st.Available, st.Latest)
	}
	var manifestReqs []string
	for _, got := range rt.requested[before:] {
		if got == raceControlURL {
			continue
		}
		manifestReqs = append(manifestReqs, got)
	}
	if len(manifestReqs) != 1 || manifestReqs[0] != raceLatestURL {
		t.Fatalf("steady-state manifest requests = %v, want exactly the one resolve GET", manifestReqs)
	}
}

// A correctly-signed pinned manifest whose version disagrees with the
// resolved version is refused: the signature proves authenticity, not that
// the release is the one the alias pointed at.
func TestPinnedVersionMismatchRejected(t *testing.T) {
	priv := raceKey(t)
	b999 := raceManifestBody("0.0.999")

	rt := &recordingRT{responses: staticRoundTripper{
		raceLatestURL:                               raceManifestBody("0.0.987"),
		racePinnedBase + "v0.0.987/update.json":     b999,
		racePinnedBase + "v0.0.987/update.json.sig": raceSig(priv, b999),
	}}
	u := newRaceUpdater(rt)

	_, err := u.fetchManifest(context.Background())
	if err == nil {
		t.Fatal("want error when the pinned manifest version disagrees with the resolved version")
	}
	if isTransientUpdateErr(err) {
		t.Fatalf("pinned version mismatch must be non-transient, got %v", err)
	}
}

func TestDetachedSigURLPreservesQuery(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://h/p/update.json", "https://h/p/update.json.sig"},
		{"https://h/p/update.json?token=1", "https://h/p/update.json.sig?token=1"},
		{"https://h/a%2Fb/update.json", "https://h/a%2Fb/update.json.sig"},
		{"://not-a-url", "://not-a-url.sig"},
	}
	for _, c := range cases {
		if got := detachedSigURL(c.in); got != c.want {
			t.Errorf("detachedSigURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
