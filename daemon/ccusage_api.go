package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ccUsageHTTPTransport is the cc-usage proxy's OWN connection pool, kept
// separate from sharedHTTPTransport / ariaClient so a burst of 11-minute ARIA
// tool turns can't starve the read-side usage proxy of connections (and vice
// versa). Small pool — usage reads are low-volume (a dashboard refresh).
var ccUsageHTTPTransport = &http.Transport{
	MaxIdleConns:        4,
	MaxIdleConnsPerHost: 2,
	IdleConnTimeout:     90 * time.Second,
	ForceAttemptHTTP2:   true,
}

// ccUsageClient is the cc-usage proxy's dedicated HTTP client. Its 25s ceiling
// is comfortably longer than the cloud's ~8s query timeout, leaving room for
// relay + network round-trips, but far below ariaClient's 11-minute turn
// ceiling so a hung cloud read can't pin a connection for minutes.
var ccUsageClient = &http.Client{
	Timeout:   25 * time.Second,
	Transport: ccUsageHTTPTransport,
}

const (
	// ccUsageCloudTimeout bounds a single daemon→cloud read. Longer than the
	// cloud's ~8s query timeout (room for relay + network), still derived from
	// r.Context() so a client abort cancels it cleanly.
	ccUsageCloudTimeout = 25 * time.Second
	// ccUsageFreshTTL: within this window a cache hit is served WITHOUT hitting
	// the cloud (cuts round-trips on dashboard re-polls).
	ccUsageFreshTTL = 60 * time.Second
	// ccUsageStaleTTL: how long a cached body may be served-stale on a cloud
	// error/5xx before we give up and return 503.
	ccUsageStaleTTL = 1 * time.Hour
	// ccUsageCacheMaxEntries bounds the cache: each (endpoint, window, team,
	// member, provider) combination is its own key, so an owner browsing many
	// members must not grow it without limit.
	ccUsageCacheMaxEntries = 256
)

// ccUsageCacheEntry is one cached cloud response: the full JSON body plus the
// status it was served with and when it was stored.
type ccUsageCacheEntry struct {
	body     []byte
	status   int
	storedAt time.Time
}

// ccUsageCache is a thread-safe TTL cache keyed by request (endpoint + query).
// It serves fresh hits without a cloud round-trip and serves stale bodies when
// the cloud errors. Factored out as a testable type with an injectable clock.
type ccUsageCache struct {
	mu  sync.Mutex
	m   map[string]ccUsageCacheEntry
	now func() time.Time // nil → time.Now
}

func newCCUsageCache() *ccUsageCache {
	return &ccUsageCache{m: make(map[string]ccUsageCacheEntry)}
}

func (c *ccUsageCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// getFresh returns a cached entry only if it is within ccUsageFreshTTL.
func (c *ccUsageCache) getFresh(key string) (ccUsageCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok {
		return ccUsageCacheEntry{}, false
	}
	if c.clock().Sub(e.storedAt) > ccUsageFreshTTL {
		return ccUsageCacheEntry{}, false
	}
	return e, true
}

// getStale returns a cached entry if one exists within ccUsageStaleTTL,
// regardless of freshness. Used as the degraded fallback on a cloud error.
func (c *ccUsageCache) getStale(key string) (ccUsageCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok {
		return ccUsageCacheEntry{}, false
	}
	if c.clock().Sub(e.storedAt) > ccUsageStaleTTL {
		return ccUsageCacheEntry{}, false
	}
	return e, true
}

// put stores a successful (2xx) cloud response under key, first evicting
// entries past the stale TTL and, if still full, the oldest entry.
func (c *ccUsageCache) put(key string, status int, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock()
	if _, exists := c.m[key]; !exists && len(c.m) >= ccUsageCacheMaxEntries {
		oldestKey, oldest := "", now
		for k, e := range c.m {
			if now.Sub(e.storedAt) > ccUsageStaleTTL {
				delete(c.m, k)
				continue
			}
			if oldestKey == "" || e.storedAt.Before(oldest) {
				oldestKey, oldest = k, e.storedAt
			}
		}
		if len(c.m) >= ccUsageCacheMaxEntries && oldestKey != "" {
			delete(c.m, oldestKey)
		}
	}
	c.m[key] = ccUsageCacheEntry{
		body:     body,
		status:   status,
		storedAt: now,
	}
}

// ccUsageCacheKey scopes a cached body to the machine token it was fetched
// with, so a re-pair to another account never serves the previous account's
// usage (including an owner-only member drill-down) from cache.
func ccUsageCacheKey(token, endpoint, rawQuery string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8]) + " " + endpoint + "?" + rawQuery
}

