package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// A local POST /api/cloud/disconnect clears claim.json and returns 200.
func TestHandleCloudDisconnectRemovesClaim(t *testing.T) {
	dir := t.TempDir()
	if err := SaveClaim(dir, &Claim{RelayURL: "wss://x/relay", Token: "tok_live", MachineID: "mach_1"}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	if _, err := os.Stat(claimFilePath(dir)); err != nil {
		t.Fatalf("claim not seeded: %v", err)
	}

	a := &apiServer{dataDir: dir} // cloudSup/pairing nil — handler must tolerate it
	rec := httptest.NewRecorder()
	a.handleCloudDisconnect(rec, httptest.NewRequest(http.MethodPost, "/api/cloud/disconnect", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(claimFilePath(dir)); !os.IsNotExist(err) {
		t.Fatalf("claim.json still present after disconnect (err=%v)", err)
	}
}

// Disconnect is LOCAL-ONLY: a relay-forwarded request is rejected 403 and the
// claim is left untouched (you can't sever a machine through its own tunnel).
func TestHandleCloudDisconnectLocalOnly(t *testing.T) {
	dir := t.TempDir()
	if err := SaveClaim(dir, &Claim{RelayURL: "wss://x/relay", Token: "tok_live", MachineID: "mach_1"}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}

	a := &apiServer{dataDir: dir}
	req := httptest.NewRequest(http.MethodPost, "/api/cloud/disconnect", nil)
	req.Header.Set("X-Forwarded-Prefix", "/m/mach_1") // cloud-relay stamp
	rec := httptest.NewRecorder()
	a.handleCloudDisconnect(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if _, err := os.Stat(claimFilePath(dir)); err != nil {
		t.Fatalf("claim.json must survive a refused relay disconnect (err=%v)", err)
	}
}

// RemoveClaim on an already-disconnected machine is success, not an error.
func TestRemoveClaimIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := RemoveClaim(dir); err != nil {
		t.Fatalf("RemoveClaim on missing claim = %v, want nil", err)
	}
}

// The supervisor's reconcile reads claim state via LoadClaim; after RemoveClaim
// that read returns (nil, nil) — the "no creds" case that drives the supervisor
// back to HasCreds=false (UI flips from "sign-in needed" to "not set up").
func TestRemoveClaimLeavesNoLoadableCreds(t *testing.T) {
	dir := t.TempDir()
	writeClaimToken(t, dir, "tok_dead")
	if c, err := LoadClaim(dir); err != nil || c == nil {
		t.Fatalf("precondition: LoadClaim = (%v, %v), want a claim", c, err)
	}

	if err := RemoveClaim(dir); err != nil {
		t.Fatalf("RemoveClaim: %v", err)
	}
	if c, err := LoadClaim(dir); err != nil || c != nil {
		t.Fatalf("LoadClaim after RemoveClaim = (%v, %v), want (nil, nil)", c, err)
	}
}

// When a CONNECTED machine's claim.json is deleted (disconnect), the supervisor
// tears the tunnel down AND fires onCredsLost exactly once on the having→none
// edge — and never re-fires on subsequent idle polls. This is what tears the
// HTTPS-side integrations (pusher / usage scanner / rotation poller) down.
func TestSupervisorFiresOnCredsLostOnDisconnect(t *testing.T) {
	dir := t.TempDir()
	writeClaimToken(t, dir, "tok-live")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ft := &fakeTunnel{}
	sup := newRelaySupervisor(ctx, dir, "wss://example.test/relay", "wsl", nil)
	sup.dial = ft.dial
	sup.pollInterval = 10 * time.Millisecond
	var lost int32
	sup.onCredsLost = func() { atomic.AddInt32(&lost, 1) }

	go sup.run()

	// Tunnel comes up (current token set).
	eventuallyTrue(t, 2*time.Second, func() bool { return atomic.LoadInt32(&ft.live) == 1 })

	// Disconnect: delete claim.json the way the handler does, then kick.
	if err := RemoveClaim(dir); err != nil {
		t.Fatalf("RemoveClaim: %v", err)
	}
	sup.signalTokenChanged()

	// Tunnel torn down + status reverted + onCredsLost fired once.
	eventuallyTrue(t, 2*time.Second, func() bool { return atomic.LoadInt32(&ft.live) == 0 })
	eventuallyTrue(t, 2*time.Second, func() bool { return atomic.LoadInt32(&lost) == 1 })
	if st := sup.status(); st.HasCreds || st.CredsInvalid {
		t.Fatalf("after disconnect: HasCreds=%v CredsInvalid=%v, want both false", st.HasCreds, st.CredsInvalid)
	}

	// Several more idle poll ticks must NOT re-fire onCredsLost (no-claim is the
	// steady state now, not a fresh edge).
	time.Sleep(60 * time.Millisecond)
	if got := atomic.LoadInt32(&lost); got != 1 {
		t.Fatalf("onCredsLost fired %d times, want exactly 1 (no re-fire on idle polls)", got)
	}
}

// A daemon that boots with NO claim.json (never paired) must NOT fire
// onCredsLost — there were no creds to lose. Guards the current!="" edge check.
func TestSupervisorNoCredsLostWhenNeverPaired(t *testing.T) {
	dir := t.TempDir() // no claim.json

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sup := newRelaySupervisor(ctx, dir, "wss://example.test/relay", "wsl", nil)
	sup.pollInterval = 10 * time.Millisecond
	var lost int32
	sup.onCredsLost = func() { atomic.AddInt32(&lost, 1) }

	go sup.run()

	time.Sleep(80 * time.Millisecond) // several poll ticks through the no-creds branch
	if got := atomic.LoadInt32(&lost); got != 0 {
		t.Fatalf("onCredsLost fired %d times on a never-paired daemon, want 0", got)
	}
}
