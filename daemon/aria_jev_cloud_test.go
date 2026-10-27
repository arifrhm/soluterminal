package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"soluterminal-shared/jevcontract"
	"soluterminal-shared/typesafe"
)

// jevFakeRelay stands in for the cloud's POST /api/jev/systemone. It checks
// the machine bearer and that the body is exactly the shared contract.
type jevFakeRelay struct {
	t         *testing.T
	calls     atomic.Int32
	status    int
	retry     string
	errorCode string
	answer    float64
	cautious  bool
	lastState atomic.Value
}

func (f *jevFakeRelay) start() *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A live SoluTerminal can discover this test listener and probe its
		// root while the host suite runs. Those are not relay model calls.
		if r.URL.Path == "/" && (r.Method == http.MethodHead || r.Method == http.MethodGet) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.calls.Add(1)
		if r.URL.Path != jevCloudPath || r.Method != http.MethodPost {
			f.t.Errorf("relay hit %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer machine-tok" {
			f.t.Errorf("relay bearer = %q, want the machine token", got)
		}
		body, _ := io.ReadAll(r.Body)
		var req typesafe.Request
		if err := json.Unmarshal(body, &req); err != nil {
			f.t.Errorf("relay body: %v", err)
		}
		if req.Model != jevcontract.ApprovalModel {
			f.t.Errorf("relay model = %q", req.Model)
		}
		if _, ok := jevcontract.MatchKnownQuestions(req.Questions); !ok {
			f.t.Errorf("relay received questions outside the contract: %v", req.Questions)
		}
		if s, ok := req.State.(string); ok {
			f.lastState.Store(s)
		}
		if f.status != 0 {
			if f.retry != "" {
				w.Header().Set("Retry-After", f.retry)
			}
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(`{"error":"` + f.errorCode + `"}`))
			return
		}
		answers := map[string]any{"permission_menu": map[string]any{"type": "noul", "noul": f.answer}}
		if f.cautious {
			answers["safe_action"] = map[string]any{"type": "noul", "noul": f.answer}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   jevcontract.ApprovalModel,
			"answers": answers,
			"usage":   map[string]any{"input_tokens": 512, "output_tokens": 2},
		})
	}))
	f.t.Cleanup(srv.Close)
	return srv
}

// newJevCloudFullHarness is a full-mode harness on a novel menu with no
// local TypeSafe key, paired to the fake relay.
func newJevCloudFullHarness(t *testing.T, relay *jevFakeRelay) (*Aria, *Pane, <-chan directAutoFrameRead) {
	t.Helper()
	a, p, frames := newJevFullHarness(t, jevNovelToolMenu)
	a.dataDir = t.TempDir() // no secret store entry
	// A build without a secret store reads the key from the environment,
	// where the detect harness seeded it.
	t.Setenv(jevTypesafeSecretEnv, "")
	a.jevClientFactory = func(string) jevSystemOneClient {
		t.Fatal("local client used without a local key")
		return nil
	}
	if relay != nil {
		a.setCloudCreds(relay.start().URL, "machine-tok")
	}
	return a, p, frames
}

func jevMetricsText(a *Aria) string {
	var b strings.Builder
	a.metrics.WriteText(&b, 1)
	return b.String()
}

func TestJevCloudUsedWithoutLocalKeyWhenPaired(t *testing.T) {
	relay := &jevFakeRelay{t: t, answer: .99}
	a, p, frames := newJevCloudFullHarness(t, relay)
	a.evaluateObservation(context.Background(), p.ID, p.Title, "")
	expectFrame(t, frames, "cloud-approved novel permission", "\r")
	if relay.calls.Load() != 1 {
		t.Fatalf("relay calls = %d, want 1", relay.calls.Load())
	}
	if s, _ := relay.lastState.Load().(string); !strings.Contains(s, "src/app.go") {
		t.Fatalf("relay state = %q, want the menu evidence", s)
	}
	outcomes := a.recentEvalOutcomes(p.ID)
	if len(outcomes) == 0 || outcomes[0].Model != ariaJevModel || outcomes[0].Kind != "fired" {
		t.Fatalf("attribution: %+v", outcomes)
	}
}

