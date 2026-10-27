package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func webTestPort(t *testing.T, raw string) *Port {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(port)
	return &Port{Port: uint16(n), Addr: host, Proto: "tcp", Pid: 123, PaneID: "pane", FirstAt: time.Now().UTC(), Scheme: u.Scheme}
}

func TestPortWebProbeHTTPAndSelfSignedHTTPS(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(strconv.FormatBool(secure), func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodHead {
					t.Errorf("probe method %s", r.Method)
				}
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("credentials sent to service")
				}
				w.WriteHeader(http.StatusUnauthorized)
			})
			var server *httptest.Server
			if secure {
				server = httptest.NewTLSServer(handler)
			} else {
				server = httptest.NewServer(handler)
			}
			defer server.Close()
			p := webTestPort(t, server.URL)
			p.Scheme = ""
			want := "http"
			if secure {
				want = "https"
			}
			if got := probePortWeb(context.Background(), p); got != want {
				t.Fatalf("scheme=%q want %q", got, want)
			}
		})
	}
}

func TestPortWebProbeDoesNotFollowRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://unrelated.invalid/", http.StatusFound)
	}))
	defer server.Close()
	if got := probePortWeb(context.Background(), webTestPort(t, server.URL)); got != "http" {
		t.Fatalf("got %q", got)
	}
}

func TestPortRegistryStableListAndIsolatedCopies(t *testing.T) {
	pr := NewPortRegistry(nil)
	pr.seen = map[string]*Port{"b": {Port: 7000}, "a": {Port: 3000}}
	for i := 0; i < 20; i++ {
		got := pr.List()
		if got[0].Port != 3000 {
			t.Fatal("unstable order")
		}
		got[0].Process = "mutated"
	}
	if pr.seen["a"].Process != "" {
		t.Fatal("list exposed mutable registry data")
	}
}

func TestPortNumericPreviewRejectsAmbiguousOwners(t *testing.T) {
	pr := NewPortRegistry(nil)
	pr.seen = map[string]*Port{"a": {Port: 5173, Addr: "127.0.0.1", Pid: 1, PaneID: "one"}, "b": {Port: 5173, Addr: "::1", Pid: 2, PaneID: "two"}}
	if pr.resolve(5173, "") != nil {
		t.Fatal("ambiguous numeric preview selected an owner")
	}
	if p := pr.resolve(5173, portSelector(pr.seen["b"])); p == nil || p.Pid != 2 {
		t.Fatal("exact owner not selected")
	}
}

func TestPortPreviewUsesExactListenerAndRejectsReplacement(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "selected listener:"+r.URL.Path)
	}))
	defer server.Close()
	p := webTestPort(t, server.URL)
	pr := NewPortRegistry(nil)
	pr.seen = map[string]*Port{"socket": p}
	a := &apiServer{ports: pr}
	path := "/api/proxy/" + strconv.Itoa(int(p.Port)) + "~" + portSelector(p) + "/app"
	r := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	a.proxyPort(w, r)
	if w.Code != 200 || w.Body.String() != "selected listener:/app" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	cp := *p
	cp.Pid++
	pr.seen["socket"] = &cp
	w = httptest.NewRecorder()
	a.proxyPort(w, r)
	if w.Code != 403 {
		t.Fatalf("old selector accepted replacement: %d", w.Code)
	}
	cp.PaneID = ""
	pr.seen["socket"] = &cp
	w = httptest.NewRecorder()
	a.proxyPort(w, httptest.NewRequest(http.MethodGet, "/api/proxy/"+strconv.Itoa(int(p.Port))+"~"+portSelector(&cp)+"/", nil))
	if w.Code != 403 {
		t.Fatal("unowned listener was proxied")
	}
}

func TestPortPreviewRedirectPreservesIdentity(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/proxy/5173~YWJj/", nil)
	r.Header.Set("X-Forwarded-Prefix", "/m/host/b/win")
	got := rewriteLocation("http://localhost:5173/login?x=1#top", "5173~YWJj", r)
	if got != "/m/host/b/win/api/proxy/5173~YWJj/login?x=1#top" {
		t.Fatal(got)
	}
}

