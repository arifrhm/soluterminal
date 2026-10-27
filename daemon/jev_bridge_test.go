package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"soluterminal-shared/typesafe"
	"soluterminal/jev"
)

const (
	bridgeTestToken   = "bridge-token-sentinel"
	bridgeMasterToken = "master-token-sentinel"
	bridgeAPIKey      = "typesafe-key-sentinel"
)

type bridgeTestSecrets struct {
	value string
	ok    bool
	err   error
}

func (s bridgeTestSecrets) Resolve(runtimeID, env string) (string, bool, error) {
	if runtimeID != jevTypesafeRuntimeID || env != jevTypesafeSecretEnv {
		return "", false, errors.New("unexpected secret namespace")
	}
	return s.value, s.ok, s.err
}

type bridgeTestClient struct {
	call func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error)
}

func (c bridgeTestClient) SystemOneModel(ctx context.Context, model string, state any, questions map[string]typesafe.Question) (*typesafe.Response, error) {
	return c.call(ctx, model, state, questions)
}

type bridgeRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn bridgeRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func validBridgeInput(kind string) jev.BrowserDecisionInput {
	in := jev.BrowserDecisionInput{
		Kind: kind, Intent: "intent-sentinel", State: json.RawMessage(`{"settled":true,"label":"state-sentinel"}`),
	}
	if kind == jev.DecisionTargetRank {
		in.Candidates = []jev.BrowserDecisionCandidate{{
			ID: "candidate-id", Role: "button", Name: "candidate-name-sentinel", Context: "candidate-context-sentinel",
		}}
	}
	return in
}

func validBridgeResponse(model string, questions map[string]typesafe.Question) *typesafe.Response {
	answers := make(map[string]typesafe.Answer, len(questions))
	for id, question := range questions {
		switch question.Type {
		case "noul":
			value := 0.73
			answers[id] = typesafe.Answer{Type: "noul", Noul: &value}
		case "choice":
			instructions, _ := question.Instructions.(map[string]any)
			var options []string
			switch ordered := instructions["ordered_options"].(type) {
			case []string:
				options = append(options, ordered...)
			case []any:
				for _, option := range ordered {
					if text, ok := option.(string); ok {
						options = append(options, text)
					}
				}
			}
			if len(options) == 0 {
				panic("test provider received a choice without ordered options")
			}
			probabilities := make(map[string]float64, len(options))
			for _, option := range options {
				probabilities[option] = 0
			}
			probabilities[options[0]] = 1
			confidence := 0.12
			answers[id] = typesafe.Answer{
				Type: "choice", Choice: options[0], Probabilities: probabilities, Confidence: &confidence,
			}
		}
	}
	return &typesafe.Response{Model: model, Answers: answers, Usage: typesafe.Usage{InputTokens: 17}}
}

func enabledBridgeForTest(t *testing.T, client bridgeTestClient) (*jevBridge, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	b := newJevBridge(t.TempDir(), func() secretResolver {
		return bridgeTestSecrets{value: bridgeAPIKey, ok: true}
	}, log.New(logs, "", 0))
	b.mu.Lock()
	b.enabled = true
	b.token = bridgeTestToken
	b.mu.Unlock()
	b.clientFactory = func(apiKey string) jevSystemOneClient {
		if apiKey != bridgeAPIKey {
			t.Errorf("client API key = %q, want configured key", apiKey)
		}
		return client
	}
	return b, logs
}

func bridgeRequest(t *testing.T, b *jevBridge, token string, input jev.BrowserDecisionInput) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787"+jevBridgePath, bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:54321"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, req)
	return rec
}

