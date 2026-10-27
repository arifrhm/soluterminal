package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type harnessProbeRoundTripper func(*http.Request) (*http.Response, error)

func (f harnessProbeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestProbeHarnessAccountCanonicalizesBeforeHTTP(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"", "deepseek-flash"},
		{"deepseek-v4-pro", "deepseek-flash"},
		{" DEEPSEEK/DEEPSEEK-V4-PRO ", "deepseek-flash"},
		{"deepseek-v4-flash-vision-exp", "deepseek-v4-flash-vision-exp"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			requests := 0
			client := &http.Client{Transport: harnessProbeRoundTripper(func(req *http.Request) (*http.Response, error) {
				requests++
				var body map[string]any
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if got := body["model"]; got != tc.want {
					t.Fatalf("wire model=%v, want %q", got, tc.want)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
			})}
			acc := harnessTestAccount("main", "key")
			acc.Model = tc.raw
			result := probeHarnessAccountForProvider(context.Background(), "https://deepseek.invalid", "deepseek", acc, client)
			if result.Status != "authenticated" || result.Model != tc.want || requests != 1 {
				t.Fatalf("result=%+v requests=%d", result, requests)
			}
		})
	}

	requests := 0
	client := &http.Client{Transport: harnessProbeRoundTripper(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, nil
	})}
	acc := harnessTestAccount("main", "key")
	acc.Model = "deepseek-v4-pro-preview"
	result := probeHarnessAccountForProvider(context.Background(), "https://deepseek.invalid", "deepseek", acc, client)
	if result.Status != "error" || requests != 0 {
		t.Fatalf("unsafe result=%+v requests=%d", result, requests)
	}
}

func TestProbeHarnessAccountClassifiesOutcomes(t *testing.T) {
	cases := []struct {
		code int
		want string
	}{
		{200, "authenticated"},
		{201, "authenticated"},
		{401, "auth-failed"},
		{403, "auth-failed"},
		{402, "billing"},
		{429, "rate-limited"},
		{500, "unavailable"},
		{400, "error"},
	}
	for _, tc := range cases {
		var gotAuth, gotPath string
		var gotBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			gotPath = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(tc.code)
			_, _ = w.Write([]byte(`{"error":"SECRET-BODY-DO-NOT-RETURN"}`))
		}))
		acc := harnessTestAccount("main", "sk-probe-secret")
		result := probeHarnessAccount(context.Background(), server.URL, acc, server.Client())
		server.Close()
		if result.Status != tc.want || result.HTTPStatus != tc.code {
			t.Fatalf("HTTP %d: got %q/%d want %q", tc.code, result.Status, result.HTTPStatus, tc.want)
		}
		if gotAuth != "Bearer sk-probe-secret" || gotPath != "/chat/completions" {
			t.Fatalf("probe wiring: auth=%q path=%q", gotAuth, gotPath)
		}
		if gotBody["max_tokens"] != float64(1) || gotBody["model"] != "deepseek-flash" {
			t.Fatalf("probe body: %v", gotBody)
		}
		// The provider body is never read back into the result.
		if strings.Contains(result.Detail, "SECRET-BODY") {
			t.Fatal("probe leaked the provider body")
		}
	}
}

func TestProbeHarnessAccountReportsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(200)
	}))
	defer server.Close()
	client := &http.Client{Timeout: 30 * time.Millisecond}
	result := probeHarnessAccount(context.Background(), server.URL, harnessTestAccount("main", "sk-timeout"), client)
	if result.Status != "timeout" {
		t.Fatalf("timeout status = %q", result.Status)
	}
}

func TestHarnessTestResultInvalidatesOnKeyOrModelChange(t *testing.T) {
	store := newHarnessTestStore(t.TempDir(), "deepseek")
	acc := harnessTestAccount("main", "key-one")
	stored := harnessTestResult{Status: "authenticated", At: 1, KeyHash: harnessKeyHash(acc.APIKey), Model: acc.Model}
	if err := store.put("personal/main", stored); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.get("personal/main", acc); !ok {
		t.Fatal("current key/model did not match its result")
	}
	rotated := acc
	rotated.APIKey = "key-two"
	if _, ok := store.get("personal/main", rotated); ok {
		t.Fatal("rotated key reused a stale test result")
	}
	reModeled := acc
	reModeled.Model = "other-model"
	if _, ok := store.get("personal/main", reModeled); ok {
		t.Fatal("edited model reused a stale test result")
	}
	info, err := os.Stat(store.path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("test store must be private", err)
	}
}

func TestHarnessTestDTOOmitsKeyHash(t *testing.T) {
	result := harnessTestResult{Status: "authenticated", KeyHash: "hash-secret", Model: "m"}
	b, _ := json.Marshal(result.DTO())
	if strings.Contains(string(b), "hash-secret") || strings.Contains(string(b), "keyHash") {
		t.Fatalf("DTO leaked the key hash: %s", b)
	}
}

