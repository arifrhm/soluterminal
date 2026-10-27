package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// writeClaimToken writes a minimal valid claim.json with the given token.
func writeClaimToken(t *testing.T, dataDir, token string) {
	t.Helper()
	c := &Claim{
		RelayURL:    "wss://example.test/relay",
		Token:       token,
		MachineID:   "mach_test",
		MachineName: "test",
		ClaimedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	if err := SaveClaim(dataDir, c); err != nil {
		t.Fatalf("save claim: %v", err)
	}
}

// fakeTunnel tracks the number of concurrently-live tunnels so the test can
// assert the exactly-one-tunnel invariant. Each call blocks (like a real
// serveRelay) until its ctx is cancelled.
type fakeTunnel struct {
	live    int32 // current live tunnels
	maxLive int32 // high-water mark — MUST stay <= 1
	starts  int32 // total dials
	mu      sync.Mutex
	tokens  []string // token of each dial, in order
}

func (f *fakeTunnel) dial(ctx context.Context, _, token, _ string, _ http.Handler, onConnected func(string)) error {
	atomic.AddInt32(&f.starts, 1)
	f.mu.Lock()
	f.tokens = append(f.tokens, token)
	f.mu.Unlock()

	now := atomic.AddInt32(&f.live, 1)
	for {
		cur := atomic.LoadInt32(&f.maxLive)
		if now <= cur || atomic.CompareAndSwapInt32(&f.maxLive, cur, now) {
			break
		}
	}
	if onConnected != nil {
		onConnected("")
	}
	<-ctx.Done()
	atomic.AddInt32(&f.live, -1)
	return ctx.Err()
}

// TestRelaySupervisorExactlyOneTunnelAcrossRotation is the mandatory P0
// regression guard: across a token-rotation event, the supervisor must keep
// EXACTLY ONE live tunnel per backendID — never two overlapping ones. This
// protects the healthy production machines from duplicate tunnels.
func TestRelaySupervisorExactlyOneTunnelAcrossRotation(t *testing.T) {
	dir := t.TempDir()
	writeClaimToken(t, dir, "tok-A")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ft := &fakeTunnel{}
	sup := newRelaySupervisor(ctx, dir, "wss://example.test/relay", "wsl", nil)
	sup.dial = ft.dial
	sup.pollInterval = 10 * time.Millisecond

	go sup.run()

	// Wait for the first tunnel to come up (token A).
	eventuallyTrue(t, 2*time.Second, func() bool { return atomic.LoadInt32(&ft.live) == 1 })
	if got := sup.status().RelayState; got != relayStateOnline {
		t.Fatalf("after first dial: relayState=%q, want online", got)
	}

	// Simulate a token rotation exactly the way the real poller does: write the
	// new token to claim.json (UpdateClaimToken), then signal the supervisor.
	if err := UpdateClaimToken(dir, "tok-B"); err != nil {
		t.Fatalf("rotate token: %v", err)
	}
	sup.signalTokenChanged()

	// The supervisor must tear the A tunnel down and bring up B. At NO point may
	// two tunnels be live simultaneously.
	eventuallyTrue(t, 2*time.Second, func() bool {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		// B has been dialed and is the only live tunnel.
		return atomic.LoadInt32(&ft.starts) >= 2 &&
			atomic.LoadInt32(&ft.live) == 1 &&
			len(ft.tokens) >= 2 && ft.tokens[len(ft.tokens)-1] == "tok-B"
	})

	// The invariant: high-water mark of concurrent tunnels never exceeded 1.
	if max := atomic.LoadInt32(&ft.maxLive); max > 1 {
		t.Fatalf("DOUBLE-DIAL: max concurrent tunnels = %d, want <= 1", max)
	}

	// An UNCHANGED claim.json (the file write rotation itself does) must not
	// cause any further dials — the token-value debounce. Re-signal a few times
	// with the same token and confirm no extra dial happens.
	before := atomic.LoadInt32(&ft.starts)
	for i := 0; i < 3; i++ {
		sup.signalTokenChanged()
		time.Sleep(20 * time.Millisecond)
	}
	if after := atomic.LoadInt32(&ft.starts); after != before {
		t.Fatalf("debounce broken: %d extra dial(s) on unchanged token", after-before)
	}
	if max := atomic.LoadInt32(&ft.maxLive); max > 1 {
		t.Fatalf("DOUBLE-DIAL after debounce signals: max concurrent = %d, want <= 1", max)
	}

	// Shutdown drains the tunnel.
	cancel()
	eventuallyTrue(t, 2*time.Second, func() bool { return atomic.LoadInt32(&ft.live) == 0 })
}

// TestRelaySupervisorAuthRejectParks asserts a 401-classified dial stops
// retrying (no tight loop) until the token changes, then resumes.
func TestRelaySupervisorAuthRejectParks(t *testing.T) {
	dir := t.TempDir()
	writeClaimToken(t, dir, "bad-token")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var dials int32
	authErr := &relayAuthError{status: http.StatusUnauthorized}
	dial := func(dctx context.Context, _, token, _ string, _ http.Handler, onConnected func(string)) error {
		atomic.AddInt32(&dials, 1)
		if token == "good-token" {
			// Block as a healthy tunnel.
			if onConnected != nil {
				onConnected("")
			}
			<-dctx.Done()
			return dctx.Err()
		}
		return authErr // immediate auth reject
	}

	sup := newRelaySupervisor(ctx, dir, "wss://example.test/relay", "", nil)
	sup.dial = dial
	sup.pollInterval = 10 * time.Millisecond
	relayRedialDelayBackup := relayRedialDelay
	relayRedialDelay = 5 * time.Millisecond
	defer func() { relayRedialDelay = relayRedialDelayBackup }()
	sup.redialDelay = relayRedialDelay

	go sup.run()

	// It should dial once, get rejected, and PARK (credsInvalid). Give it time
	// to (wrongly) loop if the park logic is broken.
	eventuallyTrue(t, 2*time.Second, func() bool { return sup.status().CredsInvalid })
	time.Sleep(150 * time.Millisecond)
	if d := atomic.LoadInt32(&dials); d > 2 {
		t.Fatalf("auth-reject tight loop: %d dials, want a parked ~1", d)
	}
	if st := sup.status(); st.RelayState != relayStateOffline || !st.CredsInvalid {
		t.Fatalf("parked state wrong: %+v", st)
	}

	// Rotating to a good token must un-park and connect.
	if err := UpdateClaimToken(dir, "good-token"); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	sup.signalTokenChanged()
	eventuallyTrue(t, 2*time.Second, func() bool { return sup.status().RelayState == relayStateOnline })
	if sup.status().CredsInvalid {
		t.Fatalf("credsInvalid still set after good-token connect")
	}
}

// TestRelaySupervisorCorruptClaimNeverCrashes asserts a malformed claim.json
// is surfaced as claimCorrupt (not a crash, not "absent").
func TestRelaySupervisorCorruptClaimNeverCrashes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Truncated / non-JSON content.
	if err := os.WriteFile(filepath.Join(dir, "claim.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dial := func(_ context.Context, _, _, _ string, _ http.Handler, _ func(string)) error {
		t.Errorf("dial must NOT be called for a corrupt claim.json")
		return errors.New("unexpected")
	}
	sup := newRelaySupervisor(ctx, dir, "wss://example.test/relay", "", nil)
	sup.dial = dial
	sup.pollInterval = 10 * time.Millisecond

	go sup.run()

	eventuallyTrue(t, 2*time.Second, func() bool { return sup.status().ClaimCorrupt })
	st := sup.status()
	if st.HasCreds {
		t.Fatalf("corrupt claim reported HasCreds=true: %+v", st)
	}
	if st.RelayState != relayStateOffline {
		t.Fatalf("corrupt claim relayState=%q, want offline", st.RelayState)
	}
}

// TestRelaySupervisorDormantThenPairs asserts the cloud-DORMANT path: a
// supervisor that boots with NO claim.json connects live once one appears,
// with no restart.
func TestRelaySupervisorDormantThenPairs(t *testing.T) {
	dir := t.TempDir() // no claim.json yet

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ft := &fakeTunnel{}
	var credsToken string
	sup := newRelaySupervisor(ctx, dir, "wss://example.test/relay", "", nil)
	sup.dial = ft.dial
	sup.pollInterval = 10 * time.Millisecond
	sup.onCreds = func(tok string) { credsToken = tok }

	go sup.run()

	// Boots dormant: configured, no creds, offline, no dial.
	time.Sleep(50 * time.Millisecond)
	if st := sup.status(); !st.Configured || st.HasCreds || st.RelayState != relayStateOffline {
		t.Fatalf("dormant state wrong: %+v", st)
	}
	if atomic.LoadInt32(&ft.starts) != 0 {
		t.Fatalf("dialed before pairing")
	}

	// Pair: write claim.json. The poll picks it up and connects — no restart.
	writeClaimToken(t, dir, "tok-paired")
	eventuallyTrue(t, 2*time.Second, func() bool { return sup.status().RelayState == relayStateOnline })
	if credsToken != "tok-paired" {
		t.Fatalf("onCreds not fired with new token, got %q", credsToken)
	}
	if max := atomic.LoadInt32(&ft.maxLive); max > 1 {
		t.Fatalf("max concurrent tunnels = %d, want <= 1", max)
	}
}

// TestRelaySupervisorStatusRaceUnderCredsFlip hammers GET /api/cloud/status
// (through the real handler) AND the raw supervisor status() concurrently while
// the supervisor flips its live cloud state through pairing → corrupt →
// rotation cycles. With the cloudStatus behind s.mu this is clean; run under
// `-race` to catch any regression that drops the lock.
func TestRelaySupervisorStatusRaceUnderCredsFlip(t *testing.T) {
	dir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ft := &fakeTunnel{}
	sup := newRelaySupervisor(ctx, dir, "wss://example.test/relay", "wsl", nil)
	sup.dial = ft.dial
	sup.pollInterval = 5 * time.Millisecond
	relayRedialDelayBackup := relayRedialDelay
	relayRedialDelay = 2 * time.Millisecond
	defer func() { relayRedialDelay = relayRedialDelayBackup }()
	sup.redialDelay = relayRedialDelay

	// Wire a real apiServer so the production handler is exercised under -race.
	api := &apiServer{cloudSup: sup}

	// relayRedialDelay remains the legacy test seam for zero-value supervisors;
	// this constructed supervisor copies the stub into its instance-local field.
	// Make the goroutine waitable so cleanup finishes before the deferred global
	// restore and no mutable test seam leaks into a later test.
	runDone := make(chan struct{})
	go func() { defer close(runDone); sup.run() }()

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Reader goroutines: concurrent status reads, both raw and through the HTTP
	// handler (which calls a.cloudSup.status()).
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = sup.status()
				_ = sup.connected()
				rec := httptest.NewRecorder()
				api.handleCloudStatus(rec, httptest.NewRequest("GET", "/api/cloud/status", nil))
				if rec.Code != 200 {
					t.Errorf("handleCloudStatus status=%d", rec.Code)
					return
				}
			}
		}()
	}

	// Writer goroutine: drive the supervisor through every state-mutating path —
	// valid token, rotation, corrupt claim, absent claim — as fast as it can.
	wg.Add(1)
	go func() {
		defer wg.Done()
		seq := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			seq++
			switch seq % 4 {
			case 0:
				writeClaimToken(t, dir, "tok-1")
			case 1:
				writeClaimToken(t, dir, "tok-2") // rotation (value change)
			case 2:
				_ = os.WriteFile(filepath.Join(dir, "claim.json"), []byte("{bad"), 0o600) // corrupt
			case 3:
				_ = os.Remove(filepath.Join(dir, "claim.json")) // absent
			}
			sup.signalTokenChanged()
			time.Sleep(time.Millisecond)
		}
	}()

	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
	cancel()
	// Block until run() has returned before the deferred relayRedialDelay restore
	// runs — run()'s ctx.Done path teardown() also blocks until the dialer exits,
	// so ft.live is already 0 here (the assertion below is a leak sanity check).
	<-runDone
	eventuallyTrue(t, 2*time.Second, func() bool { return atomic.LoadInt32(&ft.live) == 0 })
}

