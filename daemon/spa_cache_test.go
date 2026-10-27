package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSpaFileServerFallbackPreservesRequestAndLogPath(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	const shell = "<!doctype html><html>app shell</html>"
	h := logMiddleware(spaFileServer(http.FS(fstest.MapFS{
		"index.html": {Data: []byte(shell)},
	})))
	req := httptest.NewRequest(http.MethodGet, "/some/spa%2Froute?token=do-not-log", nil)
	originalURL := req.URL.String()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != shell {
		t.Fatalf("SPA fallback: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if req.URL.String() != originalURL {
		t.Fatalf("fallback mutated URL: got %q want %q", req.URL.String(), originalURL)
	}
	if !strings.Contains(logs.String(), "GET /some/spa/route 200 ") || strings.Contains(logs.String(), "do-not-log") {
		t.Fatalf("access log must retain the original path without its query: %q", logs.String())
	}
}

// TestSpaFileServerCacheHeaders locks the SPA cache policy that keeps a WebView2
// from serving a stale app shell after a daemon auto-update (the recurring "I'm
// not getting the update" bug): hashed assets are immutable; the shell (index +
// any SPA-fallback route) is no-store so a fresh index.html is fetched every load.
func TestSpaFileServerCacheHeaders(t *testing.T) {
	t.Parallel()
	fsys := http.FS(fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><html></html>")},
		"assets/App-abc123.js": {Data: []byte("console.log(1)")},
		"assets/index-d1f.css": {Data: []byte("body{}")},
		"webdist.json":         {Data: []byte(`{"webVersion":"1.2.3"}`)},
	})
	h := spaFileServer(fsys)

	cases := []struct {
		path      string
		wantCache string
	}{
		{"/", "no-store, must-revalidate"},
		{"/index.html", "no-store, must-revalidate"},
		{"/some/spa/route", "no-store, must-revalidate"}, // SPA fallback → shell policy
		{"/assets/App-abc123.js", "public, max-age=31536000, immutable"},
		{"/assets/index-d1f.css", "public, max-age=31536000, immutable"},
		{"/webdist.json", "no-store, must-revalidate"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		got := rec.Header().Get("Cache-Control")
		if got != c.wantCache {
			t.Errorf("%s: Cache-Control = %q, want %q", c.path, got, c.wantCache)
		}
	}
}

func TestSpaFileServerMissingProvenanceDoesNotFallBackToIndex(t *testing.T) {
	t.Parallel()
	h := spaFileServer(http.FS(fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><html></html>")},
	}))
	req := httptest.NewRequest(http.MethodGet, "/webdist.json", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /webdist.json status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store, must-revalidate" {
		t.Fatalf("GET /webdist.json Cache-Control = %q, want no-store", got)
	}
	if rec.Body.String() == "<!doctype html><html></html>" {
		t.Fatal("GET /webdist.json returned the SPA shell")
	}
}

// Unknown API paths are a real 404, never the SPA shell: clients polling a
// missing or method-mismatched endpoint must not see 200 and keep retrying.
func TestSpaFileServerUnknownAPIPathIsNotFound(t *testing.T) {
	t.Parallel()
	h := spaFileServer(http.FS(fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><html></html>")},
	}))
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/api/does-not-exist", http.StatusNotFound},
		{http.MethodGet, "/api", http.StatusNotFound},
		{http.MethodGet, "/api/aria/mcp/", http.StatusNotFound},
		{http.MethodDelete, "/api/aria/mcp/aria-comms", http.StatusNotFound},
		{http.MethodGet, "/apix/route", http.StatusOK}, // SPA route, not the API namespace
		{http.MethodGet, "/tasks/wft_1", http.StatusOK},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.want {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, rec.Code, c.want)
		}
	}
}