func TestPortDesktopRouterPreservesBackendAndStripsDaemonCredential(t *testing.T) {
	for _, prefix := range []string{"/b/win", ""} {
		t.Run("prefix="+prefix, func(t *testing.T) {

			credentials := make(chan [2]string, 1)
			service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				credentials <- [2]string{r.Header.Get("Authorization"), r.Header.Get("Cookie")}
				w.Header().Set("Content-Type", "text/html")
				io.WriteString(w, `<html><head><link href="/style.css"></head><body><script src="/app.js"></script></body></html>`)
			}))
			defer service.Close()
			p := webTestPort(t, service.URL)
			pr := NewPortRegistry(nil)
			pr.seen = map[string]*Port{"service": p}
			api := &apiServer{ports: pr}
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer daemon-secret" {
					t.Error("router did not authenticate to daemon")
				}
				api.proxyPort(w, r)
			}))
			defer backend.Close()
			upstream, _ := url.Parse(backend.URL)
			router := httptest.NewServer(newPrefixProxy(upstream, prefix, "daemon-secret"))
			defer router.Close()
			path := prefix + "/api/proxy/" + strconv.Itoa(int(p.Port)) + "~" + portSelector(p)
			req, _ := http.NewRequest(http.MethodGet, router.URL+path+"/", nil)
			req.Header.Set("Cookie", "solu_session=daemon-secret; dev=session")
			req.Header.Set("X-Forwarded-Prefix", "/b/wsl")
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, _ := io.ReadAll(response.Body)
			if !strings.Contains(string(body), path+"/app.js") || !strings.Contains(string(body), path+"/style.css") {
				t.Fatalf("backend lost in assets: %s", body)
			}
			observed := <-credentials
			auth, cookies := observed[0], observed[1]
			if auth != "" || strings.Contains(cookies, "solu_session") || cookies != "dev=session" {
				t.Fatalf("upstream credential isolation: auth=%q cookies=%q", auth, cookies)
			}

		})
	}
}

func TestPortNotificationsWaitForWebReadiness(t *testing.T) {
	events := make(chan Port, 8)
	pr := NewPortRegistry(nil)
	pr.onPaneServerUp = func(p Port) { events <- p }
	pr.commitLocked(map[string]*Port{})
	p := &Port{Port: 5173, PaneID: "pane", FirstAt: time.Now(), Pid: 1, Cmdline: "node vite"}
	pr.commitLocked(map[string]*Port{"x": p})
	select {
	case <-events:
		t.Fatal("TCP listener announced as web service")
	default:
	}
	// Readiness alone is insufficient: automatic hints require a known preview
	// tool that has survived the stability interval. Advance that interval
	// without introducing a five-second wall-clock sleep into this regression.
	notice := pr.notices["pane|5173"]
	notice.since = time.Now().Add(-5 * time.Second)
	pr.notices["pane|5173"] = notice
	ready := *p
	ready.Scheme = "http"
	ready.WebStatus = "ready"
	pr.commitLocked(map[string]*Port{"x": &ready})
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("ready service not announced")
	}
	pr.commitLocked(map[string]*Port{"x": &ready})
	select {
	case <-events:
		t.Fatal("unchanged service announced twice")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestPortProbeSkipsInternalTools(t *testing.T) {
	pr := NewPortRegistry(nil)
	p := &Port{Process: "codex", Port: 9999}
	pr.commitScan(map[string]*Port{"x": p})
	if p.WebStatus != "internal" || p.Scheme != "" {
		t.Fatalf("%+v", p)
	}
	if !strings.HasPrefix(p.PreviewPath, "/api/proxy/9999~") {
		t.Fatal(p.PreviewPath)
	}
}

func TestPortDiscoveryLeavesUnownedServicesUntouchedUntilExplicitCheck(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	p := webTestPort(t, server.URL)
	p.PaneID = ""
	p.Scheme = ""
	p.Process = "node"
	pr := NewPortRegistry(nil)
	pr.commitScan(map[string]*Port{"socket": p})
	if requests.Load() != 0 || p.WebStatus != "unverified" {
		t.Fatal("discovery sent traffic to an unrelated listener")
	}
	if _, err := pr.Probe(context.Background(), p.Port, "outdated"); err == nil {
		t.Fatal("stale check accepted")
	}
	if requests.Load() != 0 {
		t.Fatal("stale check sent traffic")
	}
	checked, err := pr.Probe(context.Background(), p.Port, portSelector(p))
	if err != nil || checked.WebStatus != "ready" || checked.Scheme != "http" || requests.Load() != 1 {
		t.Fatalf("explicit check: %+v %v requests=%d", checked, err, requests.Load())
	}
	if pr.Has(p.Port) {
		t.Fatal("explicit HTTP check granted proxy ownership")
	}
}

func TestPortSelectorSurvivesDaemonRestart(t *testing.T) {
	p := &Port{Port: 5173, Proto: "tcp", Addr: "127.0.0.1", Pid: 123, identity: "kernel-socket-42", FirstAt: time.Now()}
	restarted := *p
	restarted.FirstAt = restarted.FirstAt.Add(time.Hour)
	if portSelector(p) != portSelector(&restarted) {
		t.Fatal("same live socket changed identity across daemon restart")
	}
	restarted.identity = "kernel-socket-43"
	if portSelector(p) == portSelector(&restarted) {
		t.Fatal("replacement socket retained identity")
	}
}

func TestPortAutomaticReadinessStopsAfterBoundedStartupChecks(t *testing.T) {
	var attempts atomic.Int32
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			attempts.Add(1)
			connection.Close()
		}
	}()
	p := webTestPort(t, "http://"+listener.Addr().String())
	p.Process = "unknown-runtime"
	p.Scheme = ""
	pr := NewPortRegistry(nil)
	pr.commitScan(map[string]*Port{"socket": p})
	for i := 0; i < 12; i++ {
		cp := *pr.List()[0]
		cp.checkedAt = time.Now().Add(-time.Minute)
		pr.commitScan(map[string]*Port{"socket": &cp})
	}
	if pr.List()[0].probeAttempts != 3 || attempts.Load() != 3 {
		t.Fatalf("startup probing was not bounded: attempts=%d connections=%d", pr.List()[0].probeAttempts, attempts.Load())
	}
}

