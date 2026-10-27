package main

import (
	"bytes"
	"context"

	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"soluterminal-shared/machineauth"
)

const daemonPreviewTestID = "pv_00000000000000000000000000"

type previewRoundTripper func(*http.Request) (*http.Response, error)

func (f previewRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func previewTestManager(t *testing.T) *previewManager {
	t.Helper()
	m, err := newPreviewManager(t.TempDir(), "wsl", func() (string, string) {
		return "https://cloud.test", "machine-token"
	})
	if err != nil {
		t.Fatal(err)
	}
	m.client = &http.Client{Transport: previewRoundTripper(func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read signed request: %v", err)
		}
		if r.Header.Get("Authorization") != "Bearer machine-token" {
			t.Errorf("machine bearer missing: %q", r.Header.Get("Authorization"))
		}
		if _, err := machineauth.VerifyHeaders(r.Header, "machine-token", raw, time.Now()); err != nil {
			t.Errorf("machine HMAC invalid: %v", err)
		}
		status := http.StatusNoContent
		body := ""
		if r.Method == http.MethodPost && r.URL.Path == "/api/internal/previews" {
			status = http.StatusCreated
			body = `{"previewId":"` + daemonPreviewTestID + `","url":"https://preview.example/preview/` + daemonPreviewTestID + `/","expiresAt":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano) + `"}`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	return m
}

func serverPort(t *testing.T, rawURL string) uint16 {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	return uint16(p)
}

func TestPreviewUnknownIDForbiddenAndFlagOffHidden(t *testing.T) {
	m := previewTestManager(t)
	a := &apiServer{previews: m}
	req := httptest.NewRequest(http.MethodGet, "/api/preview-serve/"+daemonPreviewTestID+"/", nil)
	req.SetPathValue("previewId", daemonPreviewTestID)
	t.Setenv("SOLU_PREVIEWS", "1")
	rec := httptest.NewRecorder()
	a.servePreview(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unknown status=%d body=%q", rec.Code, rec.Body.String())
	}
	t.Setenv("SOLU_PREVIEWS", "0")
	rec = httptest.NewRecorder()
	a.servePreview(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("flag-off status=%d", rec.Code)
	}
}

func TestPreviewServePinsExactPortAndStripsCredentials(t *testing.T) {
	t.Setenv("SOLU_PREVIEWS", "1")
	seen := make(chan *http.Request, 1)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(r.Context())
		_, _ = io.WriteString(w, "first target")
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "wrong target") }))
	defer second.Close()
	m := previewTestManager(t)
	now := time.Now().UTC()
	m.targets[daemonPreviewTestID] = &PreviewTarget{ID: daemonPreviewTestID, Mode: previewModeAdopt, TargetPort: serverPort(t, first.URL), State: previewStateRunning, CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(time.Hour)}
	a := &apiServer{previews: m}
	req := httptest.NewRequest(http.MethodGet, "/api/preview-serve/"+daemonPreviewTestID+"/asset?q=1&access_token=secret&pv=secret", nil)
	req.SetPathValue("previewId", daemonPreviewTestID)
	req.Header.Set("Authorization", "Bearer cloud")
	req.Header.Set("Proxy-Authorization", "Basic cloud")
	req.Header.Set("Forwarded", "for=attacker")
	req.Header.Set("X-Forwarded-For", "attacker")
	req.Header.Set("Sec-WebSocket-Protocol", "chat, bearer.jwt")
	req.AddCookie(&http.Cookie{Name: "solu_pv_" + daemonPreviewTestID, Value: "secret"})
	rec := httptest.NewRecorder()
	a.servePreview(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "first target" || strings.Contains(rec.Body.String(), "wrong target") {
		t.Fatalf("response=%d body=%q", rec.Code, rec.Body.String())
	}
	forwarded := <-seen
	if forwarded.Host != "127.0.0.1:"+strconv.Itoa(int(serverPort(t, first.URL))) {
		t.Fatalf("Host=%q", forwarded.Host)
	}
	for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Forwarded", "X-Forwarded-For"} {
		if forwarded.Header.Get(name) != "" {
			t.Fatalf("%s leaked: %q", name, forwarded.Header.Get(name))
		}
	}
	if got := forwarded.Header.Get("Sec-WebSocket-Protocol"); got != "chat" {
		t.Fatalf("subprotocol=%q", got)
	}
	if strings.Contains(forwarded.URL.RawQuery, "secret") {
		t.Fatalf("credential query leaked: %q", forwarded.URL.RawQuery)
	}
}

