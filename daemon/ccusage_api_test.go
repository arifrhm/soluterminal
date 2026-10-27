package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// --- cache type: fresh / stale / expiry semantics ----------------------------

func TestCCUsageCacheFreshThenStale(t *testing.T) {
	base := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	now := base
	c := newCCUsageCache()
	c.now = func() time.Time { return now }

	c.put("k", 200, []byte(`{"ok":true}`))

	// At store time: fresh AND stale-eligible.
	if _, ok := c.getFresh("k"); !ok {
		t.Fatalf("expected fresh hit immediately after put")
	}
	if _, ok := c.getStale("k"); !ok {
		t.Fatalf("expected stale hit immediately after put")
	}

	// Just inside the fresh TTL → still fresh.
	now = base.Add(ccUsageFreshTTL - time.Second)
	if _, ok := c.getFresh("k"); !ok {
		t.Fatalf("expected fresh hit just inside fresh TTL")
	}

	// Past the fresh TTL → no longer fresh, but still stale-eligible.
	now = base.Add(ccUsageFreshTTL + time.Second)
	if _, ok := c.getFresh("k"); ok {
		t.Fatalf("expected fresh MISS past fresh TTL")
	}
	if _, ok := c.getStale("k"); !ok {
		t.Fatalf("expected stale hit just past fresh TTL")
	}

	// Past the stale TTL → gone entirely.
	now = base.Add(ccUsageStaleTTL + time.Second)
	if _, ok := c.getStale("k"); ok {
		t.Fatalf("expected stale MISS past stale TTL")
	}
}

func TestCCUsageCacheMiss(t *testing.T) {
	c := newCCUsageCache()
	if _, ok := c.getFresh("nope"); ok {
		t.Fatalf("expected fresh miss for unknown key")
	}
	if _, ok := c.getStale("nope"); ok {
		t.Fatalf("expected stale miss for unknown key")
	}
}

func TestCCUsageCacheKeyedIndependently(t *testing.T) {
	c := newCCUsageCache()
	c.put("a", 200, []byte("A"))
	c.put("b", 200, []byte("B"))
	ea, _ := c.getFresh("a")
	eb, _ := c.getFresh("b")
	if string(ea.body) != "A" || string(eb.body) != "B" {
		t.Fatalf("keys collided: a=%q b=%q", ea.body, eb.body)
	}
}

func TestCCUsageEmptySummaryIncludesRoleShape(t *testing.T) {
	summary := ccUsageEmptySummary("30d")
	roles, ok := summary["by_role"].([]any)
	if !ok || roles == nil || len(roles) != 0 {
		t.Fatalf("by_role = %#v (%T), want non-nil empty array", summary["by_role"], summary["by_role"])
	}
}

// --- proxyCCUsageRead: end-to-end degradation contract -----------------------

// newCCUsageProxyServer wires an apiServer whose ccCloudBase points at a test
// HTTP server with the given handler. Each test gets a fresh cache so prior
// entries can't leak across cases.
func newCCUsageProxyServer(t *testing.T, h http.HandlerFunc) (*apiServer, *httptest.Server, func()) {
	t.Helper()
	srv := httptest.NewServer(h)
	a := &apiServer{ccCloudBase: srv.URL, ccCloudToken: "tok"}
	// Isolate this test's cache from the package-global one.
	prev := ccUsageReadCache
	ccUsageReadCache = newCCUsageCache()
	return a, srv, func() {
		srv.Close()
		ccUsageReadCache = prev
	}
}

func doCCUsageRead(a *apiServer, endpoint, rawQuery string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/cc-usage/summary?"+rawQuery, nil)
	r.URL.RawQuery = rawQuery
	a.proxyCCUsageRead(rr, r, endpoint)
	return rr
}

