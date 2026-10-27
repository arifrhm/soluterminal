package main

// Tests for the server-side update control plane (updater_control.go). The
// pure-function tests encode the load-bearing safety invariants:
//   - a nil/zero control is permissive (old daemon, absent control file),
//   - the kill switch (paused / halt_*) outranks the staged-rollout cohort,
//   - RolloutPercent==nil means 100 (the *int-vs-int compat landmine: an int
//     would default an absent field to 0 = ship-to-nobody = frozen fleet),
//   - rollout is deterministic per (machineID, candidate) and MONOTONIC as the
//     percentage rises (promoting 5%→100% only ever adds machines).
//
// The fail-safe tests exercise controlFallbackLocked (ride the last-known-good
// cache on a transient blip, fail SAFE to paused once it's stale). The network
// tests reuse the staticRoundTripper idiom from updater_signature_test.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"soluterminal-shared/releases"
)

// intPtr returns a pointer to i. RolloutPercent is a *int, so tests that set an
// explicit percentage (including 0, which is meaningfully different from absent)
// need an addressable value.
func intPtr(i int) *int { return &i }

// disableControlSignature clears the baked-in manifest public key (and the env
// override) so verifySignatureAt is permissive and fetchControl never tries to
// fetch <url>.sig. Mirrors disableManifestSignature from the transient test.
func disableControlSignature(t *testing.T) {
	t.Helper()
	orig := defaultUpdateManifestPubKey
	defaultUpdateManifestPubKey = ""
	t.Cleanup(func() { defaultUpdateManifestPubKey = orig })
	t.Setenv("SOLU_UPDATE_MANIFEST_PUBKEY", "")
}

func TestUpdateControlRollbackUsesOnlyHardFloors(t *testing.T) {
	rollout := 0
	forwardOnly := &UpdateControl{Paused: true, HaltAtOrAbove: "1.0.0", RolloutPercent: &rollout}
	if blocked, reason := forwardOnly.blocksRollback("1.0.0", "2.0.0"); blocked {
		t.Fatalf("forward controls blocked recovery rollback: %s", reason)
	}
	for _, control := range []*UpdateControl{{MinDaemonVer: "1.1.0"}, {HaltBelow: "1.1.0"}} {
		if blocked, _ := control.blocksRollback("1.0.0", "2.0.0"); !blocked {
			t.Fatalf("hard floor did not block rollback: %+v", control)
		}
	}
}

func TestControlURLDerivation(t *testing.T) {
	u := NewUpdater("0.0.1", "", "", nil, nil)
	u.manifestURL = releases.LatestManifestURL(releases.Repo)

	got := u.controlURL()
	want := releases.DownloadPrefix(releases.Repo) + "control/update-control.json"
	if got != want {
		t.Fatalf("controlURL() = %q, want %q", got, want)
	}

	// SOLU_UPDATE_CONTROL_URL overrides the derivation entirely.
	t.Setenv("SOLU_UPDATE_CONTROL_URL", "https://override.test/ctrl.json")
	if got := u.controlURL(); got != "https://override.test/ctrl.json" {
		t.Fatalf("controlURL() with override = %q, want the override", got)
	}
}

func TestControlBlocksApplyPaused(t *testing.T) {
	c := &UpdateControl{Paused: true}
	blocked, reason := c.blocksApply("0.0.800", "0.0.700", "machine-1")
	if !blocked {
		t.Fatal("paused control must block apply")
	}
	if reason == "" {
		t.Fatal("paused block must carry a non-empty reason")
	}
}