func TestPreviewAdoptionRejectsDeadAndNonHTTPPorts(t *testing.T) {
	t.Setenv("SOLU_PREVIEWS", "1")
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadPort := serverPort(t, dead.URL)
	dead.Close()
	m := previewTestManager(t)
	// A plain host refuses the closed port ("not listening"). Under WSL
	// Consomme networking a just-closed port still accepts and then drops the
	// connection, which surfaces as "not an HTTP service". Either way the
	// port must be rejected.
	if _, err := m.Create(context.Background(), deadPort, "", nil); err == nil ||
		(!strings.Contains(err.Error(), "not listening") && !strings.Contains(err.Error(), "not an HTTP service")) {
		t.Fatalf("dead port err=%v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_, _ = conn.Write([]byte("not http\n"))
			_ = conn.Close()
		}
	}()
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	if _, err := m.Create(context.Background(), port, "", nil); err == nil || !strings.Contains(err.Error(), "not an HTTP service") {
		t.Fatalf("non-http err=%v", err)
	}
}

func TestPreviewRegistrySaveReloadExpiryAndCap(t *testing.T) {
	t.Setenv("SOLU_PREVIEWS", "1")
	dataDir := t.TempDir()
	m, err := newPreviewManager(dataDir, "wsl", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	m.targets[daemonPreviewTestID] = &PreviewTarget{ID: daemonPreviewTestID, URL: "https://preview.example/preview/" + daemonPreviewTestID + "/", Mode: previewModeAdopt, TargetPort: 3000, State: previewStateRunning, CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(time.Hour)}
	expiredID := "pv_11111111111111111111111111"
	if !validDaemonPreviewID(expiredID) {
		t.Fatal("expired reload fixture must have a valid preview ID")
	}
	m.targets[expiredID] = &PreviewTarget{ID: expiredID, URL: "https://preview.example/preview/" + expiredID + "/", Mode: previewModeAdopt, TargetPort: 3001, State: previewStateExpired, CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(-time.Minute)}
	malformedID := "malformed"
	m.targets[malformedID] = &PreviewTarget{ID: malformedID, URL: "https://preview.example/preview/malformed/", Mode: previewModeAdopt, TargetPort: 3002, State: previewStateRunning, CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(time.Hour)}
	m.mu.Lock()
	if err := m.saveLocked(); err != nil {
		t.Fatal(err)
	}
	m.mu.Unlock()
	reloaded, err := newPreviewManager(dataDir, "wsl", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Get(daemonPreviewTestID); !ok {
		t.Fatal("live preview was not reloaded")
	}
	if _, ok := reloaded.Get(expiredID); ok {
		t.Fatal("expired preview survived reload")
	}
	if _, ok := reloaded.Get(malformedID); ok {
		t.Fatal("malformed preview survived reload")
	}
	raw, err := os.ReadFile(reloaded.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("solupv1.")) {
		t.Fatal("registry persisted a signed token")
	}
}

func TestPreviewThreeTargetCapAndFailedCloudRollback(t *testing.T) {
	t.Setenv("SOLU_PREVIEWS", "1")
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer target.Close()
	m := previewTestManager(t)
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		id := "existing-" + strconv.Itoa(i)
		m.targets[id] = &PreviewTarget{ID: id, Mode: previewModeAdopt, TargetPort: serverPort(t, target.URL), State: previewStateRunning, CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(time.Hour)}
	}
	if _, err := m.Create(context.Background(), serverPort(t, target.URL), "", nil); err == nil {
		t.Fatal("fourth preview was accepted")
	} else {
		var apiErr *previewHTTPError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
			t.Fatalf("fourth preview error=%v, want 409", err)
		}
	}
	if len(m.List()) != 3 {
		t.Fatalf("registry size=%d", len(m.List()))
	}
	m.targets = map[string]*PreviewTarget{}
	m.client = &http.Client{Transport: previewRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("failed"))}, nil
	})}
	if _, err := m.Create(context.Background(), serverPort(t, target.URL), "", nil); err == nil {
		t.Fatal("failed cloud registration unexpectedly succeeded")
	}
	if len(m.List()) != 0 {
		t.Fatal("failed registration left a local target")
	}
}

