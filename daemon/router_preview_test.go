package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The preview listener serves the backends' preview proxy routes and
// nothing else: a page on it reaches no app API through it.
func TestRouterPreviewHandlerServesOnlyPreviewRoutes(t *testing.T) {
	t.Parallel()
	var got string
	backend := func(name string) http.Handler {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { got = name })
	}
	h := routerPreviewHandler(backend("wsl"), backend("win"))
	for path, want := range map[string]string{
		"/b/wsl/api/proxy/5173~abc/":       "wsl",
		"/b/win/api/proxy/3000/src/app.js": "win",
		"/b/wsl/api/settings":              "",
		"/api/panes":                       "",
		"/":                                "",
		"/b/wsl/api/proxyx/1":              "",
	} {
		got = ""
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:41000"+path, nil))
		if got != want {
			t.Fatalf("%s reached %q, want %q", path, got, want)
		}
		if want == "" && w.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d, want 404", path, w.Code)
		}
	}
}

func TestIsRouterPreviewPath(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]bool{
		"/b/wsl/api/proxy/5173/": true,
		"/b/win/api/proxy/1/x":   true,
		"/api/proxy/5173/":       false,
		"/b/wsl/api/panes":       false,
		"/b/other/api/proxy/1/":  false,
	} {
		if got := isRouterPreviewPath(path); got != want {
			t.Fatalf("isRouterPreviewPath(%q) = %v, want %v", path, got, want)
		}
	}
}

// The app listener and the preview listener share a hostname, so the router
// requires the exact origin: a page on the preview port cannot use the app's.
func TestRouterGuardRequiresExactOrigin(t *testing.T) {
	t.Parallel()
	reached := false
	h := routerAppGuard(routerGuard(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })))
	cases := []struct {
		name, origin, site string
		want               bool
	}{
		{"app itself", "http://127.0.0.1:28787", "same-origin", true},
		{"no origin, same-origin fetch", "", "same-origin", true},
		{"preview port", "http://127.0.0.1:41000", "same-site", false},
		{"preview port, no fetch metadata", "http://127.0.0.1:41000", "", false},
		{"opaque", "null", "cross-site", false},
	}
	for _, c := range cases {
		reached = false
		r := httptest.NewRequest("GET", "http://127.0.0.1:28787/b/wsl/api/panes", nil)
		r.Host = "127.0.0.1:28787"
		r.Header.Set("Sec-Fetch-Mode", "cors")
		if c.site != "" {
			r.Header.Set("Sec-Fetch-Site", c.site)
		}
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
		if reached != c.want {
			t.Fatalf("%s: reached=%v, want %v", c.name, reached, c.want)
		}
	}
}

// The app frames previews from the preview listener: that same-site frame
// navigation must reach it.
func TestRouterPreviewListenerAcceptsFramingFromTheApp(t *testing.T) {
	t.Parallel()
	reached := false
	h := routerGuard(routerPreviewHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }), http.NotFoundHandler()))
	r := httptest.NewRequest("GET", "http://127.0.0.1:41000/b/wsl/api/proxy/5173~abc/", nil)
	r.Host = "127.0.0.1:41000"
	r.Header.Set("Sec-Fetch-Site", "same-site")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	r.Header.Set("Sec-Fetch-Dest", "iframe")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if !reached {
		t.Fatal("the app's frame navigation into the preview listener was refused")
	}
}
