package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

// Relay re-homing after a cloud deploy.
//
// On a Render deploy the new cloud instance starts receiving new traffic while
// the old instance, which still holds this daemon's relay tunnel, keeps running
// for minutes before it is sent SIGTERM and drains its tunnels. In that window
// every browser request through the cloud lands on the new instance and gets
// 503 "machine offline" (seen 2026-10-25: ~2.5 min of outage after a deploy).
//
// The cloud names its instance on the relay handshake (relaymux.InstanceHeader)
// and in GET /api/health (`instance`). While a tunnel is up the supervisor asks
// /api/health every ~15s where new traffic now lands; if it names a different
// instance on two consecutive checks the tunnel is closed and redialled at once.
// Clouds that send no instance get no health requests and no action.

const (
	// relayHealthInterval is the base period between /api/health checks while
	// a tunnel is up. Each wait is jittered by ±25% so a fleet doesn't poll in
	// lockstep.
	relayHealthInterval = 15 * time.Second
	// relayHealthTimeout bounds one /api/health request.
	relayHealthTimeout = 5 * time.Second
	// relayRehomeConfirmations is how many consecutive checks must name the
	// same other instance before the tunnel moves (one stray answer during a
	// routing switch is not enough).
	relayRehomeConfirmations = 2
	// relayRehomeBudget is the most moves allowed within relayRehomeWindow.
	// A single-instance cloud needs one per deploy; hitting the budget means
	// something else is going on (several instances, or redials keep landing
	// back on the old one), so moving stops for a window.
	relayRehomeBudget  = 4
	relayRehomeWindow  = 30 * time.Minute
	relayHealthMaxBody = 64 << 10
)

type relayRehomeEvent struct {
	from, to string
	at       time.Time
}

// relayRehome decides when the live tunnel should move to another cloud
// instance. It is fed by markOnline (tunnelUp), the tunnel-exit paths
// (tunnelDown) and the health watcher (observe). Safe for concurrent use.
type relayRehome struct {
	mu sync.Mutex

	// tunnel is the instance the live tunnel is attached to ("" when no tunnel
	// is up or the cloud did not name one).
	tunnel string

	// mismatches counts consecutive health answers naming target, an instance
	// other than tunnel.
	mismatches int
	target     string

	// pendingFrom is the instance a move left; the next tunnelUp compares its
	// instance with it to learn whether the redial landed back on the old one.
	pendingFrom string
	// landedBack counts consecutive moves whose redial came back to the
	// instance it was leaving. Non-zero makes the next move back off normally.
	landedBack int

	history []relayRehomeEvent
	// suspendedUntil pauses every move (budget exhausted).
	suspendedUntil time.Time
	// blocked pauses moves TO instances seen alternating (two instances both
	// taking traffic), until the given time. A move to any other instance,
	// such as a later deploy's, is still allowed.
	blocked map[string]time.Time
}

// tunnelUp records the instance a new tunnel landed on.
func (r *relayRehome) tunnelUp(instance string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tunnel = instance
	r.mismatches, r.target = 0, ""
	if r.pendingFrom != "" {
		if instance != "" && instance == r.pendingFrom {
			r.landedBack++
		} else {
			r.landedBack = 0
		}
		r.pendingFrom = ""
	}
}

// tunnelDown forgets the tunnel's instance. A pending move stays pending so
// the redial's tunnelUp can judge where it landed.
func (r *relayRehome) tunnelDown() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tunnel = ""
	r.mismatches, r.target = 0, ""
}

// current returns the live tunnel's instance ("" = nothing to compare).
func (r *relayRehome) current() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tunnel
}

// shouldBackOff reports whether the move about to happen follows a move whose
// redial landed back on the old instance; such a move uses the normal redial
// backoff rather than an immediate redial.
func (r *relayRehome) shouldBackOff() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.landedBack > 0
}

// abandon undoes the bookkeeping of a move that observe approved but the
// supervisor did not carry out (the tunnel had already changed).
func (r *relayRehome) abandon() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pendingFrom = ""
	if n := len(r.history); n > 0 {
		r.history = r.history[:n-1]
	}
}

