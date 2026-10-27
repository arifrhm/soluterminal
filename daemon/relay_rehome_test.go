package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"soluterminal-shared/relaymux"

	"github.com/gorilla/websocket"
)

// lockedBuffer is a log sink safe to read while other goroutines log.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func captureLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	prev := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return buf
}

func TestRelayRehomeNeedsTwoConsecutiveMismatches(t *testing.T) {
	var r relayRehome
	now := time.Now()
	r.tunnelUp("A")

	if r.observe("A", "A", now) {
		t.Fatal("same instance must not move the tunnel")
	}
	if r.observe("A", "", now) {
		t.Fatal("a cloud without the instance field must not move the tunnel")
	}
	if r.observe("A", "B", now) {
		t.Fatal("one mismatch must not move the tunnel")
	}
	if r.observe("A", "A", now) {
		t.Fatal("same instance must not move the tunnel")
	}
	if r.observe("A", "B", now) {
		t.Fatal("a match in between resets the count")
	}
	if r.observe("A", "C", now) {
		t.Fatal("two different other instances are not a confirmed move")
	}
	if !r.observe("A", "C", now) {
		t.Fatal("two consecutive answers naming C must move the tunnel")
	}
	if r.observe("B", "C", now) {
		t.Fatal("an answer for a tunnel that is no longer live must be ignored")
	}
}

func TestRelayRehomeLandedBackBacksOff(t *testing.T) {
	var r relayRehome
	now := time.Now()
	r.tunnelUp("A")
	r.observe("A", "B", now)
	if !r.observe("A", "B", now) {
		t.Fatal("want a move")
	}
	if r.shouldBackOff() {
		t.Fatal("the first move must redial at once")
	}
	r.tunnelDown()
	r.tunnelUp("A") // the redial landed back on the old instance
	if !r.shouldBackOff() {
		t.Fatal("after landing back on the old instance the next move must back off")
	}
	r.observe("A", "B", now)
	r.observe("A", "B", now)
	r.tunnelDown()
	r.tunnelUp("B")
	if r.shouldBackOff() {
		t.Fatal("a move that reached the new instance clears the backoff")
	}
}

func TestRelayRehomeStopsWhenInstancesAlternate(t *testing.T) {
	logs := captureLog(t)
	var r relayRehome
	now := time.Now()

	r.tunnelUp("A")
	r.observe("A", "B", now)
	if !r.observe("A", "B", now) {
		t.Fatal("want the first move A -> B")
	}
	r.tunnelDown()
	r.tunnelUp("B")

	// Health now sends traffic back to A: two instances are both serving.
	r.observe("B", "A", now)
	if r.observe("B", "A", now) {
		t.Fatal("moving back to an instance just left must be refused")
	}
	if n := strings.Count(logs.String(), "not moving the tunnel"); n != 1 {
		t.Fatalf("suspension logged %d times, want 1:\n%s", n, logs.String())
	}
	// Further alternation stays quiet while suspended.
	for i := 0; i < 6; i++ {
		if r.observe("B", "A", now.Add(time.Minute)) {
			t.Fatal("suspended rehome must not move the tunnel")
		}
	}
	if n := strings.Count(logs.String(), "not moving the tunnel"); n != 1 {
		t.Fatalf("suspension logged %d times, want 1", n)
	}
	// A later deploy's new instance can still be followed at once.
	soon := now.Add(2 * time.Minute)
	r.observe("B", "C", soon)
	if !r.observe("B", "C", soon) {
		t.Fatal("the alternating pair must not block a move to a new instance")
	}
	r.tunnelDown()
	r.tunnelUp("C")
	// Moving back to either alternating instance stays paused...
	r.observe("C", "A", soon)
	if r.observe("C", "A", soon) {
		t.Fatal("a move to an instance seen alternating must stay paused")
	}
	// ...until the window passes.
	later := now.Add(relayRehomeWindow + time.Minute)
	r.observe("C", "D", later)
	if !r.observe("C", "D", later) {
		t.Fatal("rehome must resume after the window")
	}
}