func bridgeResponseBody(t *testing.T, response *typesafe.Response) *http.Response {
	t.Helper()
	body, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

func TestJevBridgeDefaultOffAndScopedAuthentication(t *testing.T) {
	var calls atomic.Int32
	client := bridgeTestClient{call: func(_ context.Context, model string, _ any, questions map[string]typesafe.Question) (*typesafe.Response, error) {
		calls.Add(1)
		return validBridgeResponse(model, questions), nil
	}}
	b := newJevBridge(t.TempDir(), func() secretResolver {
		return bridgeTestSecrets{value: bridgeAPIKey, ok: true}
	}, log.New(io.Discard, "", 0))
	b.clientFactory = func(string) jevSystemOneClient { return client }

	disabled := bridgeRequest(t, b, "", validBridgeInput(jev.DecisionActionOutcome))
	if disabled.Code != http.StatusForbidden || !strings.Contains(disabled.Body.String(), "jev_bridge_disabled") {
		t.Fatalf("disabled response: status=%d body=%s", disabled.Code, disabled.Body.String())
	}
	if _, err := os.Stat(filepath.Join(b.dataDir, jevBridgeTokenFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default-off bridge created a token: %v", err)
	}

	b.mu.Lock()
	b.enabled = true
	b.token = bridgeTestToken
	b.mu.Unlock()
	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "no bearer"},
		{name: "wrong bearer", token: "wrong-token"},
		{name: "master bearer", token: bridgeMasterToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := bridgeRequest(t, b, tc.token, validBridgeInput(jev.DecisionActionOutcome))
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}

	cookieReq := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787"+jevBridgePath, strings.NewReader(`{"kind":"action_outcome","intent":"x","state":{}}`))
	cookieReq.RemoteAddr = "127.0.0.1:54321"
	cookieReq.AddCookie(&http.Cookie{Name: tokenCookie, Value: bridgeTestToken})
	cookieRec := httptest.NewRecorder()
	b.ServeHTTP(cookieRec, cookieReq)
	if cookieRec.Code != http.StatusUnauthorized {
		t.Fatalf("bridge cookie status=%d, want 401", cookieRec.Code)
	}

	good := bridgeRequest(t, b, bridgeTestToken, validBridgeInput(jev.DecisionActionOutcome))
	if good.Code != http.StatusOK {
		t.Fatalf("scoped bearer status=%d body=%s", good.Code, good.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls=%d, want only the scoped-bearer request", calls.Load())
	}

	methodReq := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787"+jevBridgePath, nil)
	methodReq.Header.Set("Authorization", "Bearer "+bridgeTestToken)
	methodRec := httptest.NewRecorder()
	b.ServeHTTP(methodRec, methodReq)
	if methodRec.Code != http.StatusMethodNotAllowed || methodRec.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("method response status=%d allow=%q", methodRec.Code, methodRec.Header().Get("Allow"))
	}
}

func TestJevBridgeSuccessKindsAreTypedBoundedAndNonDisclosing(t *testing.T) {
	t.Setenv(jevBridgeEnableEnv, "")
	t.Setenv(jevBridgeModelEnv, "")
	dataDir := t.TempDir()
	seedRuntimeSecret(t, dataDir, jevTypesafeRuntimeID, jevTypesafeSecretEnv, bridgeAPIKey)
	if err := os.WriteFile(filepath.Join(dataDir, jevBridgeEnableMarker), []byte("enabled\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	b := newJevBridge(dataDir, func() secretResolver {
		// This is deliberately independent of a workflow engine/runtime.
		return openRuntimeSecrets(dataDir)
	}, log.New(logs, "", 0))
	b.mu.Lock()
	b.endpoint = "http://127.0.0.1:38123" + jevBridgePath
	b.mu.Unlock()
	if err := b.reconcile(); err != nil {
		t.Fatal(err)
	}
	_, scopedToken := b.authSnapshot()

	var authHeaders []string
	var requestedModels []string
	var returnedBodies strings.Builder
	var captureMu sync.Mutex
	b.clientFactory = func(apiKey string) jevSystemOneClient {
		client := typesafe.New(apiKey)
		client.BaseURL = "http://typesafe.invalid"
		client.HTTPClient = &http.Client{Transport: bridgeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			var providerRequest typesafe.Request
			if err := json.NewDecoder(req.Body).Decode(&providerRequest); err != nil {
				t.Errorf("decode provider request: %v", err)
			}
			captureMu.Lock()
			authHeaders = append(authHeaders, req.Header.Get("Authorization"))
			requestedModels = append(requestedModels, providerRequest.Model)
			captureMu.Unlock()
			return bridgeResponseBody(t, validBridgeResponse(providerRequest.Model, providerRequest.Questions)), nil
		})}
		return client
	}

	for _, kind := range []string{jev.DecisionTargetRank, jev.DecisionActionOutcome, jev.DecisionSuspiciousInstruction} {
		t.Run(kind, func(t *testing.T) {
			response := bridgeRequest(t, b, scopedToken, validBridgeInput(kind))
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			returnedBodies.Write(response.Body.Bytes())
			var top map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &top); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"model", "latency_ms", "input_tokens", "answers", "advisory"} {
				if _, ok := top[key]; !ok {
					t.Errorf("response missing %q: %s", key, response.Body.String())
				}
			}
			if len(top) != 5 {
				t.Errorf("response has unexpected fields: %s", response.Body.String())
			}
			var decoded jevBridgeResponse
			if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Model != jevBridgeDefaultModel || decoded.InputTokens != 17 || !decoded.Advisory || decoded.Answers.Kind != kind {
				t.Fatalf("response=%+v", decoded)
			}
			if kind == jev.DecisionTargetRank {
				if decoded.Answers.Decision.Type != "choice" || decoded.Answers.Decision.Option != "candidate-id" || decoded.Answers.Decision.Confidence == nil || *decoded.Answers.Decision.Confidence != 0.12 {
					t.Fatalf("low-confidence target answer was changed: %+v", decoded.Answers)
				}
			} else if kind == jev.DecisionSuspiciousInstruction && decoded.Answers.Decision.Type != "noul" {
				t.Fatalf("suspicious answer=%+v", decoded.Answers.Decision)
			}
		})
	}

	for _, got := range authHeaders {
		if got != "Bearer "+bridgeAPIKey {
			t.Errorf("provider Authorization=%q", got)
		}
	}
	for _, got := range requestedModels {
		if got != jevBridgeDefaultModel {
			t.Errorf("provider model=%q", got)
		}
	}
	if len(authHeaders) != 3 {
		t.Fatalf("provider calls=%d, want 3", len(authHeaders))
	}

	discovery, err := os.ReadFile(filepath.Join(dataDir, jevBridgeDiscoveryFile))
	if err != nil {
		t.Fatal(err)
	}
	combined := logs.String() + string(discovery)
	for _, sentinel := range []string{
		bridgeAPIKey, "intent-sentinel", "state-sentinel", "candidate-id",
		"candidate-name-sentinel", "candidate-context-sentinel", "provider-body-sentinel",
	} {
		if strings.Contains(combined, sentinel) {
			t.Errorf("logs/discovery leaked %q: %s", sentinel, combined)
		}
	}
	for _, sentinel := range []string{
		bridgeAPIKey, "intent-sentinel", "state-sentinel", "candidate-name-sentinel",
		"candidate-context-sentinel", "provider-body-sentinel",
	} {
		if strings.Contains(returnedBodies.String(), sentinel) {
			t.Errorf("response leaked %q: %s", sentinel, returnedBodies.String())
		}
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("audit lines=%d logs=%q", len(lines), logs.String())
	}
	for _, line := range lines {
		for _, field := range []string{"jev_bridge_audit", "kind=", "candidates=", "model=jev-1.13.0", "latency_ms=", "input_tokens=17", "outcome=success"} {
			if !strings.Contains(line, field) {
				t.Errorf("audit line missing %q: %s", field, line)
			}
		}
	}
	if strings.Contains(strings.Join(os.Args, "\x00"), bridgeAPIKey) {
		t.Fatal("API key appeared in process argv")
	}
	if matches, _ := filepath.Glob(filepath.Join(dataDir, "*event*")); len(matches) != 0 {
		t.Fatalf("bridge unexpectedly created workflow event files: %v", matches)
	}
}

