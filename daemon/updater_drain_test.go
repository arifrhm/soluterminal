package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWfDrainDecision(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	window := 10 * time.Minute
	tests := []struct {
		name         string
		start        time.Time
		inflight     int
		err          error
		wantWait     bool
		wantOutcome  drainOutcome
		wantStart    time.Time
		wantInReason string
	}{
		{name: "first observation", inflight: 3, wantWait: true, wantOutcome: drainWait, wantStart: now, wantInReason: "3 in-flight"},
		{name: "still draining", start: now.Add(-4 * time.Minute), inflight: 2, wantWait: true, wantOutcome: drainWait, wantStart: now.Add(-4 * time.Minute), wantInReason: "6m0s"},
		// Behavioral replacement: deadline coverage now proves lossless defer,
		// rather than the removed swap-through-active-work behavior.
		{name: "deadline", start: now.Add(-window), inflight: 2, wantOutcome: drainDefer, wantInReason: "deferring update"},
		{name: "past deadline", start: now.Add(-window - time.Second), inflight: 2, wantOutcome: drainDefer, wantInReason: "deferring update"},
		{name: "drained", start: now.Add(-time.Minute), inflight: 0},
		{name: "error fails open", start: now.Add(-time.Minute), inflight: 2, err: errors.New("query failed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome, start, reason := drainDecision(now, tt.start, window, tt.inflight, tt.err)
			if outcome != tt.wantOutcome || !start.Equal(tt.wantStart) {
				t.Fatalf("decision = (outcome=%v, start=%v), want (outcome=%v, start=%v)", outcome, start, tt.wantOutcome, tt.wantStart)
			}
			if tt.wantInReason != "" && !strings.Contains(reason, tt.wantInReason) {
				t.Fatalf("reason = %q, want substring %q", reason, tt.wantInReason)
			}
			if tt.wantInReason == "" && reason != "" {
				t.Fatalf("non-wait reason = %q, want empty", reason)
			}
		})
	}
}

