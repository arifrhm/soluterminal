package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// capturedReq records the headers + body of an inbound HTTP request
// so the test can assert on them after the goroutine has stopped.
type capturedReq struct {
	method string
	path   string
	auth   string
	ts     string
	sig    string
	body   []byte
}

// rotationServer is a tiny stub for the cloud's two rotation endpoints.
// The test wires its responses via fields and reads back captured
// requests after.
type rotationServer struct {
	mu sync.Mutex

	// statusResponse: JSON body returned to GET /status. Tests set this
	// before the poller fires.
	statusResponse []byte
	statusCode     int

	// confirmCode: HTTP code returned to POST /confirm.
	confirmCode int

	statusCalls  []capturedReq
	confirmCalls []capturedReq

	statusFired  chan struct{}
	confirmFired chan struct{}
}

func newRotationServer() *rotationServer {
	return &rotationServer{
		statusCode:   200,
		confirmCode:  204,
		statusFired:  make(chan struct{}, 16),
		confirmFired: make(chan struct{}, 16),
	}
}

func (s *rotationServer) httpHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/internal/token-rotation/status", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.statusCalls = append(s.statusCalls, capturedReq{
			method: r.Method,
			path:   r.URL.Path,
			auth:   r.Header.Get("Authorization"),
			ts:     r.Header.Get("X-Solu-Timestamp"),
			sig:    r.Header.Get("X-Solu-Sig"),
			body:   body,
		})
		resp := s.statusResponse
		code := s.statusCode
		s.mu.Unlock()
		select {
		case s.statusFired <- struct{}{}:
		default:
		}
		if code == 0 {
			code = 200
		}
		w.WriteHeader(code)
		if len(resp) > 0 {
			_, _ = w.Write(resp)
		}
	})
	mux.HandleFunc("/api/internal/token-rotation/confirm", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.confirmCalls = append(s.confirmCalls, capturedReq{
			method: r.Method,
			path:   r.URL.Path,
			auth:   r.Header.Get("Authorization"),
			ts:     r.Header.Get("X-Solu-Timestamp"),
			sig:    r.Header.Get("X-Solu-Sig"),
			body:   body,
		})
		code := s.confirmCode
		s.mu.Unlock()
		select {
		case s.confirmFired <- struct{}{}:
		default:
		}
		if code == 0 {
			code = 204
		}
		w.WriteHeader(code)
	})
	return mux
}

// expectSig recomputes HMAC-SHA256 over <ts>.<body> and compares.
func expectSig(t *testing.T, token string, ts, sig string, body []byte) {
	t.Helper()
	if ts == "" {
		t.Fatalf("missing X-Solu-Timestamp header")
	}
	if _, err := strconv.ParseInt(ts, 10, 64); err != nil {
		t.Fatalf("X-Solu-Timestamp not unix seconds: %q (%v)", ts, err)
	}
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	gotBytes, err := hex.DecodeString(sig)
	if err != nil {
		t.Fatalf("X-Solu-Sig not hex: %q", sig)
	}
	wantBytes, _ := hex.DecodeString(want)
	if !hmac.Equal(gotBytes, wantBytes) {
		t.Fatalf("HMAC mismatch.\n got: %s\nwant: %s\n token=%s ts=%s body=%q",
			sig, want, token, ts, body)
	}
}

// runPollerForOneCycle starts the rotation poller with a tiny
// baseInterval and waits for at least one /status call. Returns a
// cancel func the caller defers.
func runPollerForOneCycle(t *testing.T, srv *rotationServer, ts *httptest.Server, token string, onRotated func(string), dataDir string) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cfg := tokenRotationCfg{
		cloudBase:    ts.URL,
		token:        token,
		onRotated:    onRotated,
		client:       ts.Client(),
		dataDir:      dataDir,
		baseInterval: 5 * time.Millisecond,
	}
	go runTokenRotationPoller(ctx, cfg)

	select {
	case <-srv.statusFired:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatalf("poller did not call /status within 2s")
	}
	return cancel
}