// ccUsageBypassFresh reports whether the caller asked to skip the fresh-cache
// window (the page's explicit Refresh). The stale fallback still applies.
func ccUsageBypassFresh(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Cache-Control")), "no-cache")
}

// ccUsageReadCache is the daemon-wide cache shared by the three cc-usage read
// handlers. Keyed by path+RawQuery so each (endpoint, window, team_id, user_id)
// combination is cached independently.
var ccUsageReadCache = newCCUsageCache()

// proxyCCUsageRead is the shared body for the three cc-usage read handlers. It
// implements the daemon side of the usage-proxy reliability contract:
//
//   - Local-only (no cloud configured): the caller handles that branch and
//     never reaches here.
//   - Fresh cache hit (≤60s): serve the cached 2xx body, no cloud round-trip.
//   - Otherwise fetch the cloud (own client, 25s ctx timeout):
//   - cloud 2xx: read the FULL body into memory, cache it, pass through 200.
//   - cloud 4xx: pass through verbatim (e.g. 403 privacy boundary) — never
//     retried or cache-substituted.
//   - cloud 5xx OR transport error: if a stale-but-within-1h cache exists,
//     serve it with 200 + "X-Solu-Usage-Stale: 1"; else return 503
//     (transient, retryable) — NOT 502.
//
// Reading the whole body before WriteHeader closes the truncated-200 hole: a
// mid-stream cloud drop surfaces as an error → stale/503, never a partial 200.
func (a *apiServer) proxyCCUsageRead(w http.ResponseWriter, r *http.Request, endpoint string) {
	ccCloudBase, ccCloudToken := a.ccCloudCreds()
	url := ccCloudBase + endpoint
	if q := r.URL.RawQuery; q != "" {
		url += "?" + q
	}
	cacheKey := ccUsageCacheKey(ccCloudToken, endpoint, r.URL.RawQuery)

	// Fresh cache hit: serve without touching the cloud (unless the page's
	// Refresh asked for a fresh read).
	if !ccUsageBypassFresh(r) {
		if e, ok := ccUsageReadCache.getFresh(cacheKey); ok {
			writeCCUsageBody(w, e.status, e.body, false)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), ccUsageCloudTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		http.Error(w, "build request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("Authorization", "Bearer "+ccCloudToken)

	resp, err := ccUsageClient.Do(req)
	if err != nil {
		serveStaleOr503(w, cacheKey)
		return
	}
	defer resp.Body.Close()

	// Read the full body into memory BEFORE writing status, so a mid-stream
	// cloud drop yields an error (→ stale/503) rather than a truncated 200.
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		serveStaleOr503(w, cacheKey)
		return
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		ccUsageReadCache.put(cacheKey, resp.StatusCode, body)
		writeCCUsageBody(w, resp.StatusCode, body, false)
	case resp.StatusCode >= 500:
		// Transient cloud failure (incl. the cloud's own 503-on-query-timeout).
		serveStaleOr503(w, cacheKey)
	default:
		// 4xx: a real, non-transient answer (e.g. 403 privacy boundary). Pass
		// through verbatim — never retried or cache-substituted.
		writeCCUsageBody(w, resp.StatusCode, body, false)
	}
}

// serveStaleOr503 serves a stale cached body with 200 + X-Solu-Usage-Stale: 1
// if one exists within the stale TTL, else returns 503 (transient, retryable).
func serveStaleOr503(w http.ResponseWriter, cacheKey string) {
	if e, ok := ccUsageReadCache.getStale(cacheKey); ok {
		writeCCUsageBody(w, http.StatusOK, e.body, true)
		return
	}
	http.Error(w, "cloud usage temporarily unavailable", http.StatusServiceUnavailable)
}

// writeCCUsageBody writes a buffered JSON body with the given status. When
// stale, it overrides the status to 200 and stamps X-Solu-Usage-Stale: 1 so the
// web layer can show a "showing last-known usage" affordance.
func writeCCUsageBody(w http.ResponseWriter, status int, body []byte, stale bool) {
	w.Header().Set("Content-Type", "application/json")
	if stale {
		w.Header().Set("X-Solu-Usage-Stale", "1")
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// handleCCUsageSummary proxies the cloud's GET /api/cc-usage/summary
// through to the browser. Daemon→cloud auth uses the daemon's machine
// token; the browser never sees it.
//
// When no cloud is configured (local-only dev), it returns the contract's
// EMPTY SHAPE with 200 so the web UI degrades gracefully into its "still
// gathering your usage" empty state instead of erroring.
// See docs/USAGE-PHASE2-CONTRACT.md.
func (a *apiServer) handleCCUsageSummary(w http.ResponseWriter, r *http.Request) {
	if base, tok := a.ccCloudCreds(); base == "" || tok == "" {
		writeJSON(w, 200, ccUsageEmptySummaryFor(r))
		return
	}
	a.proxyCCUsageRead(w, r, "/api/cc-usage/summary")
}

// handleCCUsageTeamSummary proxies the cloud's GET /api/cc-usage/team-summary
// (Phase 3). Same machine-token proxy as handleCCUsageSummary — the cloud does
// the team resolution + per-member privacy redaction; the daemon just forwards.
// Local-only dev → empty team shape (team:null) so the web hides the Team toggle.
func (a *apiServer) handleCCUsageTeamSummary(w http.ResponseWriter, r *http.Request) {
	if base, tok := a.ccCloudCreds(); base == "" || tok == "" {
		s := ccUsageEmptySummaryFor(r)
		s["team"] = nil
		s["teams"] = []any{}
		s["by_member"] = []any{}
		writeJSON(w, 200, s)
		return
	}
	a.proxyCCUsageRead(w, r, "/api/cc-usage/team-summary")
}

// handleCCUsageMemberSummary proxies the cloud's GET /api/cc-usage/member-summary
// (Phase 3.1). Same machine-token proxy — the cloud does the owner authorization
// (a non-owner / unauthorized target gets 403 from the cloud). Local-only dev →
// empty member shape.
func (a *apiServer) handleCCUsageMemberSummary(w http.ResponseWriter, r *http.Request) {
	if base, tok := a.ccCloudCreds(); base == "" || tok == "" {
		s := ccUsageEmptySummaryFor(r)
		s["member"] = map[string]any{
			"user_id": r.URL.Query().Get("user_id"), "display_name": "", "email": "",
			"roles": []any{}, "is_you": false,
		}
		writeJSON(w, 200, s)
		return
	}
	a.proxyCCUsageRead(w, r, "/api/cc-usage/member-summary")
}

// ccUsageEmptySummaryFor is ccUsageEmptySummary for a request, echoing its
// provider filter the way the cloud does so the page knows it was honored.
func ccUsageEmptySummaryFor(r *http.Request) map[string]any {
	s := ccUsageEmptySummary(r.URL.Query().Get("window"))
	switch p := r.URL.Query().Get("provider"); p {
	case "claude", "codex":
		s["provider"] = p
	}
	return s
}

// ccUsageEmptySummary returns the contract's empty-shape summary: hero all
// zero, arrays empty (never null), in_out + subagent_share zeroed. The web
// client treats hero.turns == 0 as the "still gathering your usage" state.
func ccUsageEmptySummary(window string) map[string]any {
	windowDays := 30
	switch window {
	case "7d":
		windowDays = 7
	case "90d":
		windowDays = 90
	case "all":
		windowDays = 0
	case "30d":
		windowDays = 30
	default:
		window = "30d"
		windowDays = 30
	}
	return map[string]any{
		"window":       window,
		"window_days":  windowDays,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"currency":     "USD",
		"notional":     true,
		"hero": map[string]any{
			"cost_usd":      0,
			"turns":         0,
			"sessions":      0,
			"active_days":   0,
			"fresh_tokens":  0,
			"cached_tokens": 0,
			"models_used":   0,
		},
		"by_model":     []any{},
		"by_provider":  []any{},
		"provider":     "all",
		"by_role":      []any{},
		"top_projects": []any{},
		"daily":        []any{},
		"in_out": map[string]any{
			"in_app":  map[string]any{"turns": 0, "cost_usd": 0},
			"out_app": map[string]any{"turns": 0, "cost_usd": 0},
		},
		"subagent_share": map[string]any{
			"cost_usd":   0,
			"cost_share": 0,
		},
	}
}
