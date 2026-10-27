package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewStaticServerProxiesAPIWhenConfigured(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	s := &previewStaticServer{}
	if err := s.setUpstream(upstream.URL); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/workflow/tasks", strings.NewReader(`{"a":1}`))
	s.serveAPI(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("proxy response = %d %q", rec.Code, rec.Body.String())
	}
	if gotMethod != http.MethodPost || gotPath != "/api/workflow/tasks" || gotBody != `{"a":1}` {
		t.Fatalf("upstream saw %s %s %q", gotMethod, gotPath, gotBody)
	}
}

func TestPreviewStaticServerAPIAbsentUpstreamStaysHonest(t *testing.T) {
	s := &previewStaticServer{}
	rec := httptest.NewRecorder()
	s.serveAPI(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "preview backend unavailable") {
		t.Fatalf("unconfigured API = %d %q", rec.Code, rec.Body.String())
	}
}

func TestPreviewStaticServerRejectsNonHTTPUpstream(t *testing.T) {
	s := &previewStaticServer{}
	if err := s.setUpstream("ftp://example.com"); err == nil {
		t.Fatal("non-http upstream accepted")
	}
}

func TestPreviewIsBackendPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/api", true},
		{"/api/health", true},
		{"/preview/abc123/api/health", true},
		{"/preview/abc123/api", true},
		{"/", false},
		{"/assets/app.js", false},
		{"/apian/thing", false},
	}
	for _, tc := range cases {
		if got := previewIsBackendPath(tc.path); got != tc.want {
			t.Errorf("previewIsBackendPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestPreviewMountedProxyReachesDaemonThroughAuthGate closes the browser
// acceptance defect where every /api call from a mounted static preview
// answered 403 "cross-origin request rejected". The exposure proxy rewrites the
// proxied request's Host to 127.0.0.1:<static-port> (so the request still
// authenticates as local), which left the browser's real Origin
// (http://<expose-host>:<port>) looking cross-origin to the daemon upstream.
// This drives the whole chain against the daemon's real authGate, not a bare
// stub, so the regression cannot hide behind a test-only upstream.
func TestPreviewMountedProxyReachesDaemonThroughAuthGate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok","live":true}`)
	})
	gate := &authGate{token: "acceptance-token"}
	daemon := httptest.NewServer(gate.middleware(mux))
	defer daemon.Close()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<!doctype html><title>app</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	static := &previewStaticServer{root: root, spa: true}
	if err := static.setUpstream(daemon.URL); err != nil {
		t.Fatal(err)
	}
	staticHTTP := httptest.NewServer(http.HandlerFunc(static.serveHTTP))
	defer staticHTTP.Close()
	port := uint16(staticHTTP.Listener.Addr().(*net.TCPAddr).Port)

	exposure := httptest.NewServer(previewMountedProxyHandler(port, "/preview/pv_acceptance"))
	defer exposure.Close()

	req, err := http.NewRequest(http.MethodGet, exposure.URL+"/preview/pv_acceptance/api/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	// These are the headers a real WebView2 makes when the app fetches /api/...
	// from the exposed preview origin.
	req.Header.Set("Origin", "http://172.22.149.245:39703")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"live":true`) {
		t.Fatalf("mounted preview /api/health = %d %q; the daemon origin gate refused its own preview proxy", resp.StatusCode, body)
	}
}

// TestPreviewMountedProxyReachesDaemonAPI exercises the full Go chain the
// acceptance browser uses: the exposure mount strips /preview/<id>, the static
// preview server recognizes an /api call (root or basePath-nested), and proxies
// it to the daemon upstream instead of answering "preview backend unavailable".
func TestPreviewMountedProxyReachesDaemonAPI(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"tasks":["real"],"path":"`+r.URL.Path+`"}`)
	}))
	defer upstream.Close()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<!doctype html><title>app</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	static := &previewStaticServer{root: root, spa: true}
	if err := static.setUpstream(upstream.URL); err != nil {
		t.Fatal(err)
	}
	staticHTTP := httptest.NewServer(http.HandlerFunc(static.serveHTTP))
	defer staticHTTP.Close()
	port := uint16(staticHTTP.Listener.Addr().(*net.TCPAddr).Port)

	for _, tc := range []struct {
		name     string
		prefix   string
		targetBs string
		apiPath  string
		wantPath string
	}{
		{"root mount", "/preview/pv_random", "", "/api/workflow/tasks", "/api/workflow/tasks"},
		{"basepath mount", "/preview/pv_random", "/preview/pv_placeholder", "/api/workflow/tasks", "/preview/pv_placeholder/api/workflow/tasks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := previewMountedProxyHandler(port, tc.prefix, tc.targetBs)
			mounted := httptest.NewServer(handler)
			defer mounted.Close()
			resp, err := http.Get(mounted.URL + tc.prefix + tc.apiPath)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"tasks":["real"]`) {
				t.Fatalf("status=%d body=%s", resp.StatusCode, body)
			}
			if !strings.Contains(string(body), tc.wantPath) {
				t.Fatalf("upstream saw wrong path: %s", body)
			}
		})
	}
}
