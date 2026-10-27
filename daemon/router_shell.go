package main

// Which daemon the desktop router serves the app shell (index.html, assets,
// webdist.json) from. Normally the Windows daemon (see runRouter): both
// daemons embed the same released webdist, and Windows can't vanish the way
// WSL can. A Preview build (scripts/follow-main.sh, owner-only) replaces only
// the WSL daemon, though, so with the shell on Windows the owner never saw a
// Preview's UI before publishing. While WSL reports build channel "preview",
// the shell comes from WSL instead, with Windows as the fallback whenever WSL
// errors or lacks an asset. Everyone else is unchanged.

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

const routerShellProbeEvery = 5 * time.Second

// var for tests.
var (
	routerShellDialTimeout     = 2 * time.Second
	routerShellResponseTimeout = 4 * time.Second
)

type routerShellSource struct {
	healthURL string
	token     string
	client    *http.Client
	preview   atomic.Bool
}

func newRouterShellSource(wsl *url.URL, token string) *routerShellSource {
	u := *wsl
	u.Path = "/api/health"
	return &routerShellSource{healthURL: u.String(), token: token, client: &http.Client{Timeout: 2 * time.Second}}
}

// probe reads the WSL daemon's build channel. Any failure means "not a
// Preview": the shell then stays on Windows, the safe default.
func (s *routerShellSource) probe() {
	preview := false
	if req, err := http.NewRequest(http.MethodGet, s.healthURL, nil); err == nil {
		req.Header.Set("Authorization", "Bearer "+s.token)
		if resp, err := s.client.Do(req); err == nil {
			var health struct {
				Build struct {
					Channel string `json:"channel"`
				} `json:"build"`
			}
			if resp.StatusCode == http.StatusOK {
				_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&health)
			}
			resp.Body.Close()
			preview = health.Build.Channel == "preview"
		}
	}
	if s.preview.Swap(preview) != preview {
		from := "Windows"
		if preview {
			from = "WSL (a Preview build)"
		}
		log.Printf("router: app shell now served from %s", from)
	}
}

func (s *routerShellSource) run() {
	for {
		s.probe()
		time.Sleep(routerShellProbeEvery)
	}
}

var errRouterShellFallback = errors.New("router: falling back to the Windows shell")

// newPreviewShellProxy serves the shell from WSL, handing the request to
// fallback (the Windows shell) when WSL can't be reached, fails, or answers
// an asset with its single-page fallback HTML or a 404 — a page whose index
// came from one daemon and an asset from the other is the case that matters.
func newPreviewShellProxy(wsl *url.URL, token string, fallback http.Handler) *httputil.ReverseProxy {
	p := newPrefixProxy(wsl, "", token)
	// A WSL that accepts the connection but never answers must not hang the
	// window: the shell is small and local, so a few seconds is plenty, and
	// a timeout goes to the Windows fallback like any other error.
	p.Transport = &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: routerShellDialTimeout}).DialContext,
		ResponseHeaderTimeout: routerShellResponseTimeout,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
	}
	p.ModifyResponse = func(resp *http.Response) error {
		if resp.StatusCode >= 500 {
			return errRouterShellFallback
		}
		if strings.HasPrefix(resp.Request.URL.Path, "/assets/") &&
			(resp.StatusCode == http.StatusNotFound || strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html")) {
			return errRouterShellFallback
		}
		return nil
	}
	p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, _ error) {
		fallback.ServeHTTP(w, r)
	}
	return p
}

// routerShellHandler serves the shell from WSL while it runs a Preview build,
// otherwise from Windows.
func routerShellHandler(src *routerShellSource, previewShell, windowsShell http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if src.preview.Load() {
			previewShell.ServeHTTP(w, r)
			return
		}
		windowsShell.ServeHTTP(w, r)
	})
}
