package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"soluterminal-shared/typesafe"
)

// Cloud smart approvals. A signed-in (paired) install without its own
// TypeSafe key relays the Jev auto-approval request to the cloud, which
// answers it on the operator's key within a daily allowance. A configured
// local key always wins: bring-your-own-key never leaves the machine through
// our cloud. Any cloud refusal or fault falls back to the same local path a
// missing key takes, and a short backoff stops a refused install from paying
// a round trip on every menu.

const (
	jevCloudPath      = "/api/jev/systemone"
	jevCloudBodyLimit = 1 << 20

	jevSourceLocal = "local"
	jevSourceCloud = "cloud"

	jevCloudOutcomeQuota       = "cloud_quota"
	jevCloudOutcomeUnavailable = "cloud_unavailable"
)

// jevCloudError is a non-2xx relay answer. Code is the relay's JSON error.
type jevCloudError struct {
	Status     int
	Code       string
	RetryAfter time.Duration
}

func (e *jevCloudError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("jev cloud: HTTP %d %s", e.Status, e.Code)
	}
	return fmt.Sprintf("jev cloud: HTTP %d", e.Status)
}

// jevCloudClient implements jevSystemOneClient against the cloud relay with
// the machine token. The wire body is exactly typesafe's, so the relay can
// allow-list it against the shared contract.
type jevCloudClient struct {
	base      string
	token     string
	userAgent string
	http      *http.Client
}

func newJevCloudClient(base, token, userAgent string) *jevCloudClient {
	return &jevCloudClient{
		base:      strings.TrimRight(base, "/"),
		token:     token,
		userAgent: userAgent,
		// The caller's context carries the real deadline (ariaJevTimeout).
		http: &http.Client{Timeout: typesafe.DefaultModelHTTPTimeout},
	}
}

func (c *jevCloudClient) SystemOneModel(ctx context.Context, model string, state any, questions map[string]typesafe.Question) (*typesafe.Response, error) {
	body, err := json.Marshal(typesafe.Request{State: state, Model: model, Questions: questions})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+jevCloudPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, jevCloudBodyLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > jevCloudBodyLimit {
		return nil, errors.New("jev cloud: response too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var payload struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &payload)
		retry := time.Duration(0)
		if secs, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && secs > 0 {
			retry = time.Duration(secs) * time.Second
		}
		return nil, &jevCloudError{Status: resp.StatusCode, Code: payload.Error, RetryAfter: retry}
	}
	recordJevCloudQuota(resp.Header)
	var out typesafe.Response
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("jev cloud: decode response: %w", err)
	}
	if out.Answers == nil {
		out.Answers = map[string]typesafe.Answer{}
	}
	return &out, nil
}

// jevCloudQuotaState is the last daily allowance the relay reported, shown
// next to "Approve with caution" so the 100-a-day limit is never a surprise.
type jevCloudQuotaState struct {
	mu        sync.Mutex
	remaining int
	limit     int
	at        time.Time
}

var jevCloudQuota jevCloudQuotaState

func recordJevCloudQuota(h http.Header) {
	remaining, err := strconv.Atoi(strings.TrimSpace(h.Get("X-Jev-Remaining")))
	if err != nil {
		return
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(h.Get("X-Jev-Limit")))
	jevCloudQuota.mu.Lock()
	jevCloudQuota.remaining, jevCloudQuota.limit, jevCloudQuota.at = remaining, limit, time.Now()
	jevCloudQuota.mu.Unlock()
}

// snapshot returns the allowance if it was reported within a day.
func (q *jevCloudQuotaState) snapshot(now time.Time) (remaining, limit int, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.at.IsZero() || now.Sub(q.at) >= 24*time.Hour {
		return 0, 0, false
	}
	return q.remaining, q.limit, true
}