func TestJevBridgeRejectsCloudRelayWithScopedBearer(t *testing.T) {
	var providerCalls atomic.Int32
	b, _ := enabledBridgeForTest(t, bridgeTestClient{call: func(_ context.Context, model string, _ any, questions map[string]typesafe.Question) (*typesafe.Response, error) {
		providerCalls.Add(1)
		return validBridgeResponse(model, questions), nil
	}})
	body, err := json.Marshal(validBridgeInput(jev.DecisionActionOutcome))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787"+jevBridgePath, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+bridgeTestToken)
	req.Header.Set("X-Forwarded-Prefix", "/m/cloud-machine")
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	(&apiServer{jevBridge: b}).routes(mux)

	// The cloud relay serves this central mux without authGate, so drive the
	// shared route exactly as a relayed request reaches it.
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "jev_bridge_local_only") {
		t.Fatalf("relay request status=%d body=%s", rec.Code, rec.Body.String())
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("relay request reached provider %d time(s)", providerCalls.Load())
	}
}

func TestJevBridgeStrictDecodeValidationAndBodyLimit(t *testing.T) {
	var providerCalls atomic.Int32
	client := bridgeTestClient{call: func(_ context.Context, model string, _ any, questions map[string]typesafe.Question) (*typesafe.Response, error) {
		providerCalls.Add(1)
		return validBridgeResponse(model, questions), nil
	}}
	b, _ := enabledBridgeForTest(t, client)
	b.limiter.limit = 1

	fortyOne := make([]jev.BrowserDecisionCandidate, 41)
	for i := range fortyOne {
		fortyOne[i].ID = string(rune('a' + i))
	}
	invalidInputs := []struct {
		name string
		body string
		want int
	}{
		{"unknown top field", `{"kind":"action_outcome","intent":"x","state":{},"extra":true}`, http.StatusBadRequest},
		{"unknown candidate field", `{"kind":"target_rank","intent":"x","state":{},"candidates":[{"id":"a","extra":true}]}`, http.StatusBadRequest},
		{"trailing json", `{"kind":"action_outcome","intent":"x","state":{}} {}`, http.StatusBadRequest},
		{"malformed json", `{"kind":`, http.StatusBadRequest},
	}
	contractCases := []struct {
		name string
		in   jev.BrowserDecisionInput
	}{
		{"unknown kind", jev.BrowserDecisionInput{Kind: "click", Intent: "x", State: json.RawMessage(`{}`)}},
		{"blank intent", jev.BrowserDecisionInput{Kind: jev.DecisionActionOutcome, Intent: " ", State: json.RawMessage(`{}`)}},
		{"long intent", jev.BrowserDecisionInput{Kind: jev.DecisionActionOutcome, Intent: strings.Repeat("界", 501), State: json.RawMessage(`{}`)}},
		{"null state", jev.BrowserDecisionInput{Kind: jev.DecisionActionOutcome, Intent: "x", State: json.RawMessage(`null`)}},
		{"array state", jev.BrowserDecisionInput{Kind: jev.DecisionActionOutcome, Intent: "x", State: json.RawMessage(`[]`)}},
		{"oversize state", jev.BrowserDecisionInput{Kind: jev.DecisionActionOutcome, Intent: "x", State: json.RawMessage(`{"page":"` + strings.Repeat("x", jev.MaxDecisionStateTokens*4) + `"}`)}},
		{"nested secret key", jev.BrowserDecisionInput{Kind: jev.DecisionActionOutcome, Intent: "x", State: json.RawMessage(`{"rows":[{"apiToken":"x"}]}`)}},
		{"no candidates", jev.BrowserDecisionInput{Kind: jev.DecisionTargetRank, Intent: "x", State: json.RawMessage(`{}`)}},
		{"too many candidates", jev.BrowserDecisionInput{Kind: jev.DecisionTargetRank, Intent: "x", State: json.RawMessage(`{}`), Candidates: fortyOne}},
		{"duplicate candidate", jev.BrowserDecisionInput{Kind: jev.DecisionTargetRank, Intent: "x", State: json.RawMessage(`{}`), Candidates: []jev.BrowserDecisionCandidate{{ID: "a"}, {ID: "a"}}}},
		{"reserved candidate", jev.BrowserDecisionInput{Kind: jev.DecisionTargetRank, Intent: "x", State: json.RawMessage(`{}`), Candidates: []jev.BrowserDecisionCandidate{{ID: "none"}}}},
		{"long candidate id", jev.BrowserDecisionInput{Kind: jev.DecisionTargetRank, Intent: "x", State: json.RawMessage(`{}`), Candidates: []jev.BrowserDecisionCandidate{{ID: strings.Repeat("界", 201)}}}},
		{"long candidate role", jev.BrowserDecisionInput{Kind: jev.DecisionTargetRank, Intent: "x", State: json.RawMessage(`{}`), Candidates: []jev.BrowserDecisionCandidate{{ID: "a", Role: strings.Repeat("界", 101)}}}},
		{"long candidate name", jev.BrowserDecisionInput{Kind: jev.DecisionTargetRank, Intent: "x", State: json.RawMessage(`{}`), Candidates: []jev.BrowserDecisionCandidate{{ID: "a", Name: strings.Repeat("界", 501)}}}},
		{"long candidate context", jev.BrowserDecisionInput{Kind: jev.DecisionTargetRank, Intent: "x", State: json.RawMessage(`{}`), Candidates: []jev.BrowserDecisionCandidate{{ID: "a", Context: strings.Repeat("界", 1001)}}}},
		{"action candidates", jev.BrowserDecisionInput{Kind: jev.DecisionActionOutcome, Intent: "x", State: json.RawMessage(`{}`), Candidates: []jev.BrowserDecisionCandidate{{ID: "a"}}}},
		{"suspicious candidates", jev.BrowserDecisionInput{Kind: jev.DecisionSuspiciousInstruction, Intent: "x", State: json.RawMessage(`{}`), Candidates: []jev.BrowserDecisionCandidate{{ID: "a"}}}},
	}
	for _, tc := range contractCases {
		body, err := json.Marshal(tc.in)
		if err != nil {
			t.Fatal(err)
		}
		invalidInputs = append(invalidInputs, struct {
			name string
			body string
			want int
		}{tc.name, string(body), http.StatusBadRequest})
	}
	invalidInputs = append(invalidInputs,
		struct {
			name string
			body string
			want int
		}{"empty action candidates", `{"kind":"action_outcome","intent":"x","state":{},"candidates":[]}`, http.StatusBadRequest},
		struct {
			name string
			body string
			want int
		}{"null action candidates", `{"kind":"action_outcome","intent":"x","state":{},"candidates":null}`, http.StatusBadRequest},
	)
	invalidInputs = append(invalidInputs, struct {
		name string
		body string
		want int
	}{"body limit", `{"kind":"action_outcome","intent":"` + strings.Repeat("x", int(jevBridgeBodyLimit)) + `","state":{}}`, http.StatusRequestEntityTooLarge})

	for _, tc := range invalidInputs {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787"+jevBridgePath, strings.NewReader(tc.body))
			if tc.name == "body limit" {
				req.ContentLength = -1 // Exercise MaxBytesReader, not only the header fast path.
			}
			req.Header.Set("Authorization", "Bearer "+bridgeTestToken)
			rec := httptest.NewRecorder()
			b.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("invalid requests consumed provider quota: calls=%d", providerCalls.Load())
	}

	valid := bridgeRequest(t, b, bridgeTestToken, validBridgeInput(jev.DecisionActionOutcome))
	if valid.Code != http.StatusOK || providerCalls.Load() != 1 {
		t.Fatalf("valid request after invalid requests: status=%d calls=%d body=%s", valid.Code, providerCalls.Load(), valid.Body.String())
	}
}