func TestProxyCCUsage2xxCachesAndPassesThrough(t *testing.T) {
	a, _, cleanup := newCCUsageProxyServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"hero":{"turns":3},"by_model":[{"cache_create_5m_tokens":11,"cache_create_1h_tokens":2}],"by_role":[{"role":"reviewer","long_gap_observed":1}]}`))
	})
	defer cleanup()

	rr := doCCUsageRead(a, "/api/cc-usage/summary", "window=30d")
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Body.String(); got != `{"hero":{"turns":3},"by_model":[{"cache_create_5m_tokens":11,"cache_create_1h_tokens":2}],"by_role":[{"role":"reviewer","long_gap_observed":1}]}` {
		t.Fatalf("body = %q", got)
	}
	if rr.Header().Get("X-Solu-Usage-Stale") != "" {
		t.Fatalf("did not expect stale header on a fresh 2xx")
	}
	// It must be cached now.
	key := ccUsageCacheKey("tok", "/api/cc-usage/summary", "window=30d")
	if _, ok := ccUsageReadCache.getFresh(key); !ok {
		t.Fatalf("expected 2xx body to be cached under %q", key)
	}
}

func TestProxyCCUsageFreshHitSkipsCloud(t *testing.T) {
	var calls int32
	a, _, cleanup := newCCUsageProxyServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"v":1}`))
	})
	defer cleanup()

	_ = doCCUsageRead(a, "/api/cc-usage/summary", "window=7d")
	rr := doCCUsageRead(a, "/api/cc-usage/summary", "window=7d")
	if rr.Code != 200 || rr.Body.String() != `{"v":1}` {
		t.Fatalf("second read code=%d body=%q", rr.Code, rr.Body.String())
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("cloud hit %d times, want 1 (fresh cache should skip the second)", got)
	}
}

func TestProxyCCUsage5xxServesStale(t *testing.T) {
	var fail int32
	a, _, cleanup := newCCUsageProxyServer(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&fail) == 1 {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":"timeout"}`))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"good":1}`))
	})
	defer cleanup()

	// Prime the cache, then age it out of fresh so the next call hits the cloud.
	_ = doCCUsageRead(a, "/api/cc-usage/summary", "window=30d")
	key := ccUsageCacheKey("tok", "/api/cc-usage/summary", "window=30d")
	ccUsageReadCache.mu.Lock()
	e := ccUsageReadCache.m[key]
	e.storedAt = time.Now().Add(-(ccUsageFreshTTL + time.Minute))
	ccUsageReadCache.m[key] = e
	ccUsageReadCache.mu.Unlock()

	atomic.StoreInt32(&fail, 1)
	rr := doCCUsageRead(a, "/api/cc-usage/summary", "window=30d")
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200 (stale served)", rr.Code)
	}
	if rr.Header().Get("X-Solu-Usage-Stale") != "1" {
		t.Fatalf("expected X-Solu-Usage-Stale: 1, got %q", rr.Header().Get("X-Solu-Usage-Stale"))
	}
	if rr.Body.String() != `{"good":1}` {
		t.Fatalf("stale body = %q, want last good", rr.Body.String())
	}
}

func TestProxyCCUsage5xxNoCacheReturns503(t *testing.T) {
	a, _, cleanup := newCCUsageProxyServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"error":"timeout"}`))
	})
	defer cleanup()

	rr := doCCUsageRead(a, "/api/cc-usage/summary", "window=30d")
	if rr.Code != 503 {
		t.Fatalf("status = %d, want 503 (no cache → transient)", rr.Code)
	}
	if rr.Header().Get("X-Solu-Usage-Stale") != "" {
		t.Fatalf("did not expect stale header with no cache")
	}
}

func TestProxyCCUsageTransportErrorNoCacheReturns503(t *testing.T) {
	a, srv, cleanup := newCCUsageProxyServer(t, func(w http.ResponseWriter, r *http.Request) {})
	defer cleanup()
	srv.Close() // force a connection-refused transport error

	rr := doCCUsageRead(a, "/api/cc-usage/summary", "window=30d")
	if rr.Code != 503 {
		t.Fatalf("status = %d, want 503 on transport error with no cache", rr.Code)
	}
}

func TestProxyCCUsage4xxPassesThroughNotSubstituted(t *testing.T) {
	a, _, cleanup := newCCUsageProxyServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":"not authorized"}`))
	})
	defer cleanup()

	rr := doCCUsageRead(a, "/api/cc-usage/member-summary", "user_id=other")
	if rr.Code != 403 {
		t.Fatalf("status = %d, want 403 (privacy boundary passed through)", rr.Code)
	}
	if rr.Header().Get("X-Solu-Usage-Stale") != "" {
		t.Fatalf("4xx must not be marked stale")
	}
	if rr.Body.String() != `{"error":"not authorized"}` {
		t.Fatalf("4xx body = %q, want verbatim", rr.Body.String())
	}
	// 4xx must NOT be cached.
	if _, ok := ccUsageReadCache.getStale(ccUsageCacheKey("tok", "/api/cc-usage/member-summary", "user_id=other")); ok {
		t.Fatalf("4xx response must not be cached")
	}
}

