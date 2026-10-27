package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The pr-review proxy must forward the PR agent's dedicated key (the cloud
// refuses a bot post on the machine token alone) and must not leak it, or any
// other caller header, onto the other GitHub App routes.
func TestGithubAppProxyForwardsPRAgentKeyOnlyOnReviewRoute(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]http.Header{}
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		seen[r.URL.Path] = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"posted":"comment"}`))
	}))
	defer cloud.Close()
	a := &apiServer{cloudBase: cloud.URL, cloudToken: "machine-tok"}

	send := func(h http.HandlerFunc, path string) {
		req := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(githubAppPRAgentKeyHeader, "agent-secret")
		req.Header.Set("X-Other", "nope")
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	send(a.handleGithubAppPRReview, "/api/github/app/pr-review")
	send(a.handleGithubAppClaim, "/api/github/app/claim")

	mu.Lock()
	defer mu.Unlock()
	review, claim := seen["/api/github/app/pr-review"], seen["/api/github/app/claim"]
	if review == nil || claim == nil {
		t.Fatalf("cloud saw %v", seen)
	}
	if got := review.Get(githubAppPRAgentKeyHeader); got != "agent-secret" {
		t.Fatalf("pr-review forwarded key %q", got)
	}
	if review.Get("Authorization") != "Bearer machine-tok" || review.Get("X-Other") != "" {
		t.Fatalf("pr-review headers: %v", review)
	}
	if claim.Get(githubAppPRAgentKeyHeader) != "" || claim.Get("X-Other") != "" {
		t.Fatalf("claim leaked caller headers: %v", claim)
	}
}
