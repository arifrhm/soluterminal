package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeCloud stubs the cloud's claim endpoints (/api/claim/start +
// /api/claim/wait) so pairing can be driven without a real cloud. It serves a
// fixed code/secret and a wait-status the test flips via setWaitStatus.
type fakeCloud struct {
	code   string
	secret string
	ttlSec int

	mu        sync.Mutex
	waitState string // "pending" | "claimed" | "expired"
	token     string
	machineID string
	name      string

	startHits int32
	waitHits  int32
}

func newFakeCloud() *fakeCloud {
	return &fakeCloud{
		code:      "ABC234",
		secret:    "deadbeefsecret",
		ttlSec:    600,
		waitState: "pending",
		token:     "machine-token-xyz",
		machineID: "mach_abc234",
		name:      "test-machine",
	}
}

func (f *fakeCloud) setWaitStatus(s string) {
	f.mu.Lock()
	f.waitState = s
	f.mu.Unlock()
}

func (f *fakeCloud) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/claim/start", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.startHits, 1)
		writeJSON(w, http.StatusOK, map[string]any{
			"code":   f.code,
			"secret": f.secret,
			"ttlSec": f.ttlSec,
		})
	})
	mux.HandleFunc("/api/claim/wait", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.waitHits, 1)
		// The secret fetches the machine token once claimed, so it must ride
		// the header and never the (host-logged) URL.
		if r.URL.RawQuery != "" || r.Header.Get("X-Solu-Claim-Secret") != f.secret {
			http.Error(w, "claim secret must be sent in X-Solu-Claim-Secret only", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		state := f.waitState
		f.mu.Unlock()
		switch state {
		case "claimed":
			writeJSON(w, http.StatusOK, map[string]any{
				"status":    "claimed",
				"machineId": f.machineID,
				"name":      f.name,
				"token":     f.token,
			})
		case "expired":
			writeJSON(w, http.StatusOK, map[string]any{"status": "expired"})
		default:
			writeJSON(w, http.StatusOK, map[string]any{"status": "pending"})
		}
	})
	return mux
}

// relayURLFor turns an httptest server's http base into the ws:// form the
// daemon expects on --relay (relayToHTTPBase converts it back to http://).
func relayURLFor(ts *httptest.Server) string {
	return "ws://" + strings.TrimPrefix(ts.URL, "http://") + "/relay"
}

// readCloudStatus drives the real handler and decodes the response.
func readCloudStatus(t *testing.T, api *apiServer) cloudStatusResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	api.handleCloudStatus(rec, httptest.NewRequest("GET", "/api/cloud/status", nil))
	if rec.Code != 200 {
		t.Fatalf("cloud status: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out cloudStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode cloud status: %v (body=%s)", err, rec.Body.String())
	}
	return out
}