func TestRelayRehomeBudget(t *testing.T) {
	captureLog(t)
	var r relayRehome
	now := time.Now()
	r.tunnelUp("A")
	for i := 0; i < relayRehomeBudget; i++ {
		r.observe("A", "B", now)
		if !r.observe("A", "B", now) {
			t.Fatalf("move %d refused within budget", i+1)
		}
		r.tunnelDown()
		r.tunnelUp("A") // keeps landing back on the old instance
	}
	r.observe("A", "B", now)
	if r.observe("A", "B", now) {
		t.Fatal("moves beyond the budget must be refused")
	}
}

// rehomeHarness is a supervisor wired to a fake tunnel (which lands on the
// instance landOn returns) and a fake /api/health (which answers health()).
type rehomeHarness struct {
	sup       *relaySupervisor
	supRef    atomic.Pointer[relaySupervisor] // for the health handler goroutine
	cancel    context.CancelFunc
	starts    atomic.Int32
	live      atomic.Int32
	maxLive   atomic.Int32
	healthHit atomic.Int32
	backoffs  atomic.Int32 // moves that waited the normal backoff
}

func newRehomeHarness(t *testing.T, landOn func(dial int32) string, health func(tunnel string) map[string]any) *rehomeHarness {
	t.Helper()
	dir := t.TempDir()
	writeClaimToken(t, dir, "tok")
	h := &rehomeHarness{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			http.NotFound(w, r)
			return
		}
		h.healthHit.Add(1)
		_ = json.NewEncoder(w).Encode(health(h.supRef.Load().rehome.current()))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	t.Cleanup(cancel)
	h.sup = newRelaySupervisor(ctx, dir, "wss://example.test/relay", "wsl", nil)
	h.sup.pollInterval = 10 * time.Millisecond
	h.sup.healthInterval = 10 * time.Millisecond
	h.sup.healthURL = srv.URL + "/api/health"
	// A backed-off move is counted and waits an hour, so a move that redials
	// promptly with backoffs == 0 provably skipped the backoff.
	h.sup.rehomeBackoff = func() time.Duration {
		h.backoffs.Add(1)
		return time.Hour
	}
	h.sup.dial = func(dctx context.Context, _, _, _ string, _ http.Handler, onConnected func(string)) error {
		n := h.starts.Add(1)
		now := h.live.Add(1)
		for {
			cur := h.maxLive.Load()
			if now <= cur || h.maxLive.CompareAndSwap(cur, now) {
				break
			}
		}
		onConnected(landOn(n))
		<-dctx.Done()
		h.live.Add(-1)
		return nil
	}
	h.supRef.Store(h.sup)
	go h.sup.run()
	eventuallyTrue(t, 2*time.Second, func() bool { return h.sup.connected() })
	return h
}

func TestRelaySupervisorRehomesWhenHealthNamesAnotherInstance(t *testing.T) {
	var healthInstance atomic.Value
	healthInstance.Store("A")
	h := newRehomeHarness(t,
		func(n int32) string {
			if n == 1 {
				return "A"
			}
			return "B"
		},
		func(string) map[string]any { return map[string]any{"ok": true, "instance": healthInstance.Load()} },
	)

	// Same instance: checks happen, nothing moves.
	eventuallyTrue(t, 2*time.Second, func() bool { return h.healthHit.Load() >= 5 })
	if got := h.starts.Load(); got != 1 {
		t.Fatalf("dials = %d with health on the tunnel's instance, want 1", got)
	}

	// A deploy: new traffic lands on B.
	healthInstance.Store("B")
	eventuallyTrue(t, 2*time.Second, func() bool {
		return h.starts.Load() == 2 && h.sup.connected() && h.sup.rehome.current() == "B"
	})
	// Settled on B: no further moves.
	hits := h.healthHit.Load()
	eventuallyTrue(t, 2*time.Second, func() bool { return h.healthHit.Load() >= hits+5 })
	if got := h.starts.Load(); got != 2 {
		t.Fatalf("dials = %d after settling on B, want 2", got)
	}
	if m := h.maxLive.Load(); m > 1 {
		t.Fatalf("max concurrent tunnels = %d, want <= 1", m)
	}
	if n := h.backoffs.Load(); n != 0 {
		t.Fatalf("a clean move waited the backoff %d time(s), want 0", n)
	}
}