func TestJevBridgeLimiterConcurrentFixedWindow(t *testing.T) {
	var providerCalls atomic.Int32
	client := bridgeTestClient{call: func(_ context.Context, model string, _ any, questions map[string]typesafe.Question) (*typesafe.Response, error) {
		providerCalls.Add(1)
		return validBridgeResponse(model, questions), nil
	}}
	b, _ := enabledBridgeForTest(t, client)
	base := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	var nowNanos atomic.Int64
	nowNanos.Store(base.UnixNano())
	b.now = func() time.Time { return time.Unix(0, nowNanos.Load()).UTC() }

	const requests = 100
	statuses := make(chan int, requests)
	retries := make(chan string, requests)
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response := bridgeRequest(t, b, bridgeTestToken, validBridgeInput(jev.DecisionActionOutcome))
			statuses <- response.Code
			retries <- response.Header().Get("Retry-After")
		}()
	}
	wg.Wait()
	close(statuses)
	close(retries)
	ok, limited := 0, 0
	for status := range statuses {
		switch status {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			limited++
		default:
			t.Errorf("unexpected status %d", status)
		}
	}
	for retry := range retries {
		if retry != "" && retry != "60" {
			t.Errorf("Retry-After=%q, want 60", retry)
		}
	}
	if ok != jevBridgeRateLimit || limited != requests-jevBridgeRateLimit || int(providerCalls.Load()) != jevBridgeRateLimit {
		t.Fatalf("ok=%d limited=%d provider_calls=%d", ok, limited, providerCalls.Load())
	}

	nowNanos.Store(base.Add(30*time.Second + 100*time.Millisecond).UnixNano())
	stillLimited := bridgeRequest(t, b, bridgeTestToken, validBridgeInput(jev.DecisionActionOutcome))
	if stillLimited.Code != http.StatusTooManyRequests || stillLimited.Header().Get("Retry-After") != "30" {
		t.Fatalf("mid-window status=%d retry-after=%q", stillLimited.Code, stillLimited.Header().Get("Retry-After"))
	}
	nowNanos.Store(base.Add(jevBridgeRateWindow).UnixNano())
	reset := bridgeRequest(t, b, bridgeTestToken, validBridgeInput(jev.DecisionActionOutcome))
	if reset.Code != http.StatusOK || providerCalls.Load() != jevBridgeRateLimit+1 {
		t.Fatalf("boundary reset status=%d provider_calls=%d", reset.Code, providerCalls.Load())
	}
}