// observe takes one /api/health answer, checked while the tunnel was on from,
// and reports whether the tunnel should move now.
func (r *relayRehome) observe(from, health string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if from == "" || from != r.tunnel {
		return false // the tunnel changed while the check was in flight
	}
	if health == "" || health == from {
		// Old cloud without the field, or traffic still lands on our instance.
		r.mismatches, r.target = 0, ""
		return false
	}
	if health != r.target {
		r.target, r.mismatches = health, 1
	} else {
		r.mismatches++
	}
	if r.mismatches < relayRehomeConfirmations {
		return false
	}
	r.mismatches, r.target = 0, ""
	if now.Before(r.suspendedUntil) || now.Before(r.blocked[health]) {
		return false
	}

	kept := r.history[:0]
	for _, e := range r.history {
		if now.Sub(e.at) < relayRehomeWindow {
			kept = append(kept, e)
		}
	}
	r.history = kept

	// Moving back to an instance we recently moved away from means two
	// instances are both taking traffic: following health would ping-pong.
	pingPong := false
	for _, e := range r.history {
		if e.from == health {
			pingPong = true
			break
		}
	}
	if pingPong {
		// Pause moves between these two only; a later deploy's new instance
		// can still be followed.
		if r.blocked == nil {
			r.blocked = map[string]time.Time{}
		}
		for k, until := range r.blocked {
			if !now.Before(until) {
				delete(r.blocked, k)
			}
		}
		until := now.Add(relayRehomeWindow)
		r.blocked[health], r.blocked[from] = until, until
		log.Printf("relay: cloud reports instance %s but the tunnel is on %s; not moving the tunnel between them for %s (instances alternating)",
			health, from, relayRehomeWindow)
		return false
	}
	if len(r.history) >= relayRehomeBudget {
		r.suspendedUntil = now.Add(relayRehomeWindow)
		log.Printf("relay: cloud reports instance %s but the tunnel is on %s; not moving the tunnel for %s (too many moves: %d in the last %s)",
			health, from, relayRehomeWindow, len(r.history), relayRehomeWindow)
		return false
	}

	r.history = append(r.history, relayRehomeEvent{from: from, to: health, at: now})
	r.pendingFrom = from
	return true
}

// newRelayHealthClient returns the client for /api/health checks. It uses the
// default proxy settings (HTTPS_PROXY etc.) like the daemon's other cloud
// calls, but opens a fresh connection per check: a kept-alive connection could
// stay pinned to the old instance and hide the very switch being looked for.
func newRelayHealthClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DisableKeepAlives = true
	return &http.Client{Transport: tr, Timeout: relayHealthTimeout}
}

// fetchCloudInstance asks /api/health which instance new traffic lands on. ok
// is false when the check failed; an ok answer with "" is an older cloud.
func (s *relaySupervisor) fetchCloudInstance() (instance string, ok bool) {
	ctx, cancel := context.WithTimeout(s.ctx, relayHealthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.healthURL, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Cache-Control", "no-cache")
	client := s.healthClient
	if client == nil {
		client = newRelayHealthClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var body struct {
		Instance string `json:"instance"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, relayHealthMaxBody)).Decode(&body); err != nil {
		return "", false
	}
	return body.Instance, true
}

// watchCloudInstance runs for the supervisor's lifetime. While a tunnel that
// named its instance is up, it checks /api/health and asks the supervisor loop
// to move the tunnel when observe says so.
func (s *relaySupervisor) watchCloudInstance() {
	if s.healthURL == "" {
		return
	}
	interval := s.healthInterval
	if interval <= 0 {
		interval = relayHealthInterval
	}
	timer := time.NewTimer(jitterRelayHealth(interval))
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
		}
		timer.Reset(jitterRelayHealth(interval))

		from := s.rehome.current()
		if from == "" || !s.connected() {
			continue // no tunnel, or a cloud that names no instance: nothing to do
		}
		instance, ok := s.fetchCloudInstance()
		if !ok {
			continue
		}
		if s.rehome.observe(from, instance, time.Now()) {
			select {
			case s.rehomeCh <- from:
			default:
				s.rehome.abandon()
			}
		}
	}
}

// jitterRelayHealth spreads d by ±25%.
func jitterRelayHealth(d time.Duration) time.Duration {
	return d*3/4 + time.Duration(rand.Int64N(int64(d)/2+1))
}