// TestApiServerCloudCredsRace targets the exact race our change introduced: the
// relay supervisor writes the live cloud-creds fields dynamically (pairing /
// rotation) while HTTP handlers read them. It hammers the readers (cloudCreds,
// ccCloudCreds, getPusher) while writers (setCloudCreds, rotateCloudToken) run.
// Must be clean under -race.
func TestApiServerCloudCredsRace(t *testing.T) {
	api := &apiServer{}

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Writers: full re-pair swaps + token rotations.
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			i++
			tok := "tok-" + strconv.Itoa(i)
			if i%2 == 0 {
				api.setCloudCreds("https://c.test", tok, &Pusher{token: tok})
			} else {
				api.rotateCloudToken(tok)
			}
		}
	}()

	// Readers: mirror what voice / cc-usage / ack handlers do.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = api.cloudCreds()
				_, _ = api.ccCloudCreds()
				_ = api.getPusher()
			}
		}()
	}

	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestRelayBackoffFullJitterBounded exercises the capped-exponential +
// full-jitter redial backoff directly (no network). It asserts every draw is
// within [0, cap] for the growing cap, the cap is clamped at the ceiling, and
// the attempt counter advances on each call. Because the jitter is random we
// repeat many draws per attempt so the [0, cap] bound is exercised across the
// distribution, not a single lucky value.
func TestRelayBackoffFullJitterBounded(t *testing.T) {
	// Use the production base/ceiling so the bound math matches shipping
	// behavior. (relayRedialDelay is a var; restore it if a prior test stubbed
	// it, to keep this test order-independent.)
	relayRedialDelayBackup := relayRedialDelay
	relayRedialDelay = time.Second
	defer func() { relayRedialDelay = relayRedialDelayBackup }()

	sup := &relaySupervisor{}

	// expectedCap mirrors nextRelayBackoff's cap formula for a given attempt.
	expectedCap := func(attempt int) time.Duration {
		c := relayRedialDelay
		if attempt > 0 {
			shifted := relayRedialDelay << attempt
			if shifted <= 0 || shifted > relayRedialCeiling {
				c = relayRedialCeiling
			} else {
				c = shifted
			}
		}
		if c > relayRedialCeiling {
			c = relayRedialCeiling
		}
		return c
	}

	const draws = 200
	// Go well past the point the cap saturates the ceiling (1s<<5 = 32s > 30s,
	// so attempt 5+ is all ceiling) to prove the overflow/clamp guard holds.
	for attempt := 0; attempt < 40; attempt++ {
		cap := expectedCap(attempt)
		if cap > relayRedialCeiling {
			t.Fatalf("attempt %d: computed cap %v exceeds ceiling %v", attempt, cap, relayRedialCeiling)
		}
		for i := 0; i < draws; i++ {
			before := sup.redialAttempt
			d := sup.nextRelayBackoff()
			if d < 0 || d > cap {
				t.Fatalf("attempt %d draw %d: backoff %v out of [0, %v]", attempt, i, d, cap)
			}
			if sup.redialAttempt != before+1 {
				t.Fatalf("attempt %d draw %d: counter did not advance (%d -> %d)", attempt, i, before, sup.redialAttempt)
			}
			// Each inner draw advances the counter, so re-pin it to keep this
			// `attempt` testing a single cap value across many random draws.
			sup.redialAttempt = before
		}
		sup.redialAttempt = attempt + 1
	}

	// A fresh supervisor's very first draw must be capped at the base (no
	// pre-escalation) and the counter must start advancing from 0.
	fresh := &relaySupervisor{}
	d0 := fresh.nextRelayBackoff()
	if d0 < 0 || d0 > relayRedialDelay {
		t.Fatalf("first draw %v not in [0, base=%v]", d0, relayRedialDelay)
	}
	if fresh.redialAttempt != 1 {
		t.Fatalf("first draw left counter at %d, want 1", fresh.redialAttempt)
	}
}

// waitFor polls cond until it returns true or the timeout elapses.
func eventuallyTrue(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}