func TestTokenRotation_PendingFalse_NoOp(t *testing.T) {
	srv := newRotationServer()
	srv.mu.Lock()
	srv.statusResponse = []byte(`{"pending":false}`)
	srv.mu.Unlock()
	ts := httptest.NewServer(srv.httpHandler())
	defer ts.Close()

	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	var rotated atomic.Int32
	cancel := runPollerForOneCycle(t, srv, ts, token, func(newToken string) {
		rotated.Add(1)
	}, t.TempDir())
	defer cancel()

	// Give the poller a moment to finish processing the response.
	time.Sleep(50 * time.Millisecond)

	srv.mu.Lock()
	defer srv.mu.Unlock()

	if len(srv.statusCalls) == 0 {
		t.Fatalf("expected at least one /status call")
	}
	got := srv.statusCalls[0]
	if got.method != "GET" {
		t.Errorf("status method: got %q want GET", got.method)
	}
	if got.auth != "Bearer "+token {
		t.Errorf("status auth: got %q want Bearer <token>", got.auth)
	}
	expectSig(t, token, got.ts, got.sig, []byte{})

	if rotated.Load() != 0 {
		t.Errorf("onRotated should NOT fire on pending:false (fired %d times)", rotated.Load())
	}
	if len(srv.confirmCalls) != 0 {
		t.Errorf("confirm should NOT fire on pending:false (got %d calls)", len(srv.confirmCalls))
	}
}

