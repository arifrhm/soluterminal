package typesafe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, h http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New("test-key")
	c.BaseURL = srv.URL
	c.HTTPClient = srv.Client()
	c.Retry = RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}
	return c, srv
}

func TestSystemOneParsesAllAnswerTypes(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("path = %q", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "jev-1.13.0",
			"answers": map[string]any{
				"tier": map[string]any{
					"type":          "choice",
					"choice":        "standard",
					"probabilities": map[string]float64{"small": 0.1, "standard": 0.7, "complex": 0.2},
					"confidence":    0.62,
				},
				"risk":       map[string]any{"type": "noul", "noul": 0.23},
				"effort":     map[string]any{"type": "score", "score": 1.6, "legend": map[string]any{"0": "low", "1": "mid", "2": "high"}, "probabilities": map[string]float64{"0": 0.05, "1": 0.3, "2": 0.65}, "confidence": 0.78},
				"sentinel_x": map[string]any{"type": "noul", "noul": 0.99},
			},
			"usage": map[string]any{"input_tokens": 1234, "output_tokens": 48},
		})
	}))

	q := map[string]Question{
		"tier":       Choice("Which tier?", map[string]any{"small": "fast", "standard": "default"}),
		"risk":       Noul("Does this change credentials?"),
		"effort":     Score("How hard?", []any{"low", "mid", "high"}),
		"sentinel_x": Noul("Is this speculative?"),
	}
	resp, err := c.SystemOneModel(context.Background(), "jev-latest", "state", q)
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotBody["model"] != "jev-latest" {
		t.Errorf("request model = %v", gotBody["model"])
	}
	if _, ok := gotBody["questions"].(map[string]any)["tier"]; !ok {
		t.Errorf("questions missing tier: %#v", gotBody["questions"])
	}
	if resp.Model != "jev-1.13.0" {
		t.Errorf("model = %q", resp.Model)
	}
	if v, _ := resp.Answers["risk"].NoulValue(); v != 0.23 {
		t.Errorf("noul = %v", v)
	}
	if v, _ := resp.Answers["effort"].ScoreValue(); v != 1.6 {
		t.Errorf("score = %v", v)
	}
	if resp.Answers["tier"].Choice != "standard" {
		t.Errorf("choice = %q", resp.Answers["tier"].Choice)
	}
	if opt, p := resp.Answers["tier"].TopChoice(); opt != "standard" || p != 0.7 {
		t.Errorf("TopChoice = %q %v", opt, p)
	}
	if v, ok := resp.Answers["risk"].ConfidenceValue(); ok {
		t.Errorf("noul must not carry confidence; got %v", v)
	}
	if resp.Usage.InputTokens != 1234 || resp.Usage.OutputTokens != 48 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if got := resp.Usage.EstimatedCostUSD(); got <= 0 {
		t.Errorf("cost = %v", got)
	}
}

func TestUnauthorizedIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid api key"}`))
	}))
	_, err := c.SystemOneModel(context.Background(), "jev-latest", "s", map[string]Question{"q": Noul("?")})
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsUnauthorized(err) {
		t.Errorf("IsUnauthorized(%v) = false", err)
	}
	var ae *APIError
	if !errors.As(err, &ae) || ae.StatusCode != 401 {
		t.Errorf("err = %#v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (no retry on 401)", calls.Load())
	}
}

func TestRateLimitHonorsRetryAfterThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"slow down"}`))
			return
		}
		_, _ = w.Write([]byte(`{"model":"jev-latest","answers":{"q":{"type":"noul","noul":0.5}},"usage":{"input_tokens":1}}`))
	}))
	resp, err := c.SystemOneModel(context.Background(), "jev-latest", "s", map[string]Question{"q": Noul("?")})
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
	if v, _ := resp.Answers["q"].NoulValue(); v != 0.5 {
		t.Errorf("noul = %v", v)
	}
}

func TestOverloaded529IsRetried(t *testing.T) {
	var calls atomic.Int32
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(529)
			return
		}
		_, _ = w.Write([]byte(`{"model":"m","answers":{},"usage":{}}`))
	}))
	if _, err := c.SystemOneModel(context.Background(), "jev-latest", "s", map[string]Question{"q": Noul("?")}); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

func TestValidationFailsBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	cases := []struct {
		name string
		q    map[string]Question
	}{
		{"empty", map[string]Question{}},
		{"bad type", map[string]Question{"q": {Type: "essay", Instructions: "x"}}},
		{"choice no criteria", map[string]Question{"q": {Type: "choice", Instructions: "x"}}},
		{"score one level", map[string]Question{"q": Score("x", []any{"only"})}},
		{"blank id", map[string]Question{" ": Noul("x")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.SystemOneModel(context.Background(), "jev-latest", "s", tc.q); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	if calls.Load() != 0 {
		t.Errorf("network calls = %d, want 0", calls.Load())
	}
}

func TestListModels(t *testing.T) {
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"jev-latest","description":"flagship","release_date":"2026-09-01"}]}`))
	}))
	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 1 || models[0].Name != "jev-latest" || models[0].ReleaseDate != "2026-09-01" {
		t.Fatalf("models = %+v", models)
	}
}