// handleSmartApprovalsStatus reports where smart approvals come from and the
// last known daily allowance: GET /api/smart-approvals.
func (a *apiServer) handleSmartApprovalsStatus(w http.ResponseWriter, _ *http.Request) {
	out := map[string]any{"source": "none"}
	if a.aria != nil {
		if _, source, ok := a.aria.jevApprovalClient("soluterminal-status/1"); ok {
			out["source"] = source
		} else if reason := a.aria.jevCloud.closedReason(time.Now()); reason != "" {
			out["source"] = jevSourceCloud
			out["paused"] = reason
		}
	}
	if remaining, limit, ok := jevCloudQuota.snapshot(time.Now()); ok {
		out["remaining"] = remaining
		if limit > 0 {
			out["limit"] = limit
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// jevCloudGate remembers a cloud refusal so the next menus skip the relay
// until the refusal can plausibly have changed.
type jevCloudGate struct {
	mu     sync.Mutex
	until  time.Time
	reason string
}

func (g *jevCloudGate) open(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !now.Before(g.until)
}

// closedReason is the outcome that closed the gate, or "" while it is open.
func (g *jevCloudGate) closedReason(now time.Time) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !now.Before(g.until) {
		return ""
	}
	return g.reason
}

func (g *jevCloudGate) close(now time.Time, d time.Duration, reason string) {
	g.mu.Lock()
	g.until, g.reason = now.Add(d), reason
	g.mu.Unlock()
}

// jevCloudBackoff maps a relay failure to an audit outcome and how long to
// stop asking. Quota honours Retry-After; account/feature refusals wait long
// enough to pick up a sign-in or entitlement change; transient faults retry
// soon.
func jevCloudBackoff(err error) (outcome string, backoff time.Duration) {
	var ce *jevCloudError
	if !errors.As(err, &ce) {
		return jevCloudOutcomeUnavailable, time.Minute
	}
	switch ce.Status {
	case http.StatusTooManyRequests:
		d := ce.RetryAfter
		if d < time.Minute {
			d = time.Minute
		}
		if d > 24*time.Hour {
			d = 24 * time.Hour
		}
		return jevCloudOutcomeQuota, d
	case http.StatusUnauthorized, http.StatusForbidden:
		return jevCloudOutcomeUnavailable, 30 * time.Minute
	case http.StatusNotFound:
		// An older cloud without the relay.
		return jevCloudOutcomeUnavailable, time.Hour
	case http.StatusServiceUnavailable:
		if ce.Code == "smart_approvals_disabled" || ce.Code == "smart_approvals_unavailable" {
			return jevCloudOutcomeUnavailable, 30 * time.Minute
		}
		return jevCloudOutcomeUnavailable, 2 * time.Minute
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		// A contract mismatch will not fix itself within one menu.
		return jevCloudOutcomeUnavailable, 10 * time.Minute
	}
	return jevCloudOutcomeUnavailable, time.Minute
}

// jevCloudFailure records a relay failure and returns its audit outcome.
// Nothing about the pane or the evidence is logged.
func (a *Aria) jevCloudFailure(err error) string {
	outcome, backoff := jevCloudBackoff(err)
	a.jevCloud.close(time.Now(), backoff, outcome)
	status := 0
	var ce *jevCloudError
	if errors.As(err, &ce) {
		status = ce.Status
	}
	if a.metrics != nil {
		a.metrics.IncLabelled("solud_aria_jev_cloud_total", map[string]string{"outcome": outcome})
	}
	log.Printf("aria_jev_cloud outcome=%s status=%d backoff=%s", outcome, status, backoff)
	return outcome
}

// jevApprovalClient resolves the Jev client for auto-approval. ok=false
// means neither a local key nor an eligible cloud account is available (the
// caller's credential_fallback). A nil client with ok=true keeps the
// existing provider_fallback for a test factory that declines.
func (a *Aria) jevApprovalClient(userAgent string) (client jevSystemOneClient, source string, ok bool) {
	key, configured, err := openRuntimeSecrets(a.dataDir).Resolve(jevTypesafeRuntimeID, jevTypesafeSecretEnv)
	if err == nil && configured && strings.TrimSpace(key) != "" {
		if a.jevClientFactory != nil {
			return a.jevClientFactory(key), jevSourceLocal, true
		}
		live := typesafe.New(key)
		if base := strings.TrimSpace(os.Getenv(typesafe.EnvBaseURL)); base != "" {
			live.BaseURL = strings.TrimSuffix(base, "/")
		}
		live.UserAgent = userAgent
		live.Retry = typesafe.RetryPolicy{MaxAttempts: 1, DisableTransportRetries: true, RetryStatusCodes: []int{}}
		return live, jevSourceLocal, true
	}
	base, token := a.cloudCreds()
	if strings.TrimSpace(base) == "" || strings.TrimSpace(token) == "" {
		return nil, "", false
	}
	if a.jevCloudEntitled != nil {
		// A known "no" skips the relay; an unknown verdict attempts it and
		// lets the relay's 403/503 decide.
		if allowed, known := a.jevCloudEntitled(); known && !allowed {
			return nil, "", false
		}
	}
	if !a.jevCloud.open(time.Now()) {
		return nil, "", false
	}
	if a.jevCloudClientFactory != nil {
		return a.jevCloudClientFactory(base, token), jevSourceCloud, true
	}
	return newJevCloudClient(base, token, userAgent), jevSourceCloud, true
}

// jevProviderFailureOutcome keeps "provider_fallback" for local keys and
// invalid answers, and names cloud refusals so quota exhaustion is visible.
func (a *Aria) jevProviderFailureOutcome(source string, err error) string {
	if source == jevSourceCloud && err != nil {
		return a.jevCloudFailure(err)
	}
	return "provider_fallback"
}