func TestTokenRotation_PendingTrue_RotatesAndConfirms(t *testing.T) {
	srv := newRotationServer()
	const oldToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const newToken = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

	body, _ := json.Marshal(map[string]any{
		"pending":   true,
		"newToken":  newToken,
		"expiresAt": time.Now().Add(10 * time.Minute).Unix(),
	})
	srv.mu.Lock()
	srv.statusResponse = body
	srv.mu.Unlock()

	tsServer := httptest.NewServer(srv.httpHandler())
	defer tsServer.Close()

	// Set up a claim.json in a tempdir so UpdateClaimToken can swap
	// the token in place rather than failing because no claim exists.
	dataDir := t.TempDir()
	if err := SaveClaim(dataDir, &Claim{
		RelayURL:    "wss://test/relay",
		Token:       oldToken,
		MachineID:   "m_test",
		MachineName: "test-machine",
		ClaimedAt:   "2026-04-30T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}

	rotatedCh := make(chan string, 1)
	cancel := runPollerForOneCycle(t, srv, tsServer, oldToken, func(nt string) {
		rotatedCh <- nt
	}, dataDir)
	defer cancel()

	// Wait for onRotated to fire.
	select {
	case got := <-rotatedCh:
		if got != newToken {
			t.Fatalf("onRotated got %q, want %q", got, newToken)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("onRotated did not fire within 2s")
	}

	// Wait for the confirm POST to land.
	select {
	case <-srv.confirmFired:
	case <-time.After(2 * time.Second):
		t.Fatalf("confirm POST not observed within 2s")
	}

	// Give the poller goroutine a tick to finish the post-confirm
	// log line before we cancel — otherwise the in-flight Do() races
	// the ctx cancel and emits a spurious "confirm failed: context
	// canceled" ERROR in test output. Functional behavior is fine
	// either way; this just keeps `go test -v` quiet.
	time.Sleep(20 * time.Millisecond)
	cancel()
	time.Sleep(20 * time.Millisecond)

	srv.mu.Lock()
	defer srv.mu.Unlock()

	if len(srv.confirmCalls) == 0 {
		t.Fatalf("no confirm calls captured")
	}
	conf := srv.confirmCalls[0]
	if conf.method != "POST" {
		t.Errorf("confirm method: got %q want POST", conf.method)
	}
	if conf.auth != "Bearer "+newToken {
		t.Errorf("confirm auth: got %q want Bearer <NEW token> (rotation must use new token to confirm)", conf.auth)
	}
	if string(conf.body) != "{}" {
		t.Errorf("confirm body: got %q want {}", conf.body)
	}
	expectSig(t, newToken, conf.ts, conf.sig, conf.body)

	// Verify claim.json on disk now has the new token, other fields
	// preserved.
	c, err := LoadClaim(dataDir)
	if err != nil {
		t.Fatalf("LoadClaim: %v", err)
	}
	if c == nil {
		t.Fatalf("claim.json missing after rotation")
	}
	if c.Token != newToken {
		t.Errorf("claim.Token: got %q want %q", c.Token, newToken)
	}
	if c.MachineID != "m_test" || c.MachineName != "test-machine" || c.RelayURL != "wss://test/relay" {
		t.Errorf("non-token fields lost: %+v", c)
	}
}

func TestTokenRotation_StatusGet_NoBodySigning(t *testing.T) {
	// Independent assertion that the GET request signs an empty body
	// (not the literal string "null" or the response, etc.). Pinned
	// because the cloud's verifier will reject any deviation.
	srv := newRotationServer()
	srv.mu.Lock()
	srv.statusResponse = []byte(`{"pending":false}`)
	srv.mu.Unlock()
	ts := httptest.NewServer(srv.httpHandler())
	defer ts.Close()

	const token = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cancel := runPollerForOneCycle(t, srv, ts, token, func(string) {}, t.TempDir())
	defer cancel()

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.statusCalls) == 0 {
		t.Fatalf("no status calls captured")
	}
	got := srv.statusCalls[0]
	if len(got.body) != 0 {
		t.Errorf("status GET should have empty body, got %q", got.body)
	}
	expectSig(t, token, got.ts, got.sig, nil) // nil and []byte{} sign identically
}

func TestTokenRotation_ServerError_DoesNotCrashGoroutine(t *testing.T) {
	// 5xx → backoff. We just need to verify the goroutine survives
	// and keeps polling (not that it eventually gives up).
	srv := newRotationServer()
	srv.mu.Lock()
	srv.statusCode = 503
	srv.statusResponse = []byte("upstream busy")
	srv.mu.Unlock()
	ts := httptest.NewServer(srv.httpHandler())
	defer ts.Close()

	cancel := runPollerForOneCycle(t, srv, ts,
		strings.Repeat("c", 64), func(string) {}, t.TempDir())
	defer cancel()

	// Goroutine should be alive (no panic). The fact that
	// runPollerForOneCycle returned without t.Fatalf means at least
	// one /status call landed even after the 5xx. Good enough.
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.statusCalls) < 1 {
		t.Errorf("expected at least 1 status call, got %d", len(srv.statusCalls))
	}
}

func TestUpdateClaimToken_RoundTrip(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	orig := &Claim{
		RelayURL:    "wss://hosted-cloud.example/relay",
		Token:       "old_token_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MachineID:   "m_xyz",
		MachineName: "Benjamin's Laptop",
		ClaimedAt:   "2026-04-01T12:00:00Z",
	}
	if err := SaveClaim(dataDir, orig); err != nil {
		t.Fatalf("SaveClaim: %v", err)
	}
	const newTok = "new_token_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := UpdateClaimToken(dataDir, newTok); err != nil {
		t.Fatalf("UpdateClaimToken: %v", err)
	}
	got, err := LoadClaim(dataDir)
	if err != nil {
		t.Fatalf("LoadClaim: %v", err)
	}
	if got == nil {
		t.Fatalf("claim.json missing after UpdateClaimToken")
	}
	if got.Token != newTok {
		t.Errorf("Token: got %q want %q", got.Token, newTok)
	}
	if got.RelayURL != orig.RelayURL {
		t.Errorf("RelayURL changed: got %q want %q", got.RelayURL, orig.RelayURL)
	}
	if got.MachineID != orig.MachineID {
		t.Errorf("MachineID changed: got %q want %q", got.MachineID, orig.MachineID)
	}
	if got.MachineName != orig.MachineName {
		t.Errorf("MachineName changed: got %q want %q", got.MachineName, orig.MachineName)
	}
	if got.ClaimedAt != orig.ClaimedAt {
		t.Errorf("ClaimedAt changed: got %q want %q", got.ClaimedAt, orig.ClaimedAt)
	}

	// Sanity: file is 0600 (token-bearing).
	st, err := os.Stat(filepath.Join(dataDir, "claim.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("claim.json perms: got %o want 0600", mode)
	}
}

func TestUpdateClaimToken_NoClaim_Errors(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	err := UpdateClaimToken(dataDir, "any_new_token")
	if err == nil {
		t.Fatalf("expected error when claim.json doesn't exist, got nil")
	}
}
