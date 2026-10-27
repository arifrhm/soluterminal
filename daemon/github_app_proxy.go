package main

import (
	"io"
	"net/http"
	"time"
)

// GitHub App "Connect GitHub" proxy — the daemon-side leg of the self-serve
// agent onboarding flow (cloud/github_app.go). The web UI only ever talks to
// the daemon; these handlers forward to the cloud with the daemon's machine
// token (authMachineOrUser on the cloud resolves it to the owning user, which
// is exactly the identity the install state should be signed for). The
// browser never sees the machine token, and the daemon never sees a GitHub
// credential — installation tokens live and die inside the cloud.
//
// No cloud paired ⇒ status returns the contract's dark shape
// ({configured:false, connected:false}) with 200 so the web hides the feature
// instead of erroring; the mutating routes return 503.

var githubAppProxyClient = &http.Client{Timeout: 15 * time.Second}

func (a *apiServer) handleGithubAppStatus(w http.ResponseWriter, r *http.Request) {
	base, token := a.cloudCreds()
	if base == "" || token == "" {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false, "connected": false})
		return
	}
	a.proxyGithubApp(w, r, "GET", base+"/api/github/app/status", token)
}

func (a *apiServer) handleGithubAppInstallURL(w http.ResponseWriter, r *http.Request) {
	base, token := a.cloudCreds()
	if base == "" || token == "" {
		http.Error(w, "cloud not connected", http.StatusServiceUnavailable)
		return
	}
	a.proxyGithubApp(w, r, "GET", base+"/api/github/app/install", token)
}

func (a *apiServer) handleGithubAppClaim(w http.ResponseWriter, r *http.Request) {
	base, token := a.cloudCreds()
	if base == "" || token == "" {
		http.Error(w, "cloud not connected", http.StatusServiceUnavailable)
		return
	}
	a.proxyGithubApp(w, r, "POST", base+"/api/github/app/claim", token)
}

func (a *apiServer) handleGithubAppDisconnect(w http.ResponseWriter, r *http.Request) {
	base, token := a.cloudCreds()
	if base == "" || token == "" {
		http.Error(w, "cloud not connected", http.StatusServiceUnavailable)
		return
	}
	a.proxyGithubApp(w, r, "DELETE", base+"/api/github/app", token)
}

// handleGithubAppPRReview is the babysitter's write path (phase 3): the
// agent POSTs its finished review to the LOOPBACK daemon, which forwards it
// with the machine token; the cloud posts to GitHub as soluterminal[bot].
// The body carries a full review comment, so this one route gets a larger
// bound than the rest (the cloud enforces its own 60k cap).
//
// The machine token alone does not authorize a post: any local process can
// reach this loopback route, so the cloud also requires the PR agent's own
// key, which the agent sends as X-Solu-PR-Agent-Key and this proxy forwards
// untouched (cloud/github_app_review.go).
func (a *apiServer) handleGithubAppPRReview(w http.ResponseWriter, r *http.Request) {
	base, token := a.cloudCreds()
	if base == "" || token == "" {
		http.Error(w, "cloud not connected", http.StatusServiceUnavailable)
		return
	}
	a.proxyGithubAppBounded(w, r, "POST", base+"/api/github/app/pr-review", token, 70_000, githubAppPRAgentKeyHeader)
}

// githubAppPRAgentKeyHeader is the PR agent's dedicated credential, forwarded
// on the pr-review route only.
const githubAppPRAgentKeyHeader = "X-Solu-PR-Agent-Key"

// proxyGithubApp forwards one request to the cloud and relays the JSON answer
// verbatim. Bodies are tiny by contract (a claim is one integer); bound both
// directions anyway.
func (a *apiServer) proxyGithubApp(w http.ResponseWriter, r *http.Request, method, url, token string) {
	a.proxyGithubAppBounded(w, r, method, url, token, 4096)
}

// proxyGithubAppBounded forwards with the machine token, the Content-Type and
// only the named extra request headers.
func (a *apiServer) proxyGithubAppBounded(w http.ResponseWriter, r *http.Request, method, url, token string, maxBody int64, forwardHeaders ...string) {
	req, err := http.NewRequestWithContext(r.Context(), method, url, io.LimitReader(r.Body, maxBody))
	if err != nil {
		http.Error(w, "build request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	for _, h := range forwardHeaders {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	resp, err := githubAppProxyClient.Do(req)
	if err != nil {
		http.Error(w, "cloud unreachable", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		http.Error(w, "cloud read failed", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}