func TestRelaySupervisorIgnoresHealthWithoutInstance(t *testing.T) {
	h := newRehomeHarness(t,
		func(int32) string { return "A" },
		func(string) map[string]any { return map[string]any{"ok": true} },
	)
	eventuallyTrue(t, 2*time.Second, func() bool { return h.healthHit.Load() >= 5 })
	if got := h.starts.Load(); got != 1 {
		t.Fatalf("dials = %d against a cloud without the instance field, want 1", got)
	}
}

func TestRelaySupervisorSkipsHealthWhenHandshakeNamesNoInstance(t *testing.T) {
	h := newRehomeHarness(t,
		func(int32) string { return "" },
		func(string) map[string]any { return map[string]any{"ok": true, "instance": "B"} },
	)
	time.Sleep(150 * time.Millisecond)
	if got := h.healthHit.Load(); got != 0 {
		t.Fatalf("health checked %d times for a cloud that names no instance, want 0", got)
	}
	if got := h.starts.Load(); got != 1 {
		t.Fatalf("dials = %d, want 1", got)
	}
}

func TestRelaySupervisorLandedBackUsesBackoff(t *testing.T) {
	h := newRehomeHarness(t,
		func(int32) string { return "A" }, // every redial lands back on the old instance
		func(string) map[string]any { return map[string]any{"instance": "B"} },
	)
	// First move is immediate.
	eventuallyTrue(t, 2*time.Second, func() bool { return h.starts.Load() == 2 && h.sup.connected() })
	if n := h.backoffs.Load(); n != 0 {
		t.Fatalf("the first move waited the backoff %d time(s), want 0", n)
	}
	// The second move must wait the (hour-long) backoff rather than redial.
	eventuallyTrue(t, 2*time.Second, func() bool { return h.backoffs.Load() == 1 })
	time.Sleep(100 * time.Millisecond)
	if got := h.starts.Load(); got != 2 {
		t.Fatalf("dials = %d after landing back on the old instance, want 2 (backoff)", got)
	}
	h.cancel()
	eventuallyTrue(t, 2*time.Second, func() bool { return h.live.Load() == 0 })
}

func TestRelaySupervisorStopsRehomingWhenInstancesAlternate(t *testing.T) {
	logs := captureLog(t)
	h := newRehomeHarness(t,
		func(n int32) string {
			if n%2 == 1 {
				return "A"
			}
			return "B"
		},
		// Two instances both serving: health always names the other one.
		func(tunnel string) map[string]any {
			if tunnel == "A" {
				return map[string]any{"instance": "B"}
			}
			return map[string]any{"instance": "A"}
		},
	)
	eventuallyTrue(t, 2*time.Second, func() bool { return strings.Contains(logs.String(), "instances alternating") })
	hits := h.healthHit.Load()
	eventuallyTrue(t, 2*time.Second, func() bool { return h.healthHit.Load() >= hits+5 })
	if got := h.starts.Load(); got != 2 {
		t.Fatalf("dials = %d with alternating instances, want 2 (one move, then stop)", got)
	}
	if n := strings.Count(logs.String(), "not moving the tunnel"); n != 1 {
		t.Fatalf("suspension logged %d times, want 1", n)
	}
}

// The production dialer reports the instance the cloud named on the handshake.
func TestServeRelayClassifiedReportsHandshakeInstance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, http.Header{relaymux.InstanceHeader: {"inst-42"}})
		if err != nil {
			return
		}
		_ = ws.Close()
	}))
	defer srv.Close()

	got := make(chan string, 1)
	_ = serveRelayClassified(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http")+"/relay", "tok", "wsl", http.NewServeMux(), func(instance string) {
		got <- instance
	})
	select {
	case inst := <-got:
		if inst != "inst-42" {
			t.Fatalf("instance = %q, want inst-42", inst)
		}
	default:
		t.Fatal("onConnected was not called")
	}
}