func TestPreviewIdleDoesNotKillAdoptedTargetAndHardExpiryClosesStreams(t *testing.T) {
	t.Setenv("SOLU_PREVIEWS", "1")
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "alive") }))
	defer target.Close()
	m := previewTestManager(t)
	now := time.Now().UTC()
	m.now = func() time.Time { return now }
	m.idleAfter = time.Minute
	m.targets[daemonPreviewTestID] = &PreviewTarget{ID: daemonPreviewTestID, Mode: previewModeAdopt, TargetPort: serverPort(t, target.URL), State: previewStateRunning, CreatedAt: now.Add(-time.Hour), LastActivityAt: now.Add(-2 * time.Minute), ExpiresAt: now.Add(time.Hour), ActiveStreams: 1}
	m.sweep(context.Background())
	if _, ok := m.Get(daemonPreviewTestID); !ok {
		t.Fatal("active stream did not block idle unregister")
	}
	m.targets[daemonPreviewTestID].ActiveStreams = 0
	m.sweep(context.Background())
	if _, ok := m.Get(daemonPreviewTestID); ok {
		t.Fatal("idle adopted preview remained registered")
	}
	resp, err := http.Get(target.URL)
	if err != nil {
		t.Fatalf("adopted target was killed: %v", err)
	}
	_ = resp.Body.Close()

	m.targets[daemonPreviewTestID] = &PreviewTarget{ID: daemonPreviewTestID, Mode: previewModeAdopt, TargetPort: serverPort(t, target.URL), State: previewStateRunning, CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(time.Second)}
	_, streamCtx, done, ok := m.beginRequest(context.Background(), daemonPreviewTestID)
	if !ok {
		t.Fatal("failed to acquire stream")
	}
	now = now.Add(2 * time.Second)
	m.sweep(context.Background())
	select {
	case <-streamCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("hard expiry did not close active stream")
	}
	if _, ok := m.Get(daemonPreviewTestID); ok {
		t.Fatal("hard-expired preview remained in registry")
	}
	done()
}

func TestPreviewHardExpiryRetainsTombstoneUntilCloudRevokeSucceeds(t *testing.T) {
	t.Setenv("SOLU_PREVIEWS", "1")
	m := previewTestManager(t)
	now := time.Now().UTC()
	m.now = func() time.Time { return now }
	m.targets[daemonPreviewTestID] = &PreviewTarget{
		ID: daemonPreviewTestID, URL: "https://preview.example/preview/" + daemonPreviewTestID + "/",
		Mode: previewModeAdopt, TargetPort: 3000, State: previewStateRunning,
		CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(time.Second),
	}
	_, streamCtx, done, ok := m.beginRequest(context.Background(), daemonPreviewTestID)
	if !ok {
		t.Fatal("failed to acquire preview stream")
	}
	defer done()
	revokeAttempts := 0
	m.client = &http.Client{Transport: previewRoundTripper(func(r *http.Request) (*http.Response, error) {
		revokeAttempts++
		status := http.StatusNoContent
		if revokeAttempts == 1 {
			status = http.StatusInternalServerError
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	now = now.Add(2 * time.Second)
	m.sweep(context.Background())
	select {
	case <-streamCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("hard expiry did not close active streams after revoke failure")
	}
	retained, ok := m.Get(daemonPreviewTestID)
	if !ok || retained.State != previewStateExpired {
		t.Fatalf("hard expiry lost revoke tombstone after cloud failure: target=%+v ok=%v", retained, ok)
	}
	m.sweep(context.Background())
	if _, ok := m.Get(daemonPreviewTestID); ok {
		t.Fatal("preview tombstone survived successful revoke retry")
	}
	if revokeAttempts != 2 {
		t.Fatalf("revoke attempts=%d want 2", revokeAttempts)
	}
}

func TestPreviewAdoptedDeadTargetReturnsGone(t *testing.T) {
	t.Setenv("SOLU_PREVIEWS", "1")
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	port := serverPort(t, target.URL)
	target.Close()
	m := previewTestManager(t)
	now := time.Now().UTC()
	m.targets[daemonPreviewTestID] = &PreviewTarget{ID: daemonPreviewTestID, Mode: previewModeAdopt, TargetPort: port, State: previewStateRunning, CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(time.Hour)}
	a := &apiServer{previews: m}
	req := httptest.NewRequest(http.MethodGet, "/api/preview-serve/"+daemonPreviewTestID+"/", nil)
	req.SetPathValue("previewId", daemonPreviewTestID)
	rec := httptest.NewRecorder()
	a.servePreview(rec, req)
	if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), "Preview expired") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestPreviewManagerPruneCacheKeepsPinnedAndNewest(t *testing.T) {
	t.Parallel()
	m := previewTestManager(t)
	now := time.Now().UTC()
	makeBundle := func(hex string, age time.Duration) string {
		dir := filepath.Join(m.cacheRoot, hex)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "payload"), []byte("0123456789"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := now.Add(-age)
		if err := os.Chtimes(dir, at, at); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	pinned := makeBundle(strings.Repeat("a", 64), 3*time.Hour)
	oldest := makeBundle(strings.Repeat("b", 64), 2*time.Hour)
	newest := makeBundle(strings.Repeat("c", 64), time.Hour)
	m.targets[daemonPreviewTestID] = &PreviewTarget{ID: daemonPreviewTestID, BundleDir: pinned, State: previewStateIdle, ExpiresAt: now.Add(time.Hour)}
	if removed := m.pruneCache(20); removed != 10 {
		t.Fatalf("removed=%d want=10", removed)
	}
	for _, path := range []string{pinned, newest} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("survivor %s: %v", path, err)
		}
	}
	if _, err := os.Stat(oldest); !os.IsNotExist(err) {
		t.Fatalf("oldest bundle survived: %v", err)
	}
}
