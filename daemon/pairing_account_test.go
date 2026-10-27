package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// accountCloud wraps fakeCloud with /api/claim/accept, which flips the wait
// status to "claimed" when the bearer is the expected account token.
func accountCloud(t *testing.T, acceptStatus int, acceptBody map[string]any) (*fakeCloud, *httptest.Server, *int32) {
	t.Helper()
	cloud := newFakeCloud()
	var accepts int32
	mux := http.NewServeMux()
	mux.Handle("/api/claim/", cloud.handler())
	mux.HandleFunc("/api/claim/accept", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&accepts, 1)
		if r.Header.Get("Authorization") != "Bearer acct-token" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		var body struct{ Code, Name string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Code != cloud.code || strings.TrimSpace(body.Name) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request"})
			return
		}
		if acceptStatus != http.StatusOK {
			writeJSON(w, acceptStatus, acceptBody)
			return
		}
		cloud.setWaitStatus("claimed")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return cloud, ts, &accepts
}

func postPairAccount(api *apiServer, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/cloud/pair/account", strings.NewReader(body))
	if token != "" {
		req.Header.Set(entitlementsAccountHdr, token)
	}
	rec := httptest.NewRecorder()
	api.handleCloudPairAccount(rec, req)
	return rec
}

func decodePairAccount(t *testing.T, rec *httptest.ResponseRecorder) accountPairResult {
	t.Helper()
	var out accountPairResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	return out
}

func TestPairAccountConnectsWithAccountToken(t *testing.T) {
	cloud, ts, accepts := accountCloud(t, http.StatusOK, nil)
	dir := t.TempDir()
	pm := newPairingManager(t.Context(), relayURLFor(ts), dir, nil)
	pm.pollInterval = 5 * time.Millisecond
	defer pm.cancel()
	api := &apiServer{pairing: pm, dataDir: dir}

	rec := postPairAccount(api, "acct-token", "")
	if got := decodePairAccount(t, rec); rec.Code != 200 || got.Status != "connecting" {
		t.Fatalf("code=%d result=%+v", rec.Code, got)
	}
	if atomic.LoadInt32(accepts) != 1 {
		t.Fatalf("accepts=%d, want 1", *accepts)
	}
	eventuallyTrue(t, 2*time.Second, func() bool {
		c, err := LoadClaim(dir)
		return err == nil && c != nil && c.Token == cloud.token
	})
	// Already connected: a second call is a no-op.
	rec = postPairAccount(api, "acct-token", "")
	if got := decodePairAccount(t, rec); got.Status != "connected" || atomic.LoadInt32(accepts) != 1 {
		t.Fatalf("second call: %+v accepts=%d", got, *accepts)
	}
}

func TestPairAccountRespectsExplicitDisconnect(t *testing.T) {
	_, ts, accepts := accountCloud(t, http.StatusOK, nil)
	dir := t.TempDir()
	pm := newPairingManager(t.Context(), relayURLFor(ts), dir, nil)
	defer pm.cancel()
	api := &apiServer{pairing: pm, dataDir: dir}

	disc := httptest.NewRecorder()
	api.handleCloudDisconnect(disc, httptest.NewRequest("POST", "/api/cloud/disconnect", nil))
	if disc.Code != 200 || !userDisconnected(dir) {
		t.Fatalf("disconnect: code=%d marker=%v", disc.Code, userDisconnected(dir))
	}
	if got := decodePairAccount(t, postPairAccount(api, "acct-token", "")); got.Status != "declined" {
		t.Fatalf("auto connect after disconnect: %+v", got)
	}
	if atomic.LoadInt32(accepts) != 0 {
		t.Fatalf("declined connect still called the cloud")
	}
	// The user asking to connect again overrides the earlier choice.
	if got := decodePairAccount(t, postPairAccount(api, "acct-token", `{"force":true}`)); got.Status != "connecting" {
		t.Fatalf("forced connect: %+v", got)
	}
	if userDisconnected(dir) {
		t.Fatalf("marker kept after the user connected again")
	}
}

func TestPairAccountReportsMachineLimit(t *testing.T) {
	_, ts, _ := accountCloud(t, http.StatusForbidden, map[string]any{
		"error":   "machine_limit",
		"message": "This account can connect up to 3 computers on the free beta.",
	})
	dir := t.TempDir()
	pm := newPairingManager(t.Context(), relayURLFor(ts), dir, nil)
	defer pm.cancel()
	api := &apiServer{pairing: pm, dataDir: dir}

	rec := postPairAccount(api, "acct-token", "")
	got := decodePairAccount(t, rec)
	if rec.Code != http.StatusConflict || got.Status != "machine_limit" || !strings.Contains(got.Message, "3 computers") {
		t.Fatalf("code=%d result=%+v", rec.Code, got)
	}
	if pm.snapshot().Active {
		t.Fatalf("pending pairing left behind after a refused accept")
	}
}

func TestPairAccountNeedsTokenAndLocalCaller(t *testing.T) {
	dir := t.TempDir()
	api := &apiServer{pairing: newPairingManager(t.Context(), "ws://x.test/relay", dir, nil), dataDir: dir}
	if rec := postPairAccount(api, "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: code=%d", rec.Code)
	}
	req := httptest.NewRequest("POST", "/api/cloud/pair/account", nil)
	req.Header.Set(entitlementsAccountHdr, "acct-token")
	req.Header.Set("X-Forwarded-Prefix", "/m/mach_1")
	rec := httptest.NewRecorder()
	api.handleCloudPairAccount(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("relayed request: code=%d", rec.Code)
	}
}

func TestPairAccountCheckAndDecline(t *testing.T) {
	_, ts, accepts := accountCloud(t, http.StatusOK, nil)
	dir := t.TempDir()
	pm := newPairingManager(t.Context(), relayURLFor(ts), dir, nil)
	defer pm.cancel()
	api := &apiServer{pairing: pm, dataDir: dir}

	if got := decodePairAccount(t, postPairAccount(api, "acct-token", `{"check":true}`)); got.Status != "available" {
		t.Fatalf("check: %+v", got)
	}
	if pm.snapshot().Active || atomic.LoadInt32(accepts) != 0 {
		t.Fatalf("check must not start pairing")
	}
	if got := decodePairAccount(t, postPairAccount(api, "acct-token", `{"decline":true}`)); got.Status != "declined" || !userDisconnected(dir) {
		t.Fatalf("decline: %+v marker=%v", got, userDisconnected(dir))
	}
	if got := decodePairAccount(t, postPairAccount(api, "acct-token", `{"check":true}`)); got.Status != "declined" {
		t.Fatalf("check after Not now: %+v", got)
	}
}