func TestControlBlocksApplyHaltAtOrAbove(t *testing.T) {
	c := &UpdateControl{HaltAtOrAbove: "0.0.800"}
	cases := []struct {
		candidate string
		want      bool // want blocked
	}{
		{"0.0.800", true},  // boundary: == halt_at_or_above is BLOCKED
		{"0.0.801", true},  // one patch above is blocked
		{"0.0.900", true},  // well above is blocked
		{"0.0.799", false}, // below is allowed
		{"0.0.700", false}, // well below is allowed
	}
	for _, tc := range cases {
		blocked, reason := c.blocksApply(tc.candidate, "0.0.700", "machine-1")
		if blocked != tc.want {
			t.Errorf("halt_at_or_above=0.0.800, candidate=%s: blocked=%v, want %v (reason=%q)",
				tc.candidate, blocked, tc.want, reason)
		}
		if blocked && reason == "" {
			t.Errorf("candidate=%s blocked with empty reason", tc.candidate)
		}
	}
}

func TestControlBlocksApplyHaltBelow(t *testing.T) {
	c := &UpdateControl{HaltBelow: "0.0.800"}
	cases := []struct {
		candidate string
		want      bool // want blocked
	}{
		{"0.0.799", true},  // below halt_below is blocked (downgrade guard)
		{"0.0.500", true},  // well below is blocked
		{"0.0.800", false}, // at halt_below is allowed
		{"0.0.801", false}, // above is allowed
	}
	for _, tc := range cases {
		blocked, reason := c.blocksApply(tc.candidate, "0.0.700", "machine-1")
		if blocked != tc.want {
			t.Errorf("halt_below=0.0.800, candidate=%s: blocked=%v, want %v (reason=%q)",
				tc.candidate, blocked, tc.want, reason)
		}
	}
}

func TestControlBlocksApplyNilAllows(t *testing.T) {
	// A nil *UpdateControl (new daemon meets absent control file) must never
	// block — the whole feature is dormant until a control file is published.
	blocked, reason := (*UpdateControl)(nil).blocksApply("0.0.800", "0.0.700", "machine-1")
	if blocked {
		t.Fatalf("nil control blocked apply (reason=%q); must be permissive", reason)
	}
}

func TestControlKillSwitchBeatsRollout(t *testing.T) {
	// Paused with a full rollout: paused must still win.
	c := &UpdateControl{Paused: true, RolloutPercent: intPtr(100)}
	if blocked, _ := c.blocksApply("0.0.800", "0.0.700", "machine-1"); !blocked {
		t.Fatal("paused must win even when rollout_percent=100")
	}

	// halt_at_or_above with a full rollout: the halt must still win.
	c2 := &UpdateControl{HaltAtOrAbove: "0.0.800", RolloutPercent: intPtr(100)}
	if blocked, _ := c2.blocksApply("0.0.800", "0.0.700", "machine-1"); !blocked {
		t.Fatal("halt_at_or_above must win even when rollout_percent=100")
	}
}

func TestControlRolloutAbsentMeans100(t *testing.T) {
	// THE compat invariant: RolloutPercent==nil means ship to ALL machines. A
	// regression to plain-int (absent → 0) would freeze the entire fleet.
	c := &UpdateControl{} // RolloutPercent left nil
	ids := []string{"", "machine-1", "machine-2", "abc", "def", "00000000-0000-0000-0000-000000000000"}
	for _, id := range ids {
		if !c.rolloutAllows("0.0.800", id) {
			t.Errorf("rolloutAllows(candidate, %q) = false with nil RolloutPercent; want true (absent==100)", id)
		}
	}
}

func TestControlRolloutZeroMeansNone(t *testing.T) {
	// Explicit 0 is meaningfully different from absent: ship to nobody.
	c := &UpdateControl{RolloutPercent: intPtr(0)}
	ids := []string{"", "machine-1", "machine-2", "abc", "def"}
	for _, id := range ids {
		if c.rolloutAllows("0.0.800", id) {
			t.Errorf("rolloutAllows(candidate, %q) = true with RolloutPercent=0; want false", id)
		}
	}
}

func TestControlRolloutDeterministic(t *testing.T) {
	c := &UpdateControl{RolloutPercent: intPtr(50)}
	for _, id := range []string{"machine-1", "machine-7", "machine-9999"} {
		want := c.rolloutAllows("0.0.800", id)
		for i := 0; i < 1000; i++ {
			if got := c.rolloutAllows("0.0.800", id); got != want {
				t.Fatalf("rolloutAllows nondeterministic for %q: got %v then %v", id, want, got)
			}
		}
	}
}