func TestApplyWorkflowDrainGate(t *testing.T) {
	t.Parallel()
	bus := newUIBus()
	u := NewUpdater("1.0.0", "", "", bus, nil)
	u.restartGate = new(atomic.Bool)
	u.inflightCount = func() int { return 2 }

	err := u.Apply(context.Background(), func(int) {}, false)
	var busy *updateBusyError
	if errors.As(err, &busy) || err == nil || !strings.Contains(err.Error(), "no manifest") {
		t.Fatalf("Apply without an update = %v, want no-manifest error without arming drain", err)
	}
	if u.WorkflowDrainPending() {
		t.Fatal("no-manifest Apply armed workflow admission pause")
	}

	u.mu.Lock()
	u.manifest = &Manifest{Version: "2.0.0", Platforms: map[string]ManifestPlatform{platformKeyCurrent(): {}}}
	u.status.Available = true
	u.mu.Unlock()
	err = u.Apply(context.Background(), func(int) {}, false)
	if !errors.As(err, &busy) || !strings.Contains(busy.reason, "2 in-flight") {
		t.Fatalf("non-forced Apply = %v, want workflow *updateBusyError", err)
	}
	if busy.cause != updateBusyCauseWorkflow || busy.inflight != 2 || busy.deadlineMs <= time.Now().UnixMilli() {
		t.Fatalf("workflow busy metadata = %+v, want cause + inflight + future deadline", busy)
	}
	if !u.WorkflowDrainPending() {
		t.Fatal("non-forced Apply did not close workflow invocation admission while draining")
	}
	if u.Status().Applying {
		t.Fatal("workflow 409 exposed applying=true before swap admission")
	}
	if got := countBusEvents(bus, "update-applying"); got != 0 {
		t.Fatalf("workflow 409 emitted %d update-applying events, want none", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = u.Apply(ctx, func(int) {}, true)
	if errors.As(err, &busy) || err == nil {
		t.Fatalf("forced Apply = %v, want a later non-busy error after gate bypass", err)
	}
}

func TestApplyFailsClosedUntilWorkflowBootReady(t *testing.T) {
	for _, mode := range []string{"manual", "automatic"} {
		t.Run(mode, func(t *testing.T) {
			u := NewUpdater("1.0.0", "", "", nil, nil)
			u.restartGate = new(atomic.Bool)
			u.mu.Lock()
			u.manifest = &Manifest{Version: "2.0.0", Platforms: map[string]ManifestPlatform{platformKeyCurrent(): {}}}
			u.status.Available = true
			u.mu.Unlock()
			var ready atomic.Bool
			u.workflowBootReady = ready.Load
			var drainChecks atomic.Int32
			u.inflightCount = func() int { drainChecks.Add(1); return 0 }
			apply := func(ctx context.Context) error { return u.Apply(ctx, func(int) {}, false) }
			var err error
			if mode == "automatic" {
				err = runAutoApplyIfEligible(context.Background(), true, u.Status(), apply)
			} else {
				err = apply(context.Background())
			}
			var busy *updateBusyError
			if !errors.As(err, &busy) || busy.cause != updateBusyCauseWorkflow || !strings.Contains(busy.reason, "boot") {
				t.Fatalf("%s Apply during workflow load = %v, want workflow boot busy", mode, err)
			}
			if got := drainChecks.Load(); got != 0 {
				t.Fatalf("%s Apply inspected the unpublished board %d time(s)", mode, got)
			}
			u.mu.Lock()
			lease := u.leaseHeld
			u.mu.Unlock()
			if lease || u.WorkflowDrainPending() {
				t.Fatalf("%s Apply acquired workflow update admission while boot was loading", mode)
			}

			ready.Store(true)
			u.inflightCount = func() int { drainChecks.Add(1); return 2 }
			err = apply(context.Background())
			if !errors.As(err, &busy) || busy.cause != updateBusyCauseWorkflow || busy.inflight != 2 {
				t.Fatalf("%s Apply after readiness = %v, want normal workflow drain", mode, err)
			}
			if drainChecks.Load() != 1 {
				t.Fatalf("%s ready Apply drain checks = %d, want 1", mode, drainChecks.Load())
			}
		})
	}
}

func TestApplyWorkflowDrainFailOpen(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		gate func() int
	}{
		{name: "nil"},
		{name: "panic", gate: func() int { panic("boom") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := NewUpdater("1.0.0", "", "", nil, nil)
			u.restartGate = new(atomic.Bool)
			u.inflightCount = tt.gate
			u.mu.Lock()
			u.manifest = &Manifest{Version: "2.0.0", Platforms: map[string]ManifestPlatform{platformKeyCurrent(): {}}}
			u.status.Available = true
			u.mu.Unlock()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := u.Apply(ctx, func(int) {}, false)
			var busy *updateBusyError
			if errors.As(err, &busy) || err == nil {
				t.Fatalf("Apply = %v, want later non-busy error after fail-open gate", err)
			}
		})
	}
}

func TestApplyWorkflowDrainDeadline(t *testing.T) {
	t.Parallel()
	u := NewUpdater("1.0.0", "", "", nil, nil)
	u.restartGate = new(atomic.Bool)
	u.inflightCount = func() int { return 4 }
	u.mu.Lock()
	u.manifest = &Manifest{Version: "2.0.0", Platforms: map[string]ManifestPlatform{platformKeyCurrent(): {}}}
	u.status.Available = true
	u.drainStart = time.Now().Add(-11 * time.Minute)
	u.drainWindow = 10 * time.Minute
	u.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := u.Apply(ctx, func(int) {}, false)
	var deferred *updateDeferredError
	if !errors.As(err, &deferred) || deferred.inflight != 4 {
		t.Fatalf("Apply past deadline = %v, want typed defer", err)
	}
	if u.WorkflowDrainPending() {
		t.Fatal("deadline defer left admission paused")
	}

	// Once the bounded window releases admission, a later quiet non-forced
	// attempt gets past the invocation gate. The intentionally incomplete test
	// manifest fails later; only busy/deferred would indicate a wedged drain.
	u.inflightCount = func() int { return 0 }
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	err = u.Apply(ctx, func(int) {}, false)
	var busy *updateBusyError
	if errors.As(err, &busy) || errors.As(err, &deferred) || err == nil {
		t.Fatalf("quiet retry after bounded defer = %v, want a later non-gate error", err)
	}
	if u.WorkflowDrainPending() {
		t.Fatal("later failed retry leaked the updater admission lease")
	}
}

func TestApplyWorkflowDrainDeadlineDefersBeforeDownload(t *testing.T) {
	t.Parallel()
	payload := []byte("replacement binary")
	sum := sha256.Sum256(payload)
	const manifestURL = "https://updates.example.test/update.json"
	const binaryURL = "https://updates.example.test/solud"

	u := NewUpdater("1.0.0", "", "", nil, nil)
	u.restartGate = new(atomic.Bool)
	useTestExecutable(t, u)
	u.httpClient = &http.Client{Transport: staticRoundTripper{binaryURL: payload}}
	u.manifestURL = manifestURL
	u.mu.Lock()
	u.manifest = &Manifest{Version: "2.0.0", Platforms: map[string]ManifestPlatform{
		platformKeyCurrent(): {URL: binaryURL, SHA256: fmt.Sprintf("%x", sum)},
	}}
	u.status.Available = true
	u.drainStart = time.Now().Add(-11 * time.Minute)
	u.drainWindow = 10 * time.Minute
	u.mu.Unlock()

	var calls atomic.Int32
	u.inflightCount = func() int { calls.Add(1); return 2 }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := u.Apply(ctx, func(int) {}, false)
	var deferred *updateDeferredError
	if !errors.As(err, &deferred) {
		t.Fatalf("Apply past deadline = %v, want typed defer", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("workflow gate calls = %d, want entry-only defer", calls.Load())
	}
	self, _ := u.executable()
	if _, statErr := os.Stat(self + ".new"); !os.IsNotExist(statErr) {
		t.Fatalf("deadline defer downloaded binary: %v", statErr)
	}
}

// Retain the historical declaration for test-integrity tracking while the
// replacement assertion above pins the lossless deadline behavior.
func TestApplyWorkflowDrainDeadlinePassesPreSwapRecheck(t *testing.T) {
	TestApplyWorkflowDrainDeadlineDefersBeforeDownload(t)
}

func TestApplyWorkflowDrainClearsWhenDrained(t *testing.T) {
	t.Parallel()
	u := NewUpdater("1.0.0", "", "", nil, nil)
	u.restartGate = new(atomic.Bool)
	u.mu.Lock()
	u.manifest = &Manifest{Version: "2.0.0", Platforms: map[string]ManifestPlatform{platformKeyCurrent(): {}}}
	u.status.Available = true
	u.mu.Unlock()
	inflight := 2
	u.inflightCount = func() int { return inflight }
	if err := u.Apply(context.Background(), func(int) {}, false); err == nil {
		t.Fatal("first Apply should defer")
	}
	inflight = 0
	err := u.Apply(context.Background(), func(int) {}, false)
	var busy *updateBusyError
	if errors.As(err, &busy) {
		t.Fatalf("drained Apply still busy: %v", err)
	}
	u.mu.Lock()
	start := u.drainStart
	u.mu.Unlock()
	if !start.IsZero() {
		t.Fatalf("drain start not cleared: %v", start)
	}
}

func TestWorkflowDrainStatusSurface(t *testing.T) {
	t.Parallel()
	u := NewUpdater("1.0.0", "", "", nil, nil)
	start := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	u.mu.Lock()
	u.status.Available = true
	u.drainStart = start
	u.drainInflight = 3
	u.drainWindow = 10 * time.Minute
	u.mu.Unlock()

	status := u.Status()
	if !status.WorkflowDeferred || status.WorkflowInflight != 3 || status.WorkflowDeferReason == "" {
		t.Fatalf("drain status = %+v", status)
	}
	if status.WorkflowDeferSinceMs != start.UnixMilli() || status.WorkflowDeferDeadlineMs-status.WorkflowDeferSinceMs != (10*time.Minute).Milliseconds() {
		t.Fatalf("drain timestamps = since %d deadline %d", status.WorkflowDeferSinceMs, status.WorkflowDeferDeadlineMs)
	}
	payload, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"workflowDeferred":true`) {
		t.Fatalf("status JSON = %s", payload)
	}

	u.mu.Lock()
	u.drainStart = time.Time{}
	u.drainInflight = 0
	u.mu.Unlock()
	status = u.Status()
	if status.WorkflowDeferred || status.WorkflowDeferSinceMs != 0 || status.WorkflowDeferDeadlineMs != 0 || status.WorkflowInflight != 0 || status.WorkflowDeferReason != "" {
		t.Fatalf("cleared drain status = %+v", status)
	}
	payload, err = json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"workflowDeferred", "workflowDeferReason", "workflowDeferSinceMs", "workflowDeferDeadlineMs", "workflowInflight"} {
		if strings.Contains(string(payload), field) {
			t.Fatalf("idle status should omit %s: %s", field, payload)
		}
	}

	u.mu.Lock()
	u.status.Available = true
	u.drainStart = time.Now().Add(-11 * time.Minute)
	u.drainInflight = 2
	u.drainWindow = 10 * time.Minute
	u.mu.Unlock()
	status = u.Status()
	if status.WorkflowDeferred || status.WorkflowDeferReason != "" || status.WorkflowDeferSinceMs != 0 || status.WorkflowDeferDeadlineMs != 0 || status.WorkflowInflight != 0 {
		t.Fatalf("expired drain status = %+v, want idle fields", status)
	}
	payload, err = json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"workflowDeferred", "workflowDeferReason", "workflowDeferSinceMs", "workflowDeferDeadlineMs", "workflowInflight"} {
		if strings.Contains(string(payload), field) {
			t.Fatalf("expired status should omit %s: %s", field, payload)
		}
	}
}

// drainProbeTransport wraps staticRoundTripper and invokes onBinary just
// before serving the binary payload, letting tests observe updater state at
// download time (safe: Apply holds no updater lock during the download).
type drainProbeTransport struct {
	static    staticRoundTripper
	binaryURL string
	onBinary  func()
}

func (p *drainProbeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() == p.binaryURL && p.onBinary != nil {
		p.onBinary()
	}
	return p.static.RoundTrip(r)
}

// useTestExecutable gives an updater a private binary path so parallel Apply
// tests never share download or swap artifacts beside the real test binary.
func useTestExecutable(t *testing.T, u *Updater) {
	t.Helper()
	selfPath := filepath.Join(t.TempDir(), "solud")
	if err := os.WriteFile(selfPath, []byte("test daemon binary\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	u.executable = func() (string, error) { return selfPath, nil }
}

// leaseTestUpdater returns an updater with a cached applicable manifest
// whose platform binary is payload, plus the binary URL to serve it at.
func leaseTestUpdater(t *testing.T, payload []byte) (*Updater, string) {
	t.Helper()
	sum := sha256.Sum256(payload)
	const binaryURL = "https://updates.example.test/solud"
	u := NewUpdater("1.0.0", "", "", nil, nil)
	u.restartGate = new(atomic.Bool)
	useTestExecutable(t, u)
	u.manifestURL = "https://updates.example.test/update.json"
	u.mu.Lock()
	u.manifest = &Manifest{Version: "2.0.0", Platforms: map[string]ManifestPlatform{
		platformKeyCurrent(): {URL: binaryURL, SHA256: fmt.Sprintf("%x", sum)},
	}}
	u.status.Available = true
	u.mu.Unlock()
	return u, binaryURL
}

// removeDownloadedNew deletes the verified download parked next to the test
// binary. Invoked from inflightCount stubs at the pre-swap gate so the ensuing
// harmless swap error stops Apply without replacing the running test binary.
func removeDownloadedNew(u *Updater) {
	if self, err := u.executable(); err == nil {
		if resolved, resolveErr := filepath.EvalSymlinks(self); resolveErr == nil {
			self = resolved
		}
		_ = os.Remove(self + ".new")
	}
}

// The v0.0.987 incident reproduction: a drain window is armed, in-flight
// momentarily reaches zero, and Apply passes its entry gate. Admission must
// stay paused through the download — carried by the LEASE, not the (already
// consumed) window — and no fresh window may be armed.
func TestApplyHoldsAdmissionLeaseThroughDownload(t *testing.T) {
	t.Parallel()
	payload := []byte("replacement binary")
	u, binaryURL := leaseTestUpdater(t, payload)
	var pendingDuringDownload, windowClearedDuringDownload, leaseDuringDownload, applyingDuringDownload atomic.Bool
	u.httpClient = &http.Client{Transport: &drainProbeTransport{
		static:    staticRoundTripper{binaryURL: payload},
		binaryURL: binaryURL,
		onBinary: func() {
			pendingDuringDownload.Store(u.WorkflowDrainPending())
			applyingDuringDownload.Store(u.Status().Applying)
			u.mu.Lock()
			windowClearedDuringDownload.Store(u.drainStart.IsZero())
			leaseDuringDownload.Store(u.leaseHeld)
			u.mu.Unlock()
		},
	}}
	u.mu.Lock()
	u.drainStart = time.Now().Add(-time.Minute) // armed by a prior deferred Apply
	u.drainInflight = 1
	u.mu.Unlock()
	var calls atomic.Int32
	u.inflightCount = func() int {
		if calls.Add(1) == 2 {
			removeDownloadedNew(u)
		}
		return 0
	}

	err := u.Apply(context.Background(), func(int) {}, false)
	var busy *updateBusyError
	if errors.As(err, &busy) || err == nil || !strings.Contains(err.Error(), "swap:") {
		t.Fatalf("Apply = %v, want post-gate swap error", err)
	}
	if !pendingDuringDownload.Load() {
		t.Fatal("WorkflowDrainPending was false during the download — momentary zero-inflight reopened admission")
	}
	if !windowClearedDuringDownload.Load() || !leaseDuringDownload.Load() {
		t.Fatalf("during download: windowCleared=%v leaseHeld=%v, want the lease (not a window) carrying the pause",
			windowClearedDuringDownload.Load(), leaseDuringDownload.Load())
	}
	if applyingDuringDownload.Load() {
		t.Fatal("download phase exposed applying=true before the final admission checks")
	}
	u.mu.Lock()
	start, lease := u.drainStart, u.leaseHeld
	u.mu.Unlock()
	if !start.IsZero() {
		t.Fatalf("fresh window armed during a clean attempt: %v", start)
	}
	if lease {
		t.Fatal("lease still held after Apply returned")
	}
	if u.WorkflowDrainPending() {
		t.Fatal("admission still paused after the failed attempt")
	}
}

// A straggler invocation admitted during the engine's gate-poll race aborts
// the attempt at the pre-swap re-check — but with exactly ONE window re-arm
// that keeps admission paused between attempts, so the retry converges
// instead of cycling download/abort/re-arm indefinitely.
func TestApplyPreSwapStragglerConverges(t *testing.T) {
	t.Parallel()
	payload := []byte("replacement binary")
	u, binaryURL := leaseTestUpdater(t, payload)
	u.bus = newUIBus()
	var downloads atomic.Int32
	u.httpClient = &http.Client{Transport: &drainProbeTransport{
		static:    staticRoundTripper{binaryURL: payload},
		binaryURL: binaryURL,
		onBinary:  func() { downloads.Add(1) },
	}}
	var calls atomic.Int32
	u.inflightCount = func() int {
		switch calls.Add(1) {
		case 2:
			return 1 // straggler surfaces at the first attempt's pre-swap gate
		case 4:
			removeDownloadedNew(u) // stop the retry at a harmless swap error
		}
		return 0
	}

	err := u.Apply(context.Background(), func(int) {}, false)
	var busy *updateBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("straggler Apply = %v, want *updateBusyError", err)
	}
	if u.Status().Applying || countBusEvents(u.bus, "update-applying") != 0 {
		t.Fatal("pre-swap workflow 409 exposed an admitted applying signal")
	}
	if self, execErr := u.executable(); execErr == nil {
		if resolved, resolveErr := filepath.EvalSymlinks(self); resolveErr == nil {
			self = resolved
		}
		if _, statErr := os.Stat(self + ".new"); statErr == nil {
			t.Fatal("aborted attempt left .new behind")
		}
	}
	u.mu.Lock()
	start, lease := u.drainStart, u.leaseHeld
	u.mu.Unlock()
	if start.IsZero() {
		t.Fatal("aborted attempt armed no window — admission would reopen between attempts")
	}
	if lease {
		t.Fatal("aborted attempt left the lease held")
	}
	if !u.WorkflowDrainPending() {
		t.Fatal("admission not paused between the abort and the retry")
	}

	// The 30s retry: the straggler has drained, both gates pass, and the
	// attempt reaches the (harmlessly failing) swap with no second re-arm.
	err = u.Apply(context.Background(), func(int) {}, false)
	if errors.As(err, &busy) || err == nil || !strings.Contains(err.Error(), "swap:") {
		t.Fatalf("retry Apply = %v, want post-gate swap error", err)
	}
	if got := downloads.Load(); got != 2 {
		t.Fatalf("download attempts = %d, want exactly 2 (one per Apply attempt)", got)
	}
	u.mu.Lock()
	start = u.drainStart
	u.mu.Unlock()
	if !start.IsZero() {
		t.Fatalf("second re-arm after the retry: %v — the cycle would not converge", start)
	}
	if u.WorkflowDrainPending() {
		t.Fatal("admission still paused after the retry ended")
	}
}

func TestApplyPreSwapStragglerAtDeadlineDefersWithoutSwap(t *testing.T) {
	t.Parallel()
	payload := []byte("replacement binary")
	u, binaryURL := leaseTestUpdater(t, payload)
	var downloads atomic.Int32
	u.httpClient = &http.Client{Transport: &drainProbeTransport{
		static: staticRoundTripper{binaryURL: payload}, binaryURL: binaryURL,
		onBinary: func() { downloads.Add(1) },
	}}
	var calls atomic.Int32
	u.inflightCount = func() int {
		if calls.Add(1) == 1 {
			return 0 // quiet entry takes the admission lease
		}
		// Deterministically model a straggler whose bounded pre-swap window has
		// reached its deadline before the second check samples it.
		u.mu.Lock()
		u.drainStart = time.Now().Add(-u.drainWindow - time.Second)
		u.mu.Unlock()
		return 1
	}
	err := u.Apply(context.Background(), func(int) {}, false)
	var deferred *updateDeferredError
	if !errors.As(err, &deferred) || downloads.Load() != 1 {
		t.Fatalf("pre-swap straggler err=%v downloads=%d", err, downloads.Load())
	}
	self, _ := u.executable()
	if got, readErr := os.ReadFile(self); readErr != nil || string(got) == string(payload) {
		t.Fatalf("straggler swapped binary got=%q err=%v", got, readErr)
	}
	if _, statErr := os.Stat(self + ".new"); !os.IsNotExist(statErr) {
		t.Fatalf("straggler defer retained download: %v", statErr)
	}
	if u.WorkflowDrainPending() {
		t.Fatal("straggler deadline defer left admission paused")
	}
}

// Every Apply failure must release the lease so queued work resumes.
func TestApplyFailureReleasesLease(t *testing.T) {
	t.Parallel()
	assertReleased := func(t *testing.T, u *Updater) {
		t.Helper()
		u.mu.Lock()
		lease := u.leaseHeld
		u.mu.Unlock()
		if lease {
			t.Fatal("lease still held after Apply failure")
		}
		if u.WorkflowDrainPending() {
			t.Fatal("admission still paused after Apply failure")
		}
	}

	t.Run("download failure", func(t *testing.T) {
		payload := []byte("replacement binary")
		u, binaryURL := leaseTestUpdater(t, payload)
		u.httpClient = &http.Client{Transport: staticRoundTripper{binaryURL: []byte("tampered bytes")}}
		u.inflightCount = func() int { return 0 }
		err := u.Apply(context.Background(), func(int) {}, false)
		if err == nil || !strings.Contains(err.Error(), "download:") {
			t.Fatalf("Apply = %v, want sha-mismatch download error", err)
		}
		assertReleased(t, u)
	})

	t.Run("swap failure", func(t *testing.T) {
		payload := []byte("replacement binary")
		u, binaryURL := leaseTestUpdater(t, payload)
		u.httpClient = &http.Client{Transport: staticRoundTripper{binaryURL: payload}}
		var calls atomic.Int32
		u.inflightCount = func() int {
			if calls.Add(1) == 2 {
				removeDownloadedNew(u)
			}
			return 0
		}
		err := u.Apply(context.Background(), func(int) {}, false)
		if err == nil || !strings.Contains(err.Error(), "swap:") {
			t.Fatalf("Apply = %v, want swap error", err)
		}
		self, _ := u.executable()
		if _, statErr := os.Stat(restartIntentPath(self)); !os.IsNotExist(statErr) {
			t.Fatalf("swap failure retained restart intent: %v", statErr)
		}
		assertReleased(t, u)
	})

	t.Run("control halt", func(t *testing.T) {
		payload := []byte("replacement binary")
		u, binaryURL := leaseTestUpdater(t, payload)
		u.httpClient = &http.Client{Transport: staticRoundTripper{
			binaryURL:      payload,
			u.controlURL(): []byte("{"), // fetch fails; the fresh cached halt remains authoritative
		}}
		u.inflightCount = func() int { return 0 }
		u.mu.Lock()
		u.control = &UpdateControl{Paused: true}
		u.controlFetchedAt = time.Now()
		u.mu.Unlock()

		err := u.Apply(context.Background(), func(int) {}, false)
		if err == nil || !strings.Contains(err.Error(), "update blocked:") {
			t.Fatalf("Apply = %v, want control-file halt", err)
		}
		assertReleased(t, u)
	})
}

// Behavioral replacement for the old deadline swap-through test: a deadline
// now defers before download and leaves neither a lease nor window armed.
func TestApplyDeadlineExpiredDefersAndReopensAdmission(t *testing.T) {
	t.Parallel()
	payload := []byte("replacement binary")
	u, binaryURL := leaseTestUpdater(t, payload)
	var downloads atomic.Int32
	u.httpClient = &http.Client{Transport: &drainProbeTransport{
		static:    staticRoundTripper{binaryURL: payload},
		binaryURL: binaryURL,
		onBinary:  func() { downloads.Add(1) },
	}}
	u.mu.Lock()
	u.drainStart = time.Now().Add(-11 * time.Minute)
	u.drainWindow = 10 * time.Minute
	u.mu.Unlock()
	u.inflightCount = func() int { return 2 }

	err := u.Apply(context.Background(), func(int) {}, false)
	var deferred *updateDeferredError
	if !errors.As(err, &deferred) {
		t.Fatalf("Apply past deadline = %v, want typed defer", err)
	}
	if downloads.Load() != 0 {
		t.Fatalf("deadline defer downloaded %d binaries", downloads.Load())
	}
	u.mu.Lock()
	lease := u.leaseHeld
	u.mu.Unlock()
	if lease {
		t.Fatal("lease still held after Apply returned")
	}
	if u.WorkflowDrainPending() {
		t.Fatal("deadline defer should reopen admission after the attempt")
	}
}

// Retain the historical declaration for test-integrity tracking while the
// replacement assertion above proves that busy workers are never swapped through.
func TestApplyDeadlineExpiredTakesLeaseAndProceeds(t *testing.T) {
	TestApplyDeadlineExpiredDefersAndReopensAdmission(t)
}

// force=true skips both drain checks and therefore the lease: workflow
// admission stays open for the entire forced attempt.
func TestForcedApplyTakesNoLease(t *testing.T) {
	t.Parallel()
	payload := []byte("replacement binary")
	u, binaryURL := leaseTestUpdater(t, payload)
	var pendingDuringDownload atomic.Bool
	// Serve tampered bytes so the forced apply stops at a harmless
	// sha-mismatch download error instead of swapping the test binary.
	u.httpClient = &http.Client{Transport: &drainProbeTransport{
		static:    staticRoundTripper{binaryURL: []byte("tampered bytes")},
		binaryURL: binaryURL,
		onBinary:  func() { pendingDuringDownload.Store(u.WorkflowDrainPending()) },
	}}
	var gateCalls atomic.Int32
	u.inflightCount = func() int { gateCalls.Add(1); return 5 }
	u.mu.Lock()
	u.deferAttempts = 4
	u.nextRetryTime = time.Now().Add(30 * time.Minute)
	u.mu.Unlock()

	err := u.Apply(context.Background(), func(int) {}, true)
	if err == nil || !strings.Contains(err.Error(), "download:") {
		t.Fatalf("forced Apply = %v, want download error", err)
	}
	if gateCalls.Load() != 0 {
		t.Fatalf("forced apply consulted the workflow gate %d times, want 0", gateCalls.Load())
	}
	if pendingDuringDownload.Load() {
		t.Fatal("forced apply paused workflow admission during download")
	}
	u.mu.Lock()
	lease := u.leaseHeld
	u.mu.Unlock()
	if lease || u.WorkflowDrainPending() {
		t.Fatal("forced apply took the admission lease")
	}
}

func TestApplyDefersWhileReleasePublishInFlight(t *testing.T) {
	t.Parallel()
	u := NewUpdater("1.0.0", "", "", nil, nil)
	u.restartGate = new(atomic.Bool)
	u.mu.Lock()
	u.manifest = &Manifest{Version: "2.0.0", Platforms: map[string]ManifestPlatform{platformKeyCurrent(): {}}}
	u.status.Available = true
	u.mu.Unlock()
	publishing := true
	u.releasePublishInflight = func() (string, bool) {
		if publishing {
			return "rtr_abc", true
		}
		return "", false
	}
	inflight := 2
	u.inflightCount = func() int { return inflight }
	started := time.Now()
	err := u.Apply(context.Background(), func(int) {}, false)
	var deferred *updateDeferredError
	if !errors.As(err, &deferred) || deferred.publishTrainID != "rtr_abc" || !strings.Contains(deferred.reason, "rtr_abc") {
		t.Fatalf("Apply = %v, defer=%+v", err, deferred)
	}
	if deferred.nextRetryAt.Before(started.Add(110*time.Second)) || deferred.nextRetryAt.After(started.Add(130*time.Second)) {
		t.Fatalf("next retry = %v, started %v", deferred.nextRetryAt, started)
	}
	if u.WorkflowDrainPending() || u.deferAttempts != 0 {
		t.Fatalf("publish defer pending=%v attempts=%d", u.WorkflowDrainPending(), u.deferAttempts)
	}
	status := u.Status()
	if status.WorkflowDeferState != "publish" || !strings.Contains(status.WorkflowDeferReason, "rtr_abc") || status.WorkflowNextRetryMs <= time.Now().UnixMilli() {
		t.Fatalf("status = %+v", status)
	}
	err = u.Apply(context.Background(), func(int) {}, false)
	if !errors.As(err, &deferred) || u.deferAttempts != 0 {
		t.Fatalf("second Apply = %v attempts=%d", err, u.deferAttempts)
	}

	publishing = false
	inflight = 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = u.Apply(ctx, func(int) {}, false)
	var busy *updateBusyError
	if errors.As(err, &deferred) || errors.As(err, &busy) {
		t.Fatalf("completed publish still gated Apply: %v", err)
	}
	if u.Status().WorkflowDeferState == "publish" {
		t.Fatalf("stale publish status = %+v", u.Status())
	}
}

func TestApplyPreSwapRecheckDefersWhileReleasePublishInFlight(t *testing.T) {
	t.Parallel()
	payload := []byte("replacement binary")
	u, binaryURL := leaseTestUpdater(t, payload)
	u.httpClient = &http.Client{Transport: staticRoundTripper{binaryURL: payload}}
	u.inflightCount = func() int { return 0 }
	var calls atomic.Int32
	u.releasePublishInflight = func() (string, bool) {
		if calls.Add(1) > 1 {
			return "rtr_preswap", true
		}
		return "", false
	}
	self, err := u.executable()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	err = u.Apply(context.Background(), func(int) {}, false)
	var deferred *updateDeferredError
	if !errors.As(err, &deferred) || deferred.publishTrainID != "rtr_preswap" {
		t.Fatalf("Apply = %v, defer=%+v", err, deferred)
	}
	if _, err := os.Stat(self + ".new"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("download residue stat error = %v", err)
	}
	after, err := os.ReadFile(self)
	if err != nil || string(after) != string(before) {
		t.Fatalf("binary changed: before=%q after=%q err=%v", before, after, err)
	}
	if u.WorkflowDrainPending() {
		t.Fatal("pre-swap publish defer retained admission lease")
	}
}

func TestPublishDeferNeverShortensQuietBackoff(t *testing.T) {
	t.Parallel()
	u := NewUpdater("1.0.0", "", "", nil, nil)
	u.releasePublishInflight = func() (string, bool) { return "rtr_long", true }
	next := time.Now().Add(30 * time.Minute)
	u.mu.Lock()
	u.nextRetryTime = next
	u.deferAttempts = 3
	u.mu.Unlock()
	check := u.checkWorkflowDrain(time.Now())
	if check.outcome != drainDefer || !check.nextRetryAt.Equal(next) || check.attempts != 3 {
		t.Fatalf("check = %+v", check)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.nextRetryTime.Equal(next) || u.deferAttempts != 3 {
		t.Fatalf("next=%v attempts=%d", u.nextRetryTime, u.deferAttempts)
	}
}

func TestForcedApplyIgnoresReleasePublishGate(t *testing.T) {
	t.Parallel()
	payload := []byte("replacement binary")
	u, binaryURL := leaseTestUpdater(t, payload)
	u.httpClient = &http.Client{Transport: staticRoundTripper{binaryURL: []byte("tampered")}}
	var calls atomic.Int32
	u.releasePublishInflight = func() (string, bool) {
		calls.Add(1)
		return "rtr_force", true
	}
	err := u.Apply(context.Background(), func(int) {}, true)
	if err == nil || !strings.Contains(err.Error(), "download:") {
		t.Fatalf("forced Apply = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("forced Apply consulted publish gate %d times", calls.Load())
	}
}

func TestClearWorkflowDrainResetsPublishDefer(t *testing.T) {
	t.Parallel()
	u := NewUpdater("1.0.0", "", "", nil, nil)
	u.releasePublishInflight = func() (string, bool) { return "rtr_clear", true }
	if check := u.checkWorkflowDrain(time.Now()); check.publishTrainID != "rtr_clear" {
		t.Fatalf("check = %+v", check)
	}
	u.mu.Lock()
	u.clearWorkflowDrainLocked()
	train, deferredAt := u.publishDeferTrain, u.publishDeferredAt
	u.mu.Unlock()
	if train != "" || !deferredAt.IsZero() {
		t.Fatalf("publish defer retained train=%q at=%v", train, deferredAt)
	}
}

// The lease is in-memory only: a fresh updater — the state after a daemon
// restart mid-download — must report admission open with nothing armed, so a
// crashed apply can never starve queued tasks.
func TestFreshUpdaterHasNoLease(t *testing.T) {
	t.Parallel()
	u := NewUpdater("1.0.0", "", "", nil, nil)
	if u.WorkflowDrainPending() {
		t.Fatal("fresh updater pauses admission")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.leaseHeld || !u.leaseSince.IsZero() || !u.drainStart.IsZero() || u.drainInflight != 0 ||
		u.deferAttempts != 0 || !u.nextRetryTime.IsZero() || !u.deferAt.IsZero() {
		t.Fatalf("fresh updater drain state: lease=%v since=%v start=%v inflight=%d",
			u.leaseHeld, u.leaseSince, u.drainStart, u.drainInflight)
	}
}

func TestQuietDeferBackoffBoundedAndAutomaticRetryWaits(t *testing.T) {
	t.Parallel()
	u := NewUpdater("1.0.0", "", "", nil, nil)
	now := time.Now()
	u.mu.Lock()
	for i := 0; i < 10; i++ {
		u.recordQuietDeferLocked(now, i+1)
		backoff := u.nextRetryTime.Sub(now)
		if backoff > quietBackoffMax {
			t.Fatalf("attempt %d backoff = %s, exceeds %s", i+1, backoff, quietBackoffMax)
		}
	}
	if got := u.nextRetryTime.Sub(now); got != quietBackoffMax {
		t.Fatalf("capped backoff = %s, want %s", got, quietBackoffMax)
	}
	status := u.statusCopy()
	u.mu.Unlock()
	status.Available = true
	called := false
	if err := runAutoApplyIfEligible(context.Background(), true, status, func(context.Context) error {
		called = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("automatic apply ran before quiet-window retry time")
	}
}

func TestWorkflowUpdateLosslessAutomaticRetry(t *testing.T) {
	// Drive one updater through the complete automatic drain lifecycle with a
	// deterministic clock. The callback stands in for the destructive swap:
	// it may only be reached after the real drain state machine grants a lease.
	u := NewUpdater("1.0.0", "", "", nil, nil)
	u.status.Available = true
	now := time.Now().Truncate(time.Millisecond)
	inflight := 2
	swapCalls := 0
	u.inflightCount = func() int { return inflight }

	type observation struct {
		check drainCheck
		err   error
	}
	ordinaryAttempt := func(at time.Time) observation {
		check := u.checkWorkflowDrain(at)
		switch check.outcome {
		case drainWait:
			return observation{check: check, err: &updateBusyError{
				cause: updateBusyCauseWorkflow, reason: check.reason,
				inflight: check.inflight, deadlineMs: check.deadlineMs,
			}}
		case drainDefer:
			return observation{check: check, err: &updateDeferredError{
				reason: check.reason, inflight: check.inflight,
				nextRetryAt: check.nextRetryAt, attempts: check.attempts,
			}}
		default:
			swapCalls++
			return observation{check: check}
		}
	}

	steps := []struct {
		name         string
		at           time.Time
		inflight     int
		wantOutcome  drainOutcome
		wantAttempts int
		wantBackoff  time.Duration
		wantPending  bool
		wantLease    bool
		wantSwaps    int
		wantTyped    string
	}{
		{
			name: "automatic update waits for active workers", at: now, inflight: 2,
			wantOutcome: drainWait, wantPending: true, wantSwaps: 0,
			wantTyped: "busy",
		},
		{
			name: "expired window defers and reopens admission", at: now.Add(u.drainWindow), inflight: 2,
			wantOutcome: drainDefer, wantAttempts: 1, wantBackoff: quietBackoffBase,
			wantPending: false, wantSwaps: 0, wantTyped: "deferred",
		},
		{
			name: "busy retry opens another bounded drain", at: now.Add(u.drainWindow + quietBackoffBase), inflight: 1,
			wantOutcome: drainWait, wantAttempts: 1, wantPending: true, wantSwaps: 0,
			wantTyped: "busy",
		},
		{
			name: "busy retry remains deferred", at: now.Add(2*u.drainWindow + quietBackoffBase), inflight: 1,
			wantOutcome: drainDefer, wantAttempts: 2, wantBackoff: 2 * quietBackoffBase,
			wantPending: false, wantSwaps: 0, wantTyped: "deferred",
		},
		{
			name: "quiet retry acquires lease and proceeds", at: now.Add(2*u.drainWindow + 3*quietBackoffBase), inflight: 0,
			wantOutcome: drainProceed, wantAttempts: 2, wantPending: true,
			wantLease: true, wantSwaps: 1,
		},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			inflight = step.inflight
			got := ordinaryAttempt(step.at)
			if got.check.outcome != step.wantOutcome || got.check.inflight != step.inflight {
				t.Fatalf("decision = outcome %v inflight %d, want outcome %v inflight %d",
					got.check.outcome, got.check.inflight, step.wantOutcome, step.inflight)
			}
			switch step.wantTyped {
			case "busy":
				var busy *updateBusyError
				if !errors.As(got.err, &busy) || busy.cause != updateBusyCauseWorkflow || busy.inflight != step.inflight {
					t.Fatalf("decision error = %T %v, want typed workflow busy for %d workers", got.err, got.err, step.inflight)
				}
			case "deferred":
				var deferred *updateDeferredError
				if !errors.As(got.err, &deferred) || deferred.inflight != step.inflight || deferred.attempts != step.wantAttempts {
					t.Fatalf("decision error = %T %v, want typed defer attempt %d for %d workers",
						got.err, got.err, step.wantAttempts, step.inflight)
				}
			case "":
				if got.err != nil {
					t.Fatalf("quiet decision error = %v, want proceed", got.err)
				}
			default:
				t.Fatalf("unknown expected error kind %q", step.wantTyped)
			}
			u.mu.Lock()
			attempts := u.deferAttempts
			nextRetry := u.nextRetryTime
			lease := u.leaseHeld
			u.mu.Unlock()
			if attempts != step.wantAttempts {
				t.Fatalf("defer attempts = %d, want %d", attempts, step.wantAttempts)
			}
			if step.wantBackoff != 0 {
				backoff := nextRetry.Sub(step.at)
				if backoff != step.wantBackoff || backoff > quietBackoffMax {
					t.Fatalf("next retry backoff = %s, want %s (max %s)", backoff, step.wantBackoff, quietBackoffMax)
				}
				if got.check.nextRetryAt != nextRetry || got.check.attempts != attempts {
					t.Fatalf("exposed retry = attempts %d at %v, want attempts %d at %v",
						got.check.attempts, got.check.nextRetryAt, attempts, nextRetry)
				}
				status := u.Status()
				if status.WorkflowDeferState != "backoff" || status.WorkflowNextRetryMs != nextRetry.UnixMilli() ||
					status.WorkflowDeferAttempts != attempts || status.WorkflowInflight != step.inflight {
					t.Fatalf("exposed status = %+v, want backoff attempt %d at %v for %d workers",
						status, attempts, nextRetry, step.inflight)
				}
			}
			if pending := u.WorkflowDrainPending(); pending != step.wantPending {
				t.Fatalf("admission pending = %v, want %v", pending, step.wantPending)
			}
			if lease != step.wantLease {
				t.Fatalf("lease held = %v, want %v", lease, step.wantLease)
			}
			if swapCalls != step.wantSwaps {
				t.Fatalf("swap calls = %d, want %d", swapCalls, step.wantSwaps)
			}
		})
	}
}

func TestCheckNewManifestVersionResetsQuietBackoff(t *testing.T) {
	disableManifestSignature(t)
	const manifestURL = "https://updates.example.test/new-version.json"
	body := []byte(`{"version":"3.0.0","platforms":{"` + platformKeyCurrent() + `":{"url":"https://updates.example.test/solud","sha256":"abc"}}}`)
	u := NewUpdater("1.0.0", "", "", nil, nil)
	u.manifestURL = manifestURL
	u.httpClient = &http.Client{Transport: staticRoundTripper{manifestURL: body}}
	u.mu.Lock()
	u.status.Latest = "2.0.0"
	u.status.Available = true
	u.deferAt = time.Now().Add(-time.Minute)
	u.deferInflight = 2
	u.deferAttempts = 4
	u.nextRetryTime = time.Now().Add(30 * time.Minute)
	u.lastDrainDuration = 7 * time.Minute
	u.mu.Unlock()
	status, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Latest != "3.0.0" || status.WorkflowDeferAttempts != 0 || status.WorkflowNextRetryMs != 0 || status.WorkflowDeferState != "" {
		t.Fatalf("new-version status retained backoff: %+v", status)
	}
	u.mu.Lock()
	attempts, next, drainDuration := u.deferAttempts, u.nextRetryTime, u.lastDrainDuration
	u.mu.Unlock()
	if attempts != 0 || !next.IsZero() || drainDuration != 0 {
		t.Fatalf("new-version state retained backoff telemetry: attempts=%d next=%v drain=%s", attempts, next, drainDuration)
	}
}

func TestQuietDeferThenQuietRetrySwaps(t *testing.T) {
	t.Setenv("INVOCATION_ID", "test-systemd-unit")
	payload := []byte("quiet replacement binary")
	u, binaryURL := leaseTestUpdater(t, payload)
	u.httpClient = &http.Client{Transport: staticRoundTripper{binaryURL: payload}}
	u.mu.Lock()
	u.drainStart = time.Now().Add(-u.drainWindow - time.Second)
	u.mu.Unlock()
	inflight := 1
	u.inflightCount = func() int { return inflight }

	err := u.Apply(context.Background(), func(int) {}, false)
	var deferred *updateDeferredError
	if !errors.As(err, &deferred) || u.WorkflowDrainPending() {
		t.Fatalf("busy deadline = %v pending=%v, want defer with open admission", err, u.WorkflowDrainPending())
	}
	status := u.Status()
	calls := 0
	if err := runAutoApplyIfEligible(context.Background(), true, status, func(context.Context) error {
		calls++
		return nil
	}); err != nil || calls != 0 {
		t.Fatalf("backoff auto apply err=%v calls=%d", err, calls)
	}

	inflight = 0
	u.mu.Lock()
	u.nextRetryTime = time.Now().Add(-time.Second)
	u.mu.Unlock()
	if err := u.Apply(context.Background(), func(int) {}, false); err != nil {
		t.Fatalf("quiet retry: %v", err)
	}
	self, _ := u.executable()
	got, err := os.ReadFile(self)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("swapped binary = %q err=%v", got, err)
	}
	intent := readAndClearRestartIntent(self, time.Now())
	if intent == nil {
		t.Fatal("quiet retry did not persist restart intent")
	}
	if intent.DrainDurationMs != 0 {
		t.Fatalf("quiet retry drain duration = %dms, want current attempt's 0ms", intent.DrainDurationMs)
	}
	u.mu.Lock()
	attempts, next := u.deferAttempts, u.nextRetryTime
	u.mu.Unlock()
	if attempts != 0 || !next.IsZero() {
		t.Fatalf("successful swap retained backoff: attempts=%d next=%v", attempts, next)
	}
}

func TestAutomaticUpdateAlternatesDrainAndBackoffWithoutSwapWhenBusy(t *testing.T) {
	t.Parallel()
	payload := []byte("must never download")
	u, binaryURL := leaseTestUpdater(t, payload)
	var downloads atomic.Int32
	u.httpClient = &http.Client{Transport: &drainProbeTransport{
		static: staticRoundTripper{binaryURL: payload}, binaryURL: binaryURL,
		onBinary: func() { downloads.Add(1) },
	}}
	u.inflightCount = func() int { return 1 }

	for cycle := 0; cycle < 4; cycle++ {
		// An automatic attempt opens a bounded drain window.
		st := u.Status()
		if err := runAutoApplyIfEligible(context.Background(), true, st, func(ctx context.Context) error {
			return u.Apply(ctx, func(int) {}, false)
		}); err == nil {
			t.Fatalf("cycle %d initial attempt unexpectedly admitted", cycle)
		}
		if !u.WorkflowDrainPending() {
			t.Fatalf("cycle %d did not arm a drain window", cycle)
		}

		// At its deadline, the next automatic attempt defers and reopens
		// admission instead of downloading or swapping through the invocation.
		u.mu.Lock()
		u.drainStart = time.Now().Add(-u.drainWindow - time.Second)
		u.mu.Unlock()
		st = u.Status()
		err := runAutoApplyIfEligible(context.Background(), true, st, func(ctx context.Context) error {
			return u.Apply(ctx, func(int) {}, false)
		})
		var deferred *updateDeferredError
		if !errors.As(err, &deferred) || u.WorkflowDrainPending() {
			t.Fatalf("cycle %d deadline err=%v pending=%v", cycle, err, u.WorkflowDrainPending())
		}

		// The 30s poll is rate-limited during backoff.
		called := false
		if err := runAutoApplyIfEligible(context.Background(), true, u.Status(), func(context.Context) error {
			called = true
			return nil
		}); err != nil || called {
			t.Fatalf("cycle %d backoff err=%v called=%v", cycle, err, called)
		}
		u.mu.Lock()
		u.nextRetryTime = time.Now().Add(-time.Second)
		u.mu.Unlock()
	}
	if downloads.Load() != 0 {
		t.Fatalf("persistently busy automatic update downloaded %d binaries", downloads.Load())
	}
}

func TestConcurrentApplyDuringDeadlineDeferIsStillSerialized(t *testing.T) {
	t.Parallel()
	u := NewUpdater("1.0.0", "", "", nil, nil)
	u.restartGate = new(atomic.Bool)
	u.mu.Lock()
	u.manifest = &Manifest{Version: "2.0.0", Platforms: map[string]ManifestPlatform{platformKeyCurrent(): {}}}
	u.status.Available = true
	u.drainStart = time.Now().Add(-u.drainWindow - time.Second)
	u.mu.Unlock()
	entered := make(chan struct{})
	release := make(chan struct{})
	u.inflightCount = func() int {
		close(entered)
		<-release
		return 1
	}
	first := make(chan error, 1)
	go func() { first <- u.Apply(context.Background(), func(int) {}, false) }()
	<-entered
	if u.Status().Applying {
		t.Fatal("blocked workflow-drain decision exposed applying=true")
	}
	second := u.Apply(context.Background(), func(int) {}, false)
	var restartBusy *updateBusyError
	if !errors.As(second, &restartBusy) || restartBusy.cause != updateBusyCauseRestart {
		t.Fatalf("concurrent Apply = %T %v, want typed restart busy", second, second)
	}
	close(release)
	var deferred *updateDeferredError
	if err := <-first; !errors.As(err, &deferred) {
		t.Fatalf("first Apply = %v, want deadline defer", err)
	}
}

func TestWorkflowDrainPending(t *testing.T) {
	t.Parallel()
	u := NewUpdater("1.0.0", "", "", nil, nil)
	u.mu.Lock()
	u.status.Available = true
	u.drainStart = time.Now().Add(-time.Minute)
	u.drainWindow = 10 * time.Minute
	u.mu.Unlock()
	if !u.WorkflowDrainPending() {
		t.Fatal("active available drain should be pending")
	}

	u.mu.Lock()
	u.drainStart = time.Now().Add(-11 * time.Minute)
	u.mu.Unlock()
	if u.WorkflowDrainPending() {
		t.Fatal("past-deadline drain should not be pending")
	}

	u.mu.Lock()
	u.drainStart = time.Now().Add(-time.Minute)
	u.status.Available = false
	u.mu.Unlock()
	if u.WorkflowDrainPending() {
		t.Fatal("unavailable update should not pause admission")
	}
}