func TestProxyCCUsage4xxDoesNotServeStaleOverNewAnswer(t *testing.T) {
	// A good 2xx is cached; later the cloud answers 403 (e.g. privacy changed).
	// The 403 must pass through, NOT be masked by the stale 200.
	state := int32(0)
	a, _, cleanup := newCCUsageProxyServer(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&state) == 0 {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"hero":{"turns":1}}`))
			return
		}
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	})
	defer cleanup()

	_ = doCCUsageRead(a, "/api/cc-usage/member-summary", "user_id=x")
	// Age the cache out of fresh so the next call revalidates.
	key := ccUsageCacheKey("tok", "/api/cc-usage/member-summary", "user_id=x")
	ccUsageReadCache.mu.Lock()
	e := ccUsageReadCache.m[key]
	e.storedAt = time.Now().Add(-(ccUsageFreshTTL + time.Minute))
	ccUsageReadCache.m[key] = e
	ccUsageReadCache.mu.Unlock()

	atomic.StoreInt32(&state, 1)
	rr := doCCUsageRead(a, "/api/cc-usage/member-summary", "user_id=x")
	if rr.Code != 403 {
		t.Fatalf("status = %d, want 403 (4xx must not be masked by stale cache)", rr.Code)
	}
}

// Sanity: cache key includes the query so distinct windows don't collide.
func TestProxyCCUsageCacheKeyIncludesQuery(t *testing.T) {
	a, _, cleanup := newCCUsageProxyServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("window")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"window":"` + q + `"}`))
	})
	defer cleanup()

	rr7 := doCCUsageRead(a, "/api/cc-usage/summary", url.Values{"window": {"7d"}}.Encode())
	rr30 := doCCUsageRead(a, "/api/cc-usage/summary", url.Values{"window": {"30d"}}.Encode())
	if rr7.Body.String() == rr30.Body.String() {
		t.Fatalf("distinct windows shared a cache entry: %q", rr7.Body.String())
	}
}

func TestProxyCCUsageCacheScopedToTokenAndBypassable(t *testing.T) {
	var calls int32
	a, _, cleanup := newCCUsageProxyServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"n":` + strconv.Itoa(int(n)) + `}`))
	})
	defer cleanup()

	if got := doCCUsageRead(a, "/api/cc-usage/summary", "window=7d").Body.String(); got != `{"n":1}` {
		t.Fatalf("first read = %s", got)
	}
	// A re-pair to another account must not be served the previous body.
	a.ccCloudToken = "other"
	if got := doCCUsageRead(a, "/api/cc-usage/summary", "window=7d").Body.String(); got != `{"n":2}` {
		t.Fatalf("re-paired read = %s, want a fresh cloud read", got)
	}
	// Refresh (Cache-Control: no-cache) skips the fresh window.
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/cc-usage/summary?window=7d", nil)
	r.Header.Set("Cache-Control", "no-cache")
	a.proxyCCUsageRead(rr, r, "/api/cc-usage/summary")
	if got := rr.Body.String(); got != `{"n":3}` {
		t.Fatalf("refresh read = %s, want a fresh cloud read", got)
	}
	if got := doCCUsageRead(a, "/api/cc-usage/summary", "window=7d").Body.String(); got != `{"n":3}` {
		t.Fatalf("plain read after refresh = %s, want the refreshed cache", got)
	}
}

func TestCCUsageCacheEvictsAtCapacity(t *testing.T) {
	c := newCCUsageCache()
	base := time.Now()
	for i := 0; i < ccUsageCacheMaxEntries+10; i++ {
		at := base.Add(time.Duration(i) * time.Millisecond)
		c.now = func() time.Time { return at }
		c.put("k"+strconv.Itoa(i), 200, []byte("x"))
	}
	c.mu.Lock()
	n := len(c.m)
	_, oldest := c.m["k0"]
	c.mu.Unlock()
	if n != ccUsageCacheMaxEntries || oldest {
		t.Fatalf("entries = %d (oldest kept=%v), want cap %d with oldest evicted", n, oldest, ccUsageCacheMaxEntries)
	}
}