func TestJevCloudNotUsedWhenUnpaired(t *testing.T) {
	a, p, frames := newJevCloudFullHarness(t, nil)
	a.jevCloudClientFactory = func(string, string) jevSystemOneClient {
		t.Fatal("cloud used without pairing")
		return nil
	}
	a.evaluateObservation(context.Background(), p.ID, p.Title, "")
	expectNoFrame(t, frames, "unpaired novel permission")
	if m := jevMetricsText(a); !strings.Contains(m, `outcome="credential_fallback"`) {
		t.Fatalf("missing credential_fallback audit:\n%s", m)
	}
}

func TestJevLocalKeyWinsOverCloud(t *testing.T) {
	relay := &jevFakeRelay{t: t, answer: .99}
	a, p, frames := newJevFullHarness(t, jevNovelToolMenu) // seeds a local key
	a.setCloudCreds(relay.start().URL, "machine-tok")
	var local atomic.Int32
	a.jevClientFactory = func(key string) jevSystemOneClient {
		if key != "jev-test-key" {
			t.Fatalf("local key = %q", key)
		}
		return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
			local.Add(1)
			return jevDetectYesResponse(false), nil
		}}
	}
	a.evaluateObservation(context.Background(), p.ID, p.Title, "")
	expectFrame(t, frames, "local-key approval", "\r")
	if local.Load() != 1 || relay.calls.Load() != 0 {
		t.Fatalf("local=%d relay=%d; bring-your-own-key must not touch the cloud", local.Load(), relay.calls.Load())
	}
}

func TestJevCloudFailuresFallBackAndBackOff(t *testing.T) {
	for _, tc := range []struct {
		name, code, retry, outcome string
		status                     int
	}{
		{"quota", "smart_approvals_quota", "3600", jevCloudOutcomeQuota, http.StatusTooManyRequests},
		{"anonymous", "account_required", "", jevCloudOutcomeUnavailable, http.StatusForbidden},
		{"disabled", "smart_approvals_disabled", "", jevCloudOutcomeUnavailable, http.StatusServiceUnavailable},
		{"older cloud", "", "", jevCloudOutcomeUnavailable, http.StatusNotFound},
		{"provider fault", "provider_error", "", jevCloudOutcomeUnavailable, http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			relay := &jevFakeRelay{t: t, status: tc.status, errorCode: tc.code, retry: tc.retry}
			a, p, frames := newJevCloudFullHarness(t, relay)
			a.evaluateObservation(context.Background(), p.ID, p.Title, "")
			// A novel menu without a verdict keeps the human gate.
			expectNoFrame(t, frames, "cloud failure on a novel menu")
			if m := jevMetricsText(a); !strings.Contains(m, `outcome="`+tc.outcome+`"`) {
				t.Fatalf("missing %s audit:\n%s", tc.outcome, m)
			}
			if a.jevCloud.open(time.Now()) {
				t.Fatal("cloud refusal did not back off")
			}
			// While backed off, the relay is not asked again.
			if _, _, ok := a.jevApprovalClient("test"); ok {
				t.Fatal("backed-off cloud still offered as a client")
			}
			if relay.calls.Load() != 1 {
				t.Fatalf("relay calls = %d, want 1", relay.calls.Load())
			}
		})
	}
}

func TestJevCloudNetworkErrorFallsBack(t *testing.T) {
	a, p, frames := newJevCloudFullHarness(t, nil)
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	a.setCloudCreds(dead.URL, "machine-tok")
	a.evaluateObservation(context.Background(), p.ID, p.Title, "")
	expectNoFrame(t, frames, "network failure on a novel menu")
	if m := jevMetricsText(a); !strings.Contains(m, `outcome="cloud_unavailable"`) {
		t.Fatalf("missing cloud_unavailable audit:\n%s", m)
	}
}

