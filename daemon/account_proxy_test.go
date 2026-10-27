package main

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeAccountJWT(sub string) string {
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString([]byte(`{"sub":"`+sub+`"}`)) + ".sig"
}

func withAccountCloud(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	prev := accountProxyBase
	accountProxyBase = func() string { return srv.URL }
	t.Cleanup(func() { accountProxyBase = prev })
}

func TestUserPrefsProxyForwardsTheUsersOwnToken(t *testing.T) {
	token := fakeAccountJWT("user-1")
	var gotAuth, gotBody, gotPath string
	withAccountCloud(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"prefs":{"sidebar":["a"]},"updated_at":"2026-10-23T00:00:00Z"}`))
	})
	a := &apiServer{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/user/prefs", strings.NewReader(`{"prefs":{"sidebar":["a"]}}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	a.handleUserPrefsProxy(rec, req)
	if rec.Code != 200 || gotAuth != "Bearer "+token || gotPath != "/api/user/prefs" || !strings.Contains(gotBody, "sidebar") {
		t.Fatalf("code=%d auth=%q path=%q body=%q", rec.Code, gotAuth, gotPath, gotBody)
	}
	if !strings.Contains(rec.Body.String(), "updated_at") {
		t.Fatalf("reply not passed through: %s", rec.Body.String())
	}
}

func TestUserPrefsProxyNeverForwardsTheDaemonToken(t *testing.T) {
	called := false
	withAccountCloud(t, func(w http.ResponseWriter, r *http.Request) { called = true })
	a := &apiServer{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/user/prefs", nil)
	req.Header.Set("Authorization", "Bearer daemon-local-token")
	a.handleUserPrefsProxy(rec, req)
	if rec.Code != http.StatusUnauthorized || called {
		t.Fatalf("non-account token: code=%d called=%v", rec.Code, called)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/user/prefs", nil)
	req.Header.Set("Authorization", "Bearer "+fakeAccountJWT("u"))
	req.Header.Set("X-Forwarded-Prefix", "/m/machine-1")
	a.handleUserPrefsProxy(rec, req)
	if rec.Code != http.StatusForbidden || called {
		t.Fatalf("relayed request: code=%d called=%v", rec.Code, called)
	}
}

func TestUserPrefsSyncPausesInPersonalView(t *testing.T) {
	called := false
	withAccountCloud(t, func(w http.ResponseWriter, r *http.Request) { called = true })
	a := operatorAPIServer(t)
	if _, err := a.view.set(true); err != nil {
		t.Fatal(err)
	}
	token := fakeAccountJWT("user-1")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/user/prefs", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	a.handleUserPrefsProxy(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"paused":"personal_view"`) || !strings.Contains(rec.Body.String(), `"updated_at":null`) {
		t.Fatalf("GET in personal view: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/user/prefs", strings.NewReader(`{"prefs":{}}`))
	req.Header.Set("Authorization", "Bearer "+token)
	a.handleUserPrefsProxy(rec, req)
	if rec.Code != http.StatusConflict || called {
		t.Fatalf("PUT in personal view: code=%d called=%v", rec.Code, called)
	}
	// Leaving the view resumes forwarding.
	if _, err := a.view.set(false); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/user/prefs", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	a.handleUserPrefsProxy(rec, req)
	if !called {
		t.Fatal("sync did not resume after the personal view")
	}
}

func TestMarketingConsentProxyForwards(t *testing.T) {
	var gotPath, gotMethod string
	withAccountCloud(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_, _ = w.Write([]byte(`{"consent":true}`))
	})
	a := &apiServer{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/user/marketing-consent", strings.NewReader(`{"consent":true,"source":"app-signup"}`))
	req.Header.Set("Authorization", "Bearer "+fakeAccountJWT("u"))
	a.handleMarketingConsentProxy(rec, req)
	if rec.Code != 200 || gotPath != "/api/user/marketing-consent" || gotMethod != http.MethodPost {
		t.Fatalf("code=%d path=%q method=%q", rec.Code, gotPath, gotMethod)
	}
}

func TestAuthMethodProxyForwardsWithoutAnyToken(t *testing.T) {
	var gotAuth, gotBody, gotPath string
	withAccountCloud(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"method":"password"}`))
	})
	a := &apiServer{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/method", strings.NewReader(`{"email":"pw@example.com"}`))
	req.Header.Set("Authorization", "Bearer daemon-token")
	a.handleAuthMethodProxy(rec, req)
	if rec.Code != 200 || gotAuth != "" || gotPath != "/api/auth/method" || !strings.Contains(gotBody, "pw@example.com") {
		t.Fatalf("code=%d auth=%q path=%q body=%q", rec.Code, gotAuth, gotPath, gotBody)
	}
	if !strings.Contains(rec.Body.String(), `"password"`) {
		t.Fatalf("reply not passed through: %s", rec.Body.String())
	}
}

func TestAuthMethodProxyFallsBackToCode(t *testing.T) {
	withAccountCloud(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r) // an older cloud without the endpoint
	})
	a := &apiServer{}
	rec := httptest.NewRecorder()
	a.handleAuthMethodProxy(rec, httptest.NewRequest(http.MethodPost, "/api/auth/method", strings.NewReader(`{"email":"x@example.com"}`)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"code"`) {
		t.Fatalf("fallback: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAuthMethodProxyPassesRateLimitThrough(t *testing.T) {
	withAccountCloud(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate_limited"}`))
	})
	a := &apiServer{}
	rec := httptest.NewRecorder()
	a.handleAuthMethodProxy(rec, httptest.NewRequest(http.MethodPost, "/api/auth/method", strings.NewReader(`{"email":"x@example.com"}`)))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "600" {
		t.Fatalf("rate limit: code=%d retry=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
}
