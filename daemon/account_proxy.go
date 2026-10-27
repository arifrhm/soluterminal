package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Account-scoped cloud resources reached from the desktop app. In a browser
// opened through the cloud, the page and these endpoints share an origin and
// the cloud answers them itself. The desktop app's page is served by this
// daemon, so without forwarding /api/user/prefs answered 404 and sidebar
// layout sync silently never ran on the desktop. The daemon forwards the
// signed-in user's own Supabase session token; it never sends its own token
// or the machine's credentials, and the cloud keys every row on the token's
// user.

const (
	accountProxyTimeout  = 10 * time.Second
	accountProxyMaxBody  = 256 << 10
	accountProxyMaxReply = 256 << 10
)

var accountProxyClient = &http.Client{Timeout: accountProxyTimeout}

// accountProxyBase is the cloud origin; tests point it at a fake.
var accountProxyBase = func() string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("SOLU_CLOUD_URL")), "/"); v != "" {
		return v
	}
	return defaultCloudHTTPBase
}

// accountProxyToken returns the caller's Supabase session token: the
// Authorization bearer the web app sends, or the X-Solu-Account-Token header.
// Only a JWT with a subject qualifies, so the daemon's own token is never
// forwarded.
func accountProxyToken(r *http.Request) string {
	for _, raw := range []string{
		strings.TrimPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer "),
		strings.TrimSpace(r.Header.Get(entitlementsAccountHdr)),
	} {
		raw = strings.TrimSpace(raw)
		if raw != "" && jwtSubject(raw) != "" {
			return raw
		}
	}
	return ""
}

// forwardAccountRequest relays one request to the same path on the cloud.
func (a *apiServer) forwardAccountRequest(w http.ResponseWriter, r *http.Request, cloudPath string) {
	if requestViaRelay(r) {
		// A relayed page talks to the cloud directly; nothing to forward here.
		http.Error(w, "not available through the relay", http.StatusForbidden)
		return
	}
	if accountProxyBase() == "" {
		http.Error(w, "no cloud is configured", http.StatusServiceUnavailable)
		return
	}
	token := accountProxyToken(r)
	if token == "" {
		http.Error(w, "sign in to use this", http.StatusUnauthorized)
		return
	}
	var body io.Reader
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, accountProxyMaxBody))
		if err != nil {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, accountProxyBase()+cloudPath, body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := accountProxyClient.Do(req)
	if err != nil {
		http.Error(w, "the cloud could not be reached", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	reply, _ := io.ReadAll(io.LimitReader(resp.Body, accountProxyMaxReply))
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(reply)
}

// handleUserPrefsProxy forwards sidebar layout sync. While "View as personal"
// is on, nothing syncs in either direction: the sidebar is read-only there,
// and the stranger's view must never be saved over the real arrangement.
func (a *apiServer) handleUserPrefsProxy(w http.ResponseWriter, r *http.Request) {
	if a.personalViewOn() {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]any{"prefs": map[string]any{}, "updated_at": nil, "paused": "personal_view"})
			return
		}
		http.Error(w, "layout sync is paused in personal view", http.StatusConflict)
		return
	}
	a.forwardAccountRequest(w, r, "/api/user/prefs")
}

// handleMarketingConsentProxy forwards the product-updates email choice.
func (a *apiServer) handleMarketingConsentProxy(w http.ResponseWriter, r *http.Request) {
	a.forwardAccountRequest(w, r, "/api/user/marketing-consent")
}

// handleAuthMethodProxy forwards the email-first sign-in lookup (which step to
// show: password or emailed code) to the cloud. It runs before sign-in, so it
// carries no token; the cloud answers only {"method": "password"|"code"} and
// rate-limits by IP and email. A failure answers "code", which works for every
// account, so sign-in never dead-ends on the desktop either.
func (a *apiServer) handleAuthMethodProxy(w http.ResponseWriter, r *http.Request) {
	codeAnswer := func() { writeJSON(w, http.StatusOK, map[string]string{"method": "code"}) }
	if requestViaRelay(r) {
		http.Error(w, "not available through the relay", http.StatusForbidden)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<10))
	if err != nil {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	if accountProxyBase() == "" {
		codeAnswer() // no cloud configured
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, accountProxyBase()+"/api/auth/method", bytes.NewReader(data))
	if err != nil {
		codeAnswer()
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := accountProxyClient.Do(req)
	if err != nil {
		codeAnswer()
		return
	}
	defer resp.Body.Close()
	reply, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	switch resp.StatusCode {
	case http.StatusOK, http.StatusBadRequest, http.StatusTooManyRequests:
		w.Header().Set("Content-Type", "application/json")
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			w.Header().Set("Retry-After", ra)
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(reply)
	default:
		// An older cloud without the endpoint (404) or a cloud error.
		codeAnswer()
	}
}
