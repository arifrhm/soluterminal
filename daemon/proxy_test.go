package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Every proxied response is sandboxed, on every route: the proxy is served
// from the app's own origin, so an unsandboxed page (or SVG) from a pane's
// server would run as the app and its requests would pass as the person's.
func TestApplyProxyDocumentSandboxEverywhere(t *testing.T) {
	t.Parallel()
	for _, ct := range []string{"text/html; charset=utf-8", "image/svg+xml", "application/xhtml+xml", "application/javascript"} {
		resp := &http.Response{Header: make(http.Header)}
		resp.Header.Set("Content-Type", ct)
		resp.Header.Set("Content-Security-Policy", "default-src 'self'")
		applyProxyDocumentSandbox(resp)
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.HasPrefix(csp, "sandbox") {
			t.Fatalf("%s: Content-Security-Policy = %q, want sandbox directive", ct, csp)
		}
		for _, token := range []string{"allow-scripts", "allow-forms", "allow-popups"} {
			if !strings.Contains(csp, token) {
				t.Fatalf("%s: Content-Security-Policy = %q, missing %s", ct, csp, token)
			}
		}
		if strings.Contains(csp, "allow-same-origin") {
			t.Fatalf("%s: Content-Security-Policy = %q, must not allow same-origin", ct, csp)
		}
	}
}

func TestProxyRouteSandboxedExceptDesktopRouter(t *testing.T) {
	t.Parallel()
	for prefix, want := range map[string]bool{"": true, "/m/mach_1": true, "/m/mach_1/b/wsl": true, "/b/wsl": false, "/b/win": false} {
		if got := proxyRouteSandboxed(prefix); got != want {
			t.Fatalf("proxyRouteSandboxed(%q) = %v, want %v", prefix, got, want)
		}
	}
}

func TestApplyProxyCORSOnlyOnCloudRoutes(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"", "/b/wsl"} {
		h := make(http.Header)
		applyProxyCORS(h, prefix)
		if got := h.Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("%q Access-Control-Allow-Origin = %q, want none", prefix, got)
		}
	}
}

func TestApplyCloudProxyCORSForSandboxedCloudDocuments(t *testing.T) {
	t.Parallel()
	h := make(http.Header)

	applyProxyCORS(h, "/m/mach_1")

	if got := h.Get("Access-Control-Allow-Origin"); got != "null" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want null", got)
	}
	if got := h.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("Access-Control-Allow-Credentials = %q, want true", got)
	}
	if got := h.Get("Access-Control-Allow-Private-Network"); got != "true" {
		t.Fatalf("Access-Control-Allow-Private-Network = %q, want true", got)
	}
}

func TestHandleCloudProxyPreflight(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodOptions, "http://daemon.test/api/proxy/5173/api", nil)
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type, x-dev")
	rec := httptest.NewRecorder()

	if !handleCloudProxyPreflight(rec, req, "/m/mach_1") {
		t.Fatal("handleCloudProxyPreflight returned false for cloud preflight")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "content-type, x-dev" {
		t.Fatalf("Access-Control-Allow-Headers = %q", got)
	}
}

func TestRewriteProxyPortRequestCloudDropsCredentials(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "http://cloud.test/m/mach_1/api/proxy/5173/path?q=1&access_token=cloud-jwt", nil)
	req.Header.Set("Authorization", "Bearer cloud")
	req.Header.Set("Cookie", "solu_session=cloud; pref=1")
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	req.Header.Set("X-Forwarded-Host", "cloud.test")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Prefix", "/m/mach_1")
	target := &url.URL{Scheme: "http", Host: "127.0.0.1:5173"}

	rewriteProxyPortRequest(req, target, "/path", "/m/mach_1")

	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization forwarded to cloud proxied dev server as %q", got)
	}
	if got := req.Header.Get("Cookie"); got != "" {
		t.Fatalf("Cookie forwarded to cloud proxied dev server as %q", got)
	}
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Prefix"} {
		if got := req.Header.Get(h); got != "" {
			t.Fatalf("%s forwarded upstream as %q", h, got)
		}
	}
	if req.URL.Scheme != "http" || req.URL.Host != "127.0.0.1:5173" || req.Host != "127.0.0.1:5173" {
		t.Fatalf("target = scheme %q url host %q request host %q", req.URL.Scheme, req.URL.Host, req.Host)
	}
	if got := req.URL.Path; got != "/path" {
		t.Fatalf("path = %q, want /path", got)
	}
	if got := req.URL.RawQuery; got != "q=1" {
		t.Fatalf("RawQuery = %q, want q=1", got)
	}
}

func TestRewriteProxyPortRequestLocalPreservesCredentials(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "http://daemon.test/api/proxy/5173/path", nil)
	req.Header.Set("Authorization", "Bearer local")
	req.Header.Set("Cookie", "dev=local")
	target := &url.URL{Scheme: "http", Host: "127.0.0.1:5173"}

	rewriteProxyPortRequest(req, target, "/path", "")

	if got := req.Header.Get("Authorization"); got != "Bearer local" {
		t.Fatalf("Authorization upstream = %q, want preserved local credential", got)
	}
	if got := req.Header.Get("Cookie"); got != "dev=local" {
		t.Fatalf("Cookie upstream = %q, want preserved local cookie", got)
	}
}

func TestRewriteHTMLBodyUsesCloudProxyBase(t *testing.T) {
	t.Parallel()
	resp := &http.Response{
		Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			`<html><head></head><body><script src="/app.js"></script><form action="/submit"></form></body></html>`,
		)),
	}

	if err := rewriteHTMLBody(resp, "/m/mach_1/api/proxy/5173", "cloud-jwt"); err != nil {
		t.Fatalf("rewriteHTMLBody returned error: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	for _, want := range []string{
		`src="/m/mach_1/api/proxy/5173/app.js?access_token=cloud-jwt"`,
		`action="/m/mach_1/api/proxy/5173/submit?access_token=cloud-jwt"`,
		`var BASE = "/m/mach_1/api/proxy/5173"`,
		`ACCESS_TOKEN = new URLSearchParams(window.location.search).get("access_token") || ""`,
		`parsed.searchParams.set("access_token", ACCESS_TOKEN)`,
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("rewritten body missing %q: %s", want, string(body))
		}
	}
}

func TestRewriteLocationKeepsCloudPrefixAndToken(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "http://daemon.test/api/proxy/5173/start?access_token=cloud-jwt", nil)
	req.Header.Set("X-Forwarded-Prefix", "/m/mach_1/b/wsl")

	got := rewriteLocation("http://localhost:5173/login?next=%2F#top", "5173", req)
	want := "/m/mach_1/b/wsl/api/proxy/5173/login?access_token=cloud-jwt&next=%2F#top"
	if got != want {
		t.Fatalf("rewriteLocation = %q, want %q", got, want)
	}
}