func TestHarnessStatusLadderAndTestRouteNeedSelection(t *testing.T) {
	a := &apiServer{dataDir: t.TempDir()}
	mux := http.NewServeMux()
	a.registerHarnessRoutes(mux)
	a.registerExtensionRoutes(mux)

	// No accounts: not merely "installed".
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest("GET", "/api/harnesses/deepseek", nil))
	var payload map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["configured"] != false || payload["keyPresent"] != false || payload["authenticationStatus"] != "no-account" {
		t.Fatalf("empty ladder: %v", payload)
	}

	// Saving a key makes it present but NOT authenticated: only a Test can say that.
	body, _ := json.Marshal(harnessTestAccount("main", "sk-ladder-secret"))
	res = httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/harnesses/deepseek/accounts", strings.NewReader(string(body)))
	mux.ServeHTTP(res, req)
	if res.Code != 200 {
		t.Fatal(res.Code, res.Body.String())
	}
	res = httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest("GET", "/api/harnesses/deepseek", nil))
	payload = map[string]any{}
	_ = json.Unmarshal(res.Body.Bytes(), &payload)
	if payload["configured"] != true || payload["keyPresent"] != true || payload["authenticationStatus"] != "not-tested" {
		t.Fatalf("key-present ladder: %v", payload)
	}
	if strings.Contains(res.Body.String(), "sk-ladder-secret") || strings.Contains(res.Body.String(), "apiKey") {
		t.Fatal("ladder leaked the key")
	}

	// A current explicit Test verdict is surfaced; the key hash stays local.
	acc := harnessTestAccount("main", "sk-ladder-secret")
	if err := newHarnessTestStore(a.dataDir, "deepseek").put("personal/main", harnessTestResult{Status: "authenticated", At: 2, KeyHash: harnessKeyHash(acc.APIKey), Model: acc.Model}); err != nil {
		t.Fatal(err)
	}
	res = httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest("GET", "/api/harnesses/deepseek", nil))
	ladderBody := res.Body.String()
	if !strings.Contains(ladderBody, `"authenticationStatus":"authenticated"`) || !strings.Contains(ladderBody, `"lastTest"`) || strings.Contains(ladderBody, "keyHash") {
		t.Fatalf("tested ladder: %s", ladderBody)
	}

	// The Test route is a credential operation: local-only and selection-gated.
	res = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/harnesses/deepseek/test", nil)
	req.Header.Set("X-Forwarded-Prefix", "/m/machine-1")
	mux.ServeHTTP(res, req)
	if res.Code != 403 {
		t.Fatalf("relay test route = %d", res.Code)
	}
}

func TestProbeHarnessAuthClassifiesOutcomes(t *testing.T) {
	cases := []struct {
		code int
		want string
	}{
		{200, "authenticated"},
		{401, "auth-failed"},
		{402, "billing"},
		{429, "rate-limited"},
		{404, "unsupported"},
		{405, "unsupported"},
		{500, "unavailable"},
		{400, "error"},
	}
	for _, tc := range cases {
		var gotMethod, gotPath, gotAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
			w.WriteHeader(tc.code)
			_, _ = w.Write([]byte(`{"secret":"AUTH-BODY-DO-NOT-RETURN"}`))
		}))
		result := probeHarnessAuth(context.Background(), server.URL, harnessTestAccount("main", "sk-auth-secret"), server.Client())
		server.Close()
		if result.Status != tc.want || result.HTTPStatus != tc.code {
			t.Fatalf("HTTP %d: got %q/%d want %q", tc.code, result.Status, result.HTTPStatus, tc.want)
		}
		if gotMethod != http.MethodGet || gotPath != "/models" || gotAuth != "Bearer sk-auth-secret" {
			t.Fatalf("auth probe wiring: %s %s auth=%q", gotMethod, gotPath, gotAuth)
		}
		if strings.Contains(result.Detail, "AUTH-BODY") {
			t.Fatal("auth probe leaked the provider body")
		}
	}
}

func TestHarnessEndpointUsesProviderFacts(t *testing.T) {
	cases := []struct {
		provider, billing, want string
	}{
		{"deepseek", "api", "https://api.deepseek.com"},
		{"pi", "api", "https://api.deepseek.com"},
		{"glm", "api", "https://api.z.ai/api/paas/v4"},
		{"glm", "coding-plan", "https://api.z.ai/api/coding/paas/v4"},
		{"pi-glm", "coding-plan", "https://api.z.ai/api/coding/paas/v4"},
	}
	for _, tc := range cases {
		got, err := harnessEndpoint(tc.provider, tc.billing)
		if err != nil || got != tc.want {
			t.Fatalf("harnessEndpoint(%q,%q) = %q, %v; want %q", tc.provider, tc.billing, got, err, tc.want)
		}
	}
	for _, tc := range []struct{ provider, billing string }{
		{"deepseek", "coding-plan"}, {"claude", "api"}, {"glm", "other"}, {"nope", "api"},
		{"pi-glm", "api"}, {"pi", "coding-plan"},
	} {
		if _, err := harnessEndpoint(tc.provider, tc.billing); err == nil {
			t.Fatalf("harnessEndpoint(%q,%q) unexpectedly succeeded", tc.provider, tc.billing)
		}
	}
}