// TestPairStartRegistersPendingVisibleInStatus: POST /pair/start returns a code
// and registers pending state visible in GET /api/cloud/status.
func TestPairStartRegistersPendingVisibleInStatus(t *testing.T) {
	cloud := newFakeCloud()
	ts := httptest.NewServer(cloud.handler())
	defer ts.Close()

	dir := t.TempDir()
	pm := newPairingManager(t.Context(), relayURLFor(ts), dir, nil)
	pm.pollInterval = 10 * time.Millisecond
	api := &apiServer{pairing: pm}
	defer pm.cancel()

	rec := httptest.NewRecorder()
	api.handleCloudPairStart(rec, httptest.NewRequest("POST", "/api/cloud/pair/start", nil))
	if rec.Code != 200 {
		t.Fatalf("pair/start: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var start pairStartResult
	if err := json.Unmarshal(rec.Body.Bytes(), &start); err != nil {
		t.Fatalf("decode pair/start: %v", err)
	}
	if start.Code != cloud.code {
		t.Fatalf("pair/start code=%q, want %q", start.Code, cloud.code)
	}
	if start.ExpiresAt == "" {
		t.Fatalf("pair/start expiresAt empty")
	}
	if _, err := time.Parse(time.RFC3339, start.ExpiresAt); err != nil {
		t.Fatalf("pair/start expiresAt not RFC3339: %q (%v)", start.ExpiresAt, err)
	}

	// /api/cloud/status must now show the pending pair.
	st := readCloudStatus(t, api)
	if !st.Pairing.Active {
		t.Fatalf("status.pairing.active=false, want true: %+v", st.Pairing)
	}
	if st.Pairing.Code != cloud.code {
		t.Fatalf("status.pairing.code=%q, want %q", st.Pairing.Code, cloud.code)
	}
	if st.Pairing.ExpiresAt != start.ExpiresAt {
		t.Fatalf("status.pairing.expiresAt=%q, want %q", st.Pairing.ExpiresAt, start.ExpiresAt)
	}
}

// TestPairStartIdempotentSameCode: a second /pair/start while one is pending
// returns the SAME code and does NOT start a second cloud claim.
func TestPairStartIdempotentSameCode(t *testing.T) {
	cloud := newFakeCloud()
	ts := httptest.NewServer(cloud.handler())
	defer ts.Close()

	dir := t.TempDir()
	pm := newPairingManager(t.Context(), relayURLFor(ts), dir, nil)
	pm.pollInterval = 10 * time.Millisecond
	defer pm.cancel()

	first, err := pm.start(t.Context())
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	second, err := pm.start(t.Context())
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	if first.Code != second.Code {
		t.Fatalf("idempotency broken: first=%q second=%q", first.Code, second.Code)
	}
	if got := atomic.LoadInt32(&cloud.startHits); got != 1 {
		t.Fatalf("cloud /claim/start called %d times, want 1 (idempotent)", got)
	}
}

// TestPairStartRejectedOverRelay: a relay-forwarded /pair/start is rejected 403
// and registers NO pending state.
func TestPairStartRejectedOverRelay(t *testing.T) {
	cloud := newFakeCloud()
	ts := httptest.NewServer(cloud.handler())
	defer ts.Close()

	dir := t.TempDir()
	pm := newPairingManager(t.Context(), relayURLFor(ts), dir, nil)
	pm.pollInterval = 10 * time.Millisecond
	api := &apiServer{pairing: pm}
	defer pm.cancel()

	req := httptest.NewRequest("POST", "/api/cloud/pair/start", nil)
	req.Header.Set("X-Forwarded-Prefix", "/m/mach_abc234") // cloud-relay stamp
	rec := httptest.NewRecorder()
	api.handleCloudPairStart(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("relay pair/start: code=%d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
	// No outbound claim should have been made, and no pending state registered.
	if got := atomic.LoadInt32(&cloud.startHits); got != 0 {
		t.Fatalf("cloud /claim/start hit %d times on a rejected relay request", got)
	}
	if st := readCloudStatus(t, api); st.Pairing.Active {
		t.Fatalf("relay-rejected start left pairing active: %+v", st.Pairing)
	}
}

// TestPairCancelRejectedOverRelay: /pair/cancel is also local-only.
func TestPairCancelRejectedOverRelay(t *testing.T) {
	t.Parallel()
	api := &apiServer{pairing: newPairingManager(t.Context(), "ws://x.test/relay", t.TempDir(), nil)}
	req := httptest.NewRequest("POST", "/api/cloud/pair/cancel", nil)
	req.Header.Set("X-Forwarded-Prefix", "/m/mach_abc234")
	rec := httptest.NewRecorder()
	api.handleCloudPairCancel(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("relay pair/cancel: code=%d, want 403", rec.Code)
	}
}

// TestPairCancelClearsPending: /pair/cancel stops a pending pair and flips
// /status pairing.active back to false.
func TestPairCancelClearsPending(t *testing.T) {
	cloud := newFakeCloud()
	ts := httptest.NewServer(cloud.handler())
	defer ts.Close()

	dir := t.TempDir()
	pm := newPairingManager(t.Context(), relayURLFor(ts), dir, nil)
	pm.pollInterval = 10 * time.Millisecond
	api := &apiServer{pairing: pm}

	if _, err := pm.start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if st := readCloudStatus(t, api); !st.Pairing.Active {
		t.Fatalf("expected active pairing before cancel: %+v", st.Pairing)
	}

	rec := httptest.NewRecorder()
	api.handleCloudPairCancel(rec, httptest.NewRequest("POST", "/api/cloud/pair/cancel", nil))
	if rec.Code != 200 {
		t.Fatalf("pair/cancel: code=%d", rec.Code)
	}
	if st := readCloudStatus(t, api); st.Pairing.Active {
		t.Fatalf("pairing still active after cancel: %+v", st.Pairing)
	}
}

// TestPairClaimedWritesClaimAndFiresOnClaimed: when the cloud reports "claimed",
// the poll goroutine writes claim.json, clears pending, and fires onClaimed
// (the supervisor nudge) — without dialing anything.
func TestPairClaimedWritesClaimAndFiresOnClaimed(t *testing.T) {
	cloud := newFakeCloud()
	ts := httptest.NewServer(cloud.handler())
	defer ts.Close()

	dir := t.TempDir()
	var nudged int32
	pm := newPairingManager(t.Context(), relayURLFor(ts), dir, func() { atomic.AddInt32(&nudged, 1) })
	pm.pollInterval = 5 * time.Millisecond
	api := &apiServer{pairing: pm}
	defer pm.cancel()

	if _, err := pm.start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Flip the cloud to "claimed"; the background poll should pick it up.
	cloud.setWaitStatus("claimed")

	// claim.json appears with the cloud's token.
	eventuallyTrue(t, 2*time.Second, func() bool {
		c, err := LoadClaim(dir)
		return err == nil && c != nil && c.Token == cloud.token
	})
	c, err := LoadClaim(dir)
	if err != nil || c == nil {
		t.Fatalf("claim.json not written: %v", err)
	}
	if c.MachineID != cloud.machineID || c.MachineName != cloud.name {
		t.Fatalf("claim.json fields wrong: %+v", c)
	}
	// onClaimed (supervisor nudge) fired exactly once.
	eventuallyTrue(t, 1*time.Second, func() bool { return atomic.LoadInt32(&nudged) >= 1 })
	// Pending state cleared → /status pairing.active false.
	eventuallyTrue(t, 1*time.Second, func() bool { return !readCloudStatus(t, api).Pairing.Active })
}

// TestPairPollerSurvivesRequestContextCancel is the regression test for the
// "machine stays offline after accepting the code" bug: start() used to parent
// the poll goroutine on the /pair/start REQUEST context. net/http cancels that
// the instant the response is written, so the poller died before its first tick
// — the cloud accepted the code but the daemon never wrote claim.json and never
// dialed the relay. The poll goroutine MUST be parented by the daemon ctx, so a
// cancelled request ctx leaves it running until the pair actually completes.
//
// This test would FAIL against the old code (claim.json never appears) and pass
// with the fix. httptest.NewRequest can't reproduce it — its context is never
// cancelled — so we cancel an explicit request ctx by hand.
func TestPairPollerSurvivesRequestContextCancel(t *testing.T) {
	cloud := newFakeCloud()
	ts := httptest.NewServer(cloud.handler())
	defer ts.Close()

	dir := t.TempDir()
	var nudged int32
	// baseCtx = a long-lived (daemon) context, deliberately NOT the request ctx.
	pm := newPairingManager(context.Background(), relayURLFor(ts), dir, func() { atomic.AddInt32(&nudged, 1) })
	pm.pollInterval = 5 * time.Millisecond
	defer pm.cancel()

	// Simulate the production handler: a request-scoped ctx that is cancelled the
	// moment /pair/start returns its response.
	reqCtx, cancelReq := context.WithCancel(context.Background())
	if _, err := pm.start(reqCtx); err != nil {
		t.Fatalf("start: %v", err)
	}
	cancelReq() // request done — the poller MUST keep polling.

	// Now accept the code. A poller bound to reqCtx would already be dead and
	// claim.json would never appear.
	cloud.setWaitStatus("claimed")
	eventuallyTrue(t, 2*time.Second, func() bool {
		c, err := LoadClaim(dir)
		return err == nil && c != nil && c.Token == cloud.token
	})
	eventuallyTrue(t, 1*time.Second, func() bool { return atomic.LoadInt32(&nudged) >= 1 })
}

// TestPairExpiredClearsPending: when the cloud reports "expired", the poll
// goroutine clears pending state (no claim.json written, no nudge).
func TestPairExpiredClearsPending(t *testing.T) {
	cloud := newFakeCloud()
	ts := httptest.NewServer(cloud.handler())
	defer ts.Close()

	dir := t.TempDir()
	var nudged int32
	pm := newPairingManager(t.Context(), relayURLFor(ts), dir, func() { atomic.AddInt32(&nudged, 1) })
	pm.pollInterval = 5 * time.Millisecond
	api := &apiServer{pairing: pm}
	defer pm.cancel()

	if _, err := pm.start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	cloud.setWaitStatus("expired")

	eventuallyTrue(t, 2*time.Second, func() bool { return !readCloudStatus(t, api).Pairing.Active })
	if c, _ := LoadClaim(dir); c != nil {
		t.Fatalf("claim.json written on expiry: %+v", c)
	}
	if atomic.LoadInt32(&nudged) != 0 {
		t.Fatalf("onClaimed fired on expiry")
	}
}

// TestPairStartUnconfigured: with no --relay (empty relayURL), start reports the
// unconfigured error and the handler returns 400.
func TestPairStartUnconfigured(t *testing.T) {
	t.Parallel()
	api := &apiServer{pairing: newPairingManager(t.Context(), "", t.TempDir(), nil)}
	rec := httptest.NewRecorder()
	api.handleCloudPairStart(rec, httptest.NewRequest("POST", "/api/cloud/pair/start", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unconfigured pair/start: code=%d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestCloudStatusPairingInactiveByDefault: with a configured-but-idle pairing
// manager, /status reports pairing inactive with an empty code (the contract's
// default object).
func TestCloudStatusPairingInactiveByDefault(t *testing.T) {
	t.Parallel()
	api := &apiServer{pairing: newPairingManager(t.Context(), "ws://x.test/relay", t.TempDir(), nil)}
	st := readCloudStatus(t, api)
	if st.Pairing.Active || st.Pairing.Code != "" {
		t.Fatalf("idle pairing not inactive: %+v", st.Pairing)
	}
}