func TestControlRolloutMonotonicPromotion(t *testing.T) {
	// THE most important rollout test. For a fixed candidate, every machine
	// allowed at a lower percentage MUST remain allowed at every higher one:
	// promoting only ever ADDS machines, never flips one back out.
	const candidate = "0.0.800"
	const n = 5000
	percents := []int{5, 25, 100}

	for i := 0; i < n; i++ {
		id := fmt.Sprintf("machine-%d", i)
		allowedAt := false
		for _, p := range percents {
			c := &UpdateControl{RolloutPercent: intPtr(p)}
			cur := c.rolloutAllows(candidate, id)
			if allowedAt && !cur {
				t.Fatalf("machine %q allowed at a lower percent but dropped at %d%% (non-monotonic)", id, p)
			}
			allowedAt = allowedAt || cur
		}
		// At 100% every machine must be in.
		if !(&UpdateControl{RolloutPercent: intPtr(100)}).rolloutAllows(candidate, id) {
			t.Fatalf("machine %q not allowed at 100%%", id)
		}
	}
}

func TestControlRolloutDistribution(t *testing.T) {
	// Uniformity sanity: at 5% the allowed fraction over many machines should
	// land near 5%. Deterministic ids (no rand seeding) so this can't flake.
	const candidate = "0.0.800"
	const n = 10000
	c := &UpdateControl{RolloutPercent: intPtr(5)}
	allowed := 0
	for i := 0; i < n; i++ {
		if c.rolloutAllows(candidate, fmt.Sprintf("machine-%d", i)) {
			allowed++
		}
	}
	frac := float64(allowed) / float64(n)
	if frac < 0.03 || frac > 0.07 {
		t.Fatalf("rollout fraction at 5%% = %.4f (%d/%d), want within [0.03, 0.07]", frac, allowed, n)
	}
}

func TestControlRolloutPerVersionReshuffle(t *testing.T) {
	// The canary cohort must reshuffle per release: the machines allowed for
	// 0.0.800 are NOT the same set as for 0.0.801, so the same unlucky boxes
	// aren't always the canaries.
	const n = 4000
	c := &UpdateControl{RolloutPercent: intPtr(5)}

	setFor := func(candidate string) map[string]bool {
		s := make(map[string]bool)
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("machine-%d", i)
			if c.rolloutAllows(candidate, id) {
				s[id] = true
			}
		}
		return s
	}

	a := setFor("0.0.800")
	b := setFor("0.0.801")
	if len(a) == 0 || len(b) == 0 {
		t.Fatalf("expected non-empty cohorts, got %d and %d", len(a), len(b))
	}
	identical := len(a) == len(b)
	if identical {
		for id := range a {
			if !b[id] {
				identical = false
				break
			}
		}
	}
	if identical {
		t.Fatal("allowed set for 0.0.800 and 0.0.801 are identical; version must reshuffle the cohort")
	}
}

func TestControlFallbackRidesCacheOnTransient(t *testing.T) {
	// A transient fetch failure within the stale TTL must ride the cached,
	// last-known-good control — a CDN blip must NOT freeze the fleet.
	u := NewUpdater("0.0.1", "", "", nil, nil)
	cached := &UpdateControl{Paused: false, Message: "all good"}

	u.mu.Lock()
	u.control = cached
	u.controlFetchedAt = time.Now()
	got := u.controlFallbackLocked(fmt.Errorf("transient CDN 503"))
	u.mu.Unlock()

	if got != cached {
		t.Fatalf("controlFallbackLocked returned %+v, want the cached control", got)
	}
	if got.Paused {
		t.Fatal("riding a non-paused cache must not synthesize a pause")
	}
}

