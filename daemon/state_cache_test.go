package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newCacheTestPane builds a minimal running terminal pane with a ring buffer,
// suitable for exercising computeStateCached. Mirrors the construction used by
// the computeState tests in state_test.go.
func newCacheTestPane(id string) *Pane {
	return &Pane{
		ID:     id,
		Cols:   84,
		Rows:   31,
		Status: PaneRunning,
		buffer: NewRingBuffer(1 << 20),
		subs:   map[int64]*subEntry{},
	}
}

// writeBuf appends bytes to the pane's ring buffer (advancing TotalWritten,
// the cache's primary invalidation signal) the same way readLoop would.
func writeBuf(p *Pane, s string) {
	_, _ = p.buffer.Write([]byte(s))
}

// The cache refreshes c.computedAtMs ONLY on a recompute (miss). So a stable
// computedAtMs across two calls is a direct, deterministic observable that no
// recompute ran — i.e. the call was a cache hit and computeState ran exactly
// once for the two calls.

// (a) Two back-to-back cached calls with no output change and within the TTL
// return the IDENTICAL result, and computeState runs only ONCE (the entry's
// compute timestamp does not advance on the second call).
func TestComputeStateCachedHitNoRecompute(t *testing.T) {
	t.Parallel()
	p := newCacheTestPane("pane-hit")
	// Idle-ish content; the exact classification doesn't matter, only that
	// the two calls agree and the second is a hit.
	writeBuf(p, "just some ordinary shell output with no prompt\n")
	atomic.StoreInt64(&p.LastActivityMs, time.Now().UnixMilli())

	first := p.computeStateCached()
	if !p.stateCacheData.valid {
		t.Fatal("cache entry should be valid after first call")
	}

	// Back-date the stored compute timestamp by a sentinel amount that is
	// still within the TTL. A recompute (miss) would overwrite it with a
	// fresh time.Now(); a hit leaves it exactly back-dated. This makes the
	// "ran only once" assertion immune to same-millisecond clock resolution.
	p.stateCacheData.mu.Lock()
	const sentinelOffset = 200 // ms; < stateCacheTTL (1000ms) so still a hit
	p.stateCacheData.computedAtMs -= sentinelOffset
	sentinel := p.stateCacheData.computedAtMs
	p.stateCacheData.mu.Unlock()

	second := p.computeStateCached()
	secondComputedAt := p.stateCacheData.computedAtMs

	if first != second {
		t.Fatalf("cached result changed across back-to-back calls: %q vs %q", first, second)
	}
	if secondComputedAt != sentinel {
		t.Fatalf("second call recomputed (computedAtMs moved off sentinel %d -> %d); expected a cache hit",
			sentinel, secondComputedAt)
	}
}

// Cross-check: a cache HIT returns the SAME value the direct uncached compute
// would, so the optimization is behavior-identical on the hit path.
func TestComputeStateCachedMatchesDirect(t *testing.T) {
	t.Parallel()
	// A real choice-prompt menu so the classification is non-trivial.
	menu := "Bash command\n" +
		"git push\n" +
		"Do you want to proceed?\n" +
		"❯ 1. Yes\n" +
		"  2. No\n" +
		"Esc to cancel\n"

	direct := newCacheTestPane("pane-direct")
	writeBuf(direct, menu)
	atomic.StoreInt64(&direct.LastActivityMs, time.Now().UnixMilli()-2000) // age past busy gate
	want := direct.computeState()

	cached := newCacheTestPane("pane-cached")
	writeBuf(cached, menu)
	atomic.StoreInt64(&cached.LastActivityMs, time.Now().UnixMilli()-2000)
	got := cached.computeStateCached()

	if got != want {
		t.Fatalf("cached compute = %q, direct compute = %q; must be identical", got, want)
	}
}

// (b) After a simulated output advance (TotalWritten increases), the cache
// recomputes even within the TTL.
func TestComputeStateCachedRecomputesOnOutputAdvance(t *testing.T) {
	t.Parallel()
	p := newCacheTestPane("pane-advance")
	writeBuf(p, "initial output\n")
	atomic.StoreInt64(&p.LastActivityMs, time.Now().UnixMilli())

	_ = p.computeStateCached()
	firstStoredWritten := p.stateCacheData.totalWritten

	// Simulate new PTY output. This advances RingBuffer.TotalWritten — the
	// primary invalidation signal — so the next call must recompute (a miss)
	// even though we are still well within the TTL.
	writeBuf(p, "more output arrived\n")
	atomic.StoreInt64(&p.LastActivityMs, time.Now().UnixMilli())
	liveWritten := p.buffer.TotalWritten()
	if liveWritten <= firstStoredWritten {
		t.Fatalf("test setup: TotalWritten did not advance (%d -> %d)", firstStoredWritten, liveWritten)
	}

	_ = p.computeStateCached()
	secondStoredWritten := p.stateCacheData.totalWritten

	// On a recompute (miss) the cache refreshes its stored totalWritten to the
	// new live value. On a (wrong) hit it would still hold the stale value.
	// This is a clock-resolution-independent observable of "recompute ran".
	if secondStoredWritten != liveWritten {
		t.Fatalf("output advanced but cache did not recompute: stored totalWritten stayed %d, want %d",
			secondStoredWritten, liveWritten)
	}
}