func TestJevBridgeProviderAndConfigurationFailuresFailClosed(t *testing.T) {
	t.Setenv(jevBridgeModelEnv, "")
	providerBody := "provider-body-sentinel"
	cases := []struct {
		name      string
		configure func(*jevBridge)
		call      func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error)
		want      int
	}{
		{
			name: "provider error", want: http.StatusBadGateway,
			call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
				return nil, &typesafe.APIError{StatusCode: http.StatusBadGateway, Body: providerBody}
			},
		},
		{
			name: "deadline", want: http.StatusGatewayTimeout,
			configure: func(b *jevBridge) { b.timeout = 10 * time.Millisecond },
			call: func(ctx context.Context, _ string, _ any, _ map[string]typesafe.Question) (*typesafe.Response, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
		{
			name: "client ignores deadline", want: http.StatusGatewayTimeout,
			configure: func(b *jevBridge) { b.timeout = 5 * time.Millisecond },
			call: func(_ context.Context, model string, _ any, questions map[string]typesafe.Question) (*typesafe.Response, error) {
				time.Sleep(10 * time.Millisecond)
				return validBridgeResponse(model, questions), nil
			},
		},
		{
			name: "model mismatch", want: http.StatusBadGateway,
			call: func(_ context.Context, _ string, _ any, questions map[string]typesafe.Question) (*typesafe.Response, error) {
				return validBridgeResponse("jev-9.9.9", questions), nil
			},
		},
		{
			name: "negative usage", want: http.StatusBadGateway,
			call: func(_ context.Context, model string, _ any, questions map[string]typesafe.Question) (*typesafe.Response, error) {
				response := validBridgeResponse(model, questions)
				response.Usage.InputTokens = -1
				return response, nil
			},
		},
		{
			name: "malformed typed answer", want: http.StatusBadGateway,
			call: func(_ context.Context, model string, _ any, questions map[string]typesafe.Question) (*typesafe.Response, error) {
				response := validBridgeResponse(model, questions)
				value := math.NaN()
				response.Answers["decision"] = typesafe.Answer{Type: "noul", Noul: &value}
				return response, nil
			},
		},
		{
			name: "nil response", want: http.StatusBadGateway,
			call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
				return nil, nil
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := bridgeTestClient{call: tc.call}
			b, logs := enabledBridgeForTest(t, client)
			if tc.configure != nil {
				tc.configure(b)
			}
			response := bridgeRequest(t, b, bridgeTestToken, validBridgeInput(jev.DecisionActionOutcome))
			if response.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.want, response.Body.String())
			}
			combined := response.Body.String() + logs.String()
			for _, forbidden := range []string{providerBody, bridgeAPIKey, "intent-sentinel", "state-sentinel"} {
				if strings.Contains(combined, forbidden) {
					t.Errorf("failure output leaked %q: %s", forbidden, combined)
				}
			}
			if got := strings.Count(strings.TrimSpace(logs.String()), "jev_bridge_audit"); got != 1 {
				t.Fatalf("audit records=%d logs=%q", got, logs.String())
			}
		})
	}

	t.Run("missing credentials", func(t *testing.T) {
		b := newJevBridge(t.TempDir(), func() secretResolver { return bridgeTestSecrets{} }, log.New(io.Discard, "", 0))
		b.mu.Lock()
		b.enabled, b.token = true, bridgeTestToken
		b.mu.Unlock()
		response := bridgeRequest(t, b, bridgeTestToken, validBridgeInput(jev.DecisionActionOutcome))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("invalid model alias", func(t *testing.T) {
		t.Setenv(jevBridgeModelEnv, "jev-latest")
		client := bridgeTestClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
			t.Fatal("invalid model reached provider")
			return nil, nil
		}}
		b, _ := enabledBridgeForTest(t, client)
		response := bridgeRequest(t, b, bridgeTestToken, validBridgeInput(jev.DecisionActionOutcome))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
}

func TestJevBridgeConfiguredPinDeadlineAndRetryPolicy(t *testing.T) {
	t.Setenv(jevBridgeModelEnv, "jev-2.3.4")
	client := bridgeTestClient{call: func(ctx context.Context, model string, _ any, questions map[string]typesafe.Question) (*typesafe.Response, error) {
		if model != "jev-2.3.4" {
			t.Fatalf("model=%q", model)
		}
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("provider context has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining <= 4*time.Second || remaining > jevBridgeTimeout {
			t.Fatalf("deadline remaining=%s, want shared five-second bound", remaining)
		}
		return validBridgeResponse(model, questions), nil
	}}
	b, _ := enabledBridgeForTest(t, client)
	response := bridgeRequest(t, b, bridgeTestToken, validBridgeInput(jev.DecisionActionOutcome))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	productionClient, ok := newJevBridge(t.TempDir(), nil, nil).clientFactory("x").(*typesafe.Client)
	if !ok {
		t.Fatal("production bridge factory did not return TypeSafe client")
	}
	policy := productionClient.Retry
	if policy.MaxAttempts != 2 || !policy.DisableTransportRetries || len(policy.RetryStatusCodes) != 1 || policy.RetryStatusCodes[0] != http.StatusTooManyRequests {
		t.Fatalf("bridge retry policy=%+v", policy)
	}
}

func TestJevBridgeLifecycleDiscoveryAndTokenPersistence(t *testing.T) {
	t.Setenv(jevBridgeEnableEnv, "")
	dataDir := t.TempDir()
	discoveryPath := filepath.Join(dataDir, jevBridgeDiscoveryFile)
	tokenPath := filepath.Join(dataDir, jevBridgeTokenFile)
	if err := os.WriteFile(discoveryPath, []byte(`{"stale":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b := newJevBridge(dataDir, nil, log.New(io.Discard, "", 0))
	b.reconcileEvery = 5 * time.Millisecond
	endpoint := "http://127.0.0.1:45678" + jevBridgePath
	if err := b.Start(endpoint); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	if enabled, _ := b.authSnapshot(); enabled {
		t.Fatal("default-off bridge started enabled")
	}
	if _, err := os.Stat(discoveryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled startup retained stale discovery: %v", err)
	}
	if _, err := os.Stat(tokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled startup created token: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dataDir, jevBridgeEnableMarker), []byte("enabled\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForBridgeState(t, b, true, discoveryPath)
	_, firstToken := b.authSnapshot()
	if len(firstToken) != 64 {
		t.Fatalf("token length=%d, want 64 hex characters", len(firstToken))
	}
	for _, path := range []string{tokenPath, discoveryPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode=%#o, want 0600", filepath.Base(path), info.Mode().Perm())
		}
	}
	assertBridgeDiscovery(t, discoveryPath, endpoint, firstToken)

	if err := os.Chmod(tokenPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(discoveryPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := b.reconcile(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{tokenPath, discoveryPath} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode repair for %s: info=%v err=%v", path, info, err)
		}
	}

	if err := os.Remove(filepath.Join(dataDir, jevBridgeEnableMarker)); err != nil {
		t.Fatal(err)
	}
	waitForBridgeState(t, b, false, discoveryPath)
	if _, err := os.Stat(tokenPath); err != nil {
		t.Fatalf("disable removed persistent scoped token: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, jevBridgeEnableMarker), []byte("enabled\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForBridgeState(t, b, true, discoveryPath)
	_, secondToken := b.authSnapshot()
	if secondToken != firstToken {
		t.Fatalf("re-enable rotated token: first=%q second=%q", firstToken, secondToken)
	}
	assertBridgeDiscovery(t, discoveryPath, endpoint, firstToken)

	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(discoveryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shutdown retained discovery: %v", err)
	}
}

func TestJevBridgeDiscoveryEndpointFollowsListenerAddressFamily(t *testing.T) {
	for _, tc := range []struct {
		name string
		addr net.Addr
		want string
	}{
		{name: "IPv4 loopback", addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 45678}, want: "http://127.0.0.1:45678" + jevBridgePath},
		{name: "IPv6 loopback", addr: &net.TCPAddr{IP: net.ParseIP("::1"), Port: 45679}, want: "http://[::1]:45679" + jevBridgePath},
		{name: "IPv4 wildcard", addr: &net.TCPAddr{IP: net.ParseIP("0.0.0.0"), Port: 45680}, want: "http://127.0.0.1:45680" + jevBridgePath},
		{name: "IPv6 wildcard", addr: &net.TCPAddr{IP: net.ParseIP("::"), Port: 45681}, want: "http://[::1]:45681" + jevBridgePath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint, err := jevBridgeEndpointForListener(tc.addr)
			if err != nil {
				t.Fatal(err)
			}
			if endpoint != tc.want {
				t.Fatalf("endpoint=%q want=%q", endpoint, tc.want)
			}

			t.Setenv(jevBridgeEnableEnv, "1")
			dataDir := t.TempDir()
			b := newJevBridge(dataDir, nil, log.New(io.Discard, "", 0))
			b.reconcileEvery = time.Hour
			if err := b.Start(endpoint); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = b.Close() })
			_, token := b.authSnapshot()
			assertBridgeDiscovery(t, filepath.Join(dataDir, jevBridgeDiscoveryFile), tc.want, token)
		})
	}
}

func TestJevBridgeFailedArtifactCreationLeavesHandlerDisabled(t *testing.T) {
	t.Setenv(jevBridgeEnableEnv, "1")
	staleDir := t.TempDir()
	staleDiscovery := filepath.Join(staleDir, jevBridgeDiscoveryFile)
	if err := os.WriteFile(staleDiscovery, []byte(`{"stale":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	withoutEndpoint := newJevBridge(staleDir, nil, log.New(io.Discard, "", 0))
	if err := withoutEndpoint.reconcile(); err == nil {
		t.Fatal("enabled reconciliation without an endpoint unexpectedly succeeded")
	}
	if _, err := os.Stat(staleDiscovery); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed enable retained stale discovery: %v", err)
	}

	dataDir := filepath.Join(t.TempDir(), "missing", "data")
	b := newJevBridge(dataDir, nil, log.New(io.Discard, "", 0))
	b.mu.Lock()
	b.endpoint = "http://127.0.0.1:45678" + jevBridgePath
	b.mu.Unlock()
	if err := b.reconcile(); err == nil {
		t.Fatal("reconcile unexpectedly created artifacts in a missing data directory")
	}
	if enabled, token := b.authSnapshot(); enabled || token != "" {
		t.Fatalf("failed reconcile left enabled=%v token=%q", enabled, token)
	}
	response := bridgeRequest(t, b, "", validBridgeInput(jev.DecisionActionOutcome))
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestJevBridgeExactRouteRegistration(t *testing.T) {
	mux := http.NewServeMux()
	(&apiServer{jevBridge: newJevBridge(t.TempDir(), nil, log.New(io.Discard, "", 0))}).routes(mux)
	for _, tc := range []struct {
		path string
		want int
	}{
		{jevBridgePath, http.StatusForbidden},
		{jevBridgePath + "/", http.StatusNotFound},
	} {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787"+tc.path, strings.NewReader(`{"kind":"action_outcome","intent":"x","state":{}}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("path=%q status=%d want=%d body=%s", tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
}

func waitForBridgeState(t *testing.T, b *jevBridge, wantEnabled bool, discoveryPath string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		enabled, _ := b.authSnapshot()
		_, err := os.Stat(discoveryPath)
		discovered := err == nil
		if enabled == wantEnabled && discovered == wantEnabled {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	enabled, _ := b.authSnapshot()
	_, err := os.Stat(discoveryPath)
	t.Fatalf("bridge state did not reconcile: enabled=%v discovery_err=%v want_enabled=%v", enabled, err, wantEnabled)
}

func assertBridgeDiscovery(t *testing.T, path, endpoint, token string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := json.Marshal(jevBridgeDiscovery{URL: endpoint, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, expected) {
		t.Fatalf("discovery=%s want=%s", raw, expected)
	}
}