func TestControlFallbackHaltsWhenStale(t *testing.T) {
	// Past the stale TTL with no fresh fetch: fail SAFE to paused rather than
	// barrel past a kill switch we can no longer read.
	u := NewUpdater("0.0.1", "", "", nil, nil)
	u.mu.Lock()
	u.control = &UpdateControl{Paused: false, Message: "stale but was fine"}
	u.controlFetchedAt = time.Now().Add(-(controlStaleTTL + time.Minute))
	got := u.controlFallbackLocked(fmt.Errorf("still unreachable"))
	u.mu.Unlock()

	if got == nil || !got.Paused {
		t.Fatalf("stale cache must fail SAFE to Paused=true, got %+v", got)
	}
}

func TestControlFallbackHaltsWhenNoCache(t *testing.T) {
	// No cached control at all + a fetch error: fail SAFE to paused.
	u := NewUpdater("0.0.1", "", "", nil, nil)
	u.mu.Lock()
	got := u.controlFallbackLocked(fmt.Errorf("first fetch ever failed"))
	u.mu.Unlock()

	if got == nil || !got.Paused {
		t.Fatalf("no cache must fail SAFE to Paused=true, got %+v", got)
	}
}

func TestControlFetch404IsPermissive(t *testing.T) {
	// The bootstrap case: no control file published yet. A 404 must return
	// (nil, nil) — permissive default, NOT an error — so the feature stays
	// dormant until the control release exists.
	disableControlSignature(t)

	u := NewUpdater("0.0.1", "", "", nil, nil)
	u.manifestURL = "https://updates.example.test/update.json"
	// staticRoundTripper returns 404 for any URL not in the map, and we put
	// nothing in the map, so the control URL 404s.
	u.httpClient = &http.Client{Transport: staticRoundTripper{}}

	ctrl, err := u.fetchControl(context.Background())
	if err != nil {
		t.Fatalf("fetchControl on 404 returned error %v, want nil (permissive)", err)
	}
	if ctrl != nil {
		t.Fatalf("fetchControl on 404 returned %+v, want nil control", ctrl)
	}
}

func TestControlFetchParsesAndGates(t *testing.T) {
	// Serve a valid control JSON and assert fetchControl parses every field.
	// Signature verification is disabled (no public key) so the dev/unsigned
	// path is exercised and no .sig fetch happens.
	disableControlSignature(t)

	want := UpdateControl{
		Schema:         1,
		Paused:         false,
		HaltAtOrAbove:  "0.0.900",
		HaltBelow:      "0.0.500",
		RolloutPercent: intPtr(25),
		Message:        "staged at 25%",
	}
	body, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}

	u := NewUpdater("0.0.1", "", "", nil, nil)
	u.manifestURL = "https://updates.example.test/update.json"
	controlURL := u.controlURL()
	u.httpClient = &http.Client{Transport: staticRoundTripper{
		controlURL: body,
	}}

	got, err := u.fetchControl(context.Background())
	if err != nil {
		t.Fatalf("fetchControl error: %v", err)
	}
	if got == nil {
		t.Fatal("fetchControl returned nil control for a valid 200 body")
	}
	if got.Schema != want.Schema || got.Paused != want.Paused ||
		got.HaltAtOrAbove != want.HaltAtOrAbove || got.HaltBelow != want.HaltBelow ||
		got.Message != want.Message {
		t.Fatalf("parsed control = %+v, want %+v", *got, want)
	}
	if got.RolloutPercent == nil || *got.RolloutPercent != 25 {
		t.Fatalf("parsed RolloutPercent = %v, want 25", got.RolloutPercent)
	}

	// Sanity: the parsed control gates as expected (within rollout-independent
	// halt bounds, candidate 0.0.800 is allowed by the halts; 0.0.900 blocked).
	if blocked, _ := got.blocksApply("0.0.900", "0.0.700", "machine-1"); !blocked {
		t.Fatal("parsed control should block 0.0.900 (halt_at_or_above 0.0.900)")
	}
}
