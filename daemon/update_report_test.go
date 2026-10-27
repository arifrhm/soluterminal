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
	"strconv"
	"testing"
	"time"
)

// TestReportUpdateOutcomeSendsSignedRequest locks in the wire format
// the cloud handler in cloud/update_report.go validates: bearer token,
// HMAC-SHA256(timestamp || "." || body, token) hex-encoded, JSON body
// with fromVersion/toVersion/outcome. If this drifts, real reports
// will silently bounce on the cloud's signature check.
func TestReportUpdateOutcomeSendsSignedRequest(t *testing.T) {
	const (
		token = "machine-token-deadbeef"
		from  = "0.0.369"
		to    = "0.0.370"
	)

	type captured struct {
		bearer string
		ts     string
		sig    string
		body   []byte
	}
	captureCh := make(chan captured, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captureCh <- captured{
			bearer: r.Header.Get("Authorization"),
			ts:     r.Header.Get("X-Solu-Timestamp"),
			sig:    r.Header.Get("X-Solu-Sig"),
			body:   body,
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	reportUpdateOutcome(context.Background(), srv.URL, token, from, to, "ok")

	select {
	case got := <-captureCh:
		if got.bearer != "Bearer "+token {
			t.Errorf("Authorization = %q, want Bearer <token>", got.bearer)
		}
		// Decode + re-encode the body ourselves so a stable struct
		// shape is asserted, not the raw byte order.
		var b struct {
			FromVersion string `json:"fromVersion"`
			ToVersion   string `json:"toVersion"`
			Outcome     string `json:"outcome"`
		}
		if err := json.Unmarshal(got.body, &b); err != nil {
			t.Fatalf("invalid json body: %v\nraw=%s", err, got.body)
		}
		if b.FromVersion != from || b.ToVersion != to || b.Outcome != "ok" {
			t.Errorf("body = %+v, want from=%s to=%s outcome=ok", b, from, to)
		}
		// Verify the HMAC the cloud will check.
		ts, err := strconv.ParseInt(got.ts, 10, 64)
		if err != nil {
			t.Fatalf("X-Solu-Timestamp not a number: %v", err)
		}
		if skew := time.Since(time.Unix(ts, 0)); skew > 30*time.Second || skew < -30*time.Second {
			t.Errorf("timestamp skew unreasonable: %v", skew)
		}
		mac := hmac.New(sha256.New, []byte(token))
		mac.Write([]byte(got.ts))
		mac.Write([]byte("."))
		mac.Write(got.body)
		want := hex.EncodeToString(mac.Sum(nil))
		if got.sig != want {
			t.Errorf("X-Solu-Sig mismatch\n got: %s\nwant: %s", got.sig, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive the report")
	}
}