func TestPortSuccessfulReadinessIsCachedForListenerLifetime(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(200) }))
	defer server.Close()
	p := webTestPort(t, server.URL)
	pr := NewPortRegistry(nil)
	pr.commitScan(map[string]*Port{"socket": p})
	for i := 0; i < 4; i++ {
		cp := *pr.List()[0]
		cp.checkedAt = time.Now().Add(-time.Hour)
		pr.commitScan(map[string]*Port{"socket": &cp})
	}
	if requests.Load() != 1 {
		t.Fatalf("confirmed service was polled %d times", requests.Load())
	}
}

func TestPortExplicitCheckHonorsCancellationWhileScanIsBusy(t *testing.T) {
	pr := NewPortRegistry(nil)
	pr.scanMu.Lock()
	defer pr.scanMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := pr.Probe(ctx, 5173, "selector")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("busy check ignored cancellation: %v", err)
	}
}

func TestPortExplicitCheckAllowsSlowStartup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(500 * time.Millisecond); w.WriteHeader(200) }))
	defer server.Close()
	p := webTestPort(t, server.URL)
	p.PaneID = ""
	p.Scheme = ""
	pr := NewPortRegistry(nil)
	pr.seen = map[string]*Port{"socket": p}
	checked, err := pr.Probe(context.Background(), p.Port, portSelector(p))
	if err != nil || checked.WebStatus != "ready" {
		t.Fatalf("slow startup rejected: %+v %v", checked, err)
	}
}

func TestPortDiscoveryFailureWithdrawsProxyAuthorization(t *testing.T) {
	pr := NewPortRegistry(nil)
	pr.seen = map[string]*Port{"socket": {Port: 5173, PaneID: "pane"}}
	pr.invalidate("cannot verify current user")
	if pr.Has(5173) || len(pr.List()) != 0 || pr.Warning() == "" {
		t.Fatal("discovery failure left stale authorization")
	}
}

func TestPortProxyReusesConnectionsOnlyWithinListenerLifetime(t *testing.T) {
	var connections atomic.Int32
	service := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("asset")) }))
	service.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	service.Start()
	defer service.Close()
	p := webTestPort(t, service.URL)
	pr := NewPortRegistry(nil)
	pr.seen = map[string]*Port{"socket": p}
	api := &apiServer{ports: pr}
	proxy := httptest.NewServer(http.HandlerFunc(api.proxyPort))
	defer proxy.Close()
	load := func(p *Port) {
		t.Helper()
		response, err := http.Get(proxy.URL + "/api/proxy/" + strconv.Itoa(int(p.Port)) + "~" + portSelector(p) + "/asset")
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatal(response.Status)
		}
	}
	load(p)
	load(p)
	if connections.Load() != 1 {
		t.Fatalf("same listener opened %d connections", connections.Load())
	}
	replacement := *p
	replacement.Pid++
	pr.mu.Lock()
	pr.commitLocked(map[string]*Port{"socket": &replacement})
	pr.mu.Unlock()
	load(&replacement)
	if connections.Load() != 2 {
		t.Fatal("replacement reused the previous application's connection")
	}
}