func TestJevCloudRecognizedPermissionStillFallsBackLocally(t *testing.T) {
	// A known permission never needs Jev; a cloud outage must not slow it.
	relay := &jevFakeRelay{t: t, status: http.StatusServiceUnavailable}
	a, p, frames := newJevFullHarness(t, codexCommandApprovalScreen)
	a.dataDir = t.TempDir()
	a.setCloudCreds(relay.start().URL, "machine-tok")
	a.evaluateObservation(context.Background(), p.ID, p.Title, "")
	expectFrame(t, frames, "recognized permission", "\r")
	if relay.calls.Load() != 0 {
		t.Fatalf("recognized permission reached the relay %d times", relay.calls.Load())
	}
}

func TestJevCloudKnownEntitlementDenialSkipsRelay(t *testing.T) {
	relay := &jevFakeRelay{t: t, answer: .99}
	a, p, frames := newJevCloudFullHarness(t, relay)
	a.jevCloudEntitled = func() (bool, bool) { return false, true }
	a.evaluateObservation(context.Background(), p.ID, p.Title, "")
	expectNoFrame(t, frames, "unentitled novel permission")
	if relay.calls.Load() != 0 {
		t.Fatal("relay called despite a known entitlement denial")
	}
	// Unknown entitlement: attempt.
	a.jevCloudEntitled = func() (bool, bool) { return false, false }
	if _, source, ok := a.jevApprovalClient("test"); !ok || source != jevSourceCloud {
		t.Fatalf("unknown entitlement: source=%q ok=%v, want cloud attempt", source, ok)
	}
}

func TestJevCloudCautiousUsesApprovalRubricAndFallsBack(t *testing.T) {
	tail := claudeEditPermissionScreen
	for _, tc := range []struct {
		name    string
		relay   *jevFakeRelay
		outcome string
	}{
		{"low confidence", &jevFakeRelay{answer: .1, cautious: true}, "uncertain_fallback"},
		{"quota", &jevFakeRelay{status: http.StatusTooManyRequests, errorCode: "smart_approvals_quota", retry: "60"}, jevCloudOutcomeQuota},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.relay.t = t
			a, p := ariaJevSafetyFixture(t, tail, false)
			ariaJevRequireActionableFixture(t, p)
			a.metrics = NewMetrics()
			a.jevClientFactory = func(string) jevSystemOneClient { t.Fatal("local client used without key"); return nil }
			a.setCloudCreds(tc.relay.start().URL+"/", "machine-tok")
			if a.tryJevCautiousTerminal(context.Background(), p, p.ID, p.Title, tail, time.Now()) {
				t.Fatal("cloud failure or doubt must leave the incumbent evaluator in charge")
			}
			if tc.relay.calls.Load() != 1 {
				t.Fatalf("relay calls = %d", tc.relay.calls.Load())
			}
			if m := jevMetricsText(a); !strings.Contains(m, `outcome="`+tc.outcome+`"`) {
				t.Fatalf("missing %s audit:\n%s", tc.outcome, m)
			}
		})
	}
}

func TestJevCloudBackoffDurations(t *testing.T) {
	for _, tc := range []struct {
		err     error
		outcome string
		min     time.Duration
	}{
		{&jevCloudError{Status: http.StatusTooManyRequests, RetryAfter: 2 * time.Hour}, jevCloudOutcomeQuota, 2 * time.Hour},
		{&jevCloudError{Status: http.StatusTooManyRequests}, jevCloudOutcomeQuota, time.Minute},
		{&jevCloudError{Status: http.StatusTooManyRequests, RetryAfter: 72 * time.Hour}, jevCloudOutcomeQuota, 24 * time.Hour},
		{&jevCloudError{Status: http.StatusServiceUnavailable, Code: "smart_approvals_disabled"}, jevCloudOutcomeUnavailable, 30 * time.Minute},
		{context.DeadlineExceeded, jevCloudOutcomeUnavailable, time.Minute},
	} {
		outcome, d := jevCloudBackoff(tc.err)
		if outcome != tc.outcome || d != tc.min {
			t.Errorf("%v: outcome=%s backoff=%s, want %s %s", tc.err, outcome, d, tc.outcome, tc.min)
		}
	}
}