// (c) After the TTL elapses with NO output change, the cache recomputes — so
// computeState's time-based transitions (busy -> recent -> idle decay) still
// surface. We don't sleep a real second; instead we age the cache entry's
// recorded compute timestamp past the TTL, which is exactly what wall-clock
// elapsing does.
func TestComputeStateCachedRecomputesAfterTTL(t *testing.T) {
	t.Parallel()
	p := newCacheTestPane("pane-ttl")
	writeBuf(p, "some output\n")
	atomic.StoreInt64(&p.LastActivityMs, time.Now().UnixMilli())

	_ = p.computeStateCached()

	// Backdate the recorded compute time so more than the TTL appears to have
	// elapsed, with output (TotalWritten) unchanged.
	p.stateCacheData.mu.Lock()
	staleWritten := p.stateCacheData.totalWritten
	p.stateCacheData.computedAtMs -= int64(stateCacheTTL/time.Millisecond) + 50
	p.stateCacheData.computedAt = p.stateCacheData.computedAt.Add(-stateCacheTTL - 50*time.Millisecond)
	staleComputedAt := p.stateCacheData.computedAtMs
	p.stateCacheData.mu.Unlock()

	_ = p.computeStateCached()

	p.stateCacheData.mu.Lock()
	defer p.stateCacheData.mu.Unlock()
	if p.stateCacheData.totalWritten != staleWritten {
		t.Fatalf("test setup: output should be unchanged across TTL test")
	}
	if p.stateCacheData.computedAtMs == staleComputedAt {
		t.Fatal("TTL elapsed with no output change but cache did not recompute")
	}
}

// (c-bis) End-to-end TTL behavior with a real (short) sleep: a busy pane
// observed via the cache must decay to recent/idle as wall-clock advances,
// proving time-based transitions are not frozen by the cache. Uses the
// daemon's normal time.Now() clock.
func TestComputeStateCachedSurfacesTimeTransition(t *testing.T) {
	p := newCacheTestPane("pane-decay")
	writeBuf(p, "fresh burst of output\n")
	// LastActivityMs just now → age < 1500 → StateBusy.
	atomic.StoreInt64(&p.LastActivityMs, time.Now().UnixMilli())

	if got := p.computeStateCached(); got != StateBusy {
		t.Fatalf("freshly-active pane should be busy, got %q", got)
	}

	// Move activity back past the busy and recent thresholds (3000ms) with no
	// new output, then wait out the TTL so the cache must recompute.
	atomic.StoreInt64(&p.LastActivityMs, time.Now().UnixMilli()-4000)
	waitStarted := time.Now()
	time.Sleep(stateCacheTTL + 100*time.Millisecond)

	if got := p.computeStateCached(); got != StateIdle {
		now := time.Now()
		t.Fatalf("after activity aged past thresholds + TTL, pane should be idle, got %q; monotonic wait=%s wall wait=%dms cache wall age=%dms activity age=%dms direct=%q",
			got, now.Sub(waitStarted), now.UnixMilli()-waitStarted.UnixMilli(),
			now.UnixMilli()-p.stateCacheData.computedAtMs,
			now.UnixMilli()-atomic.LoadInt64(&p.LastActivityMs), p.computeState())
	}
}

// A wall-clock correction must not extend the TTL of an in-memory cached
// result. Model a backward correction by putting the recorded wall timestamp
// ahead of now, then let the unchanged real TTL expire.
func TestComputeStateCachedWallRollbackDoesNotFreezeBusy(t *testing.T) {
	p := newCacheTestPane("pane-clock-rollback")
	writeBuf(p, "fresh output\n")
	atomic.StoreInt64(&p.LastActivityMs, time.Now().UnixMilli())
	if got := p.computeStateCached(); got != StateBusy {
		t.Fatalf("fresh pane should be busy, got %q", got)
	}
	p.stateCacheData.mu.Lock()
	p.stateCacheData.computedAtMs += int64(time.Hour / time.Millisecond)
	p.stateCacheData.mu.Unlock()
	atomic.StoreInt64(&p.LastActivityMs, time.Now().UnixMilli()-60_000)
	time.Sleep(stateCacheTTL + 100*time.Millisecond)
	if got := p.computeStateCached(); got != StateIdle {
		t.Fatalf("wall timestamp in the future froze cached state past TTL: got %q, direct %q", got, p.computeState())
	}
}

// (d) Concurrent callers (the real watcher pattern: several goroutines call
// computeStateCached for the same pane simultaneously) must be race-free.
// Run under `go test -race`.
func TestComputeStateCachedConcurrent(t *testing.T) {
	p := newCacheTestPane("pane-race")
	writeBuf(p, "baseline output\n")
	atomic.StoreInt64(&p.LastActivityMs, time.Now().UnixMilli())

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Three reader goroutines mimic pusher/activity/long-running all polling.
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = p.computeStateCached()
				}
			}
		}()
	}

	// One writer goroutine simulates readLoop appending PTY output, which
	// drives cache invalidation concurrently with the readers.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				writeBuf(p, "x")
				atomic.StoreInt64(&p.LastActivityMs, time.Now().UnixMilli())
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}