func TestStructuredInstructionsSurviveMarshaling(t *testing.T) {
	var got map[string]any
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"model":"m","answers":{"q":{"type":"choice","choice":"a","probabilities":{"a":1},"confidence":1}},"usage":{}}`))
	}))
	_, err := c.SystemOneModel(context.Background(), "jev-latest",
		map[string]any{"field": map[string]any{"name": "invoice_number"}},
		map[string]Question{
			"q": Choice(
				map[string]any{"question": "Which value?", "focus": "exact"},
				map[string]any{"a": map[string]any{"what": "yes", "examples": []string{"x"}}, "b": nil},
			),
		})
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	state := got["state"].(map[string]any)
	if state["field"].(map[string]any)["name"] != "invoice_number" {
		t.Errorf("state = %#v", state)
	}
	instr := got["questions"].(map[string]any)["q"].(map[string]any)["instructions"].(map[string]any)
	if instr["focus"] != "exact" {
		t.Errorf("instructions = %#v", instr)
	}
	crit := got["questions"].(map[string]any)["q"].(map[string]any)["criteria"].(map[string]any)
	if crit["a"].(map[string]any)["what"] != "yes" {
		t.Errorf("criteria = %#v", crit)
	}
}

func TestNewFromEnv(t *testing.T) {
	t.Setenv(EnvAPIKey, "")
	if _, err := NewFromEnv(); !errors.Is(err, ErrNoAPIKey) {
		t.Fatalf("err = %v, want ErrNoAPIKey", err)
	}
	if Configured() {
		t.Fatal("Configured() = true with empty key")
	}
	t.Setenv(EnvAPIKey, "  abc  ")
	t.Setenv(EnvBaseURL, "https://example.test/")
	c, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if c.APIKey != "abc" {
		t.Errorf("key = %q", c.APIKey)
	}
	if c.BaseURL != "https://example.test" {
		t.Errorf("base = %q", c.BaseURL)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func retryTestResponse(status int, retryAfter string) *http.Response {
	header := make(http.Header)
	if retryAfter != "" {
		header.Set("Retry-After", retryAfter)
	}
	return &http.Response{
		StatusCode: status, Header: header,
		Body: io.NopCloser(strings.NewReader(`{"error":"provider sentinel"}`)),
	}
}

func exact429Client(fn roundTripFunc) *Client {
	client := New("key")
	client.BaseURL = "http://typesafe.invalid"
	client.HTTPClient = &http.Client{Transport: fn}
	client.Retry = RetryPolicy{
		MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond,
		RetryStatusCodes: []int{http.StatusTooManyRequests}, DisableTransportRetries: true,
	}
	return client
}

func TestExact429RetryPolicyAttemptsAtMostTwice(t *testing.T) {
	var calls atomic.Int32
	client := exact429Client(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return retryTestResponse(http.StatusTooManyRequests, "0"), nil
	})
	_, err := client.SystemOneModel(context.Background(), "jev-1.13.0", "state", map[string]Question{"q": Noul("?")})
	if err == nil {
		t.Fatal("expected rate-limit error")
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

func TestExact429RetryPolicyHonorsRetryAfterFormatsAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header func() string
	}{
		{"seconds", func() string { return "1" }},
		{"http date", func() string { return time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			client := exact429Client(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return retryTestResponse(http.StatusTooManyRequests, tc.header()), nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			_, err := client.SystemOneModel(ctx, "jev-1.13.0", "state", map[string]Question{"q": Noul("?")})
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error = %v, want deadline exceeded", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("calls = %d, retry did not wait for Retry-After", calls.Load())
			}
		})
	}
}

func TestExact429RetryPolicyDoesNotRetryOtherFailures(t *testing.T) {
	for _, status := range []int{529, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			client := exact429Client(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return retryTestResponse(status, ""), nil
			})
			if _, err := client.SystemOneModel(context.Background(), "jev-1.13.0", "state", map[string]Question{"q": Noul("?")}); err == nil {
				t.Fatal("expected provider error")
			}
			if calls.Load() != 1 {
				t.Fatalf("calls = %d, want 1", calls.Load())
			}
		})
	}
	t.Run("transport", func(t *testing.T) {
		var calls atomic.Int32
		client := exact429Client(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, errors.New("transport sentinel")
		})
		if _, err := client.SystemOneModel(context.Background(), "jev-1.13.0", "state", map[string]Question{"q": Noul("?")}); err == nil {
			t.Fatal("expected transport error")
		}
		if calls.Load() != 1 {
			t.Fatalf("calls = %d, want 1", calls.Load())
		}
	})
}
