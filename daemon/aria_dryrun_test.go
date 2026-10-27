package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// handleAriaDryRun's contract: loopback-only, requires aria configured,
// requires non-empty paneTail + instruction. The streaming completion
// path itself isn't unit-testable without mocking the cloud — we lean
// on integration runs for that — but the validation surface is worth
// pinning so a future "let's add a field" doesn't accidentally drop
// the loopback gate or the empty-input guard.

func TestAriaDryRunRefusesNonLoopback(t *testing.T) {
	t.Parallel()
	a := &apiServer{aria: &Aria{cloudBase: "https://example.test", cloudTok: "tok"}}
	req := httptest.NewRequest("POST", "/api/aria/dryrun", strings.NewReader(`{"paneTail":"x","instruction":"y"}`))
	req.RemoteAddr = "203.0.113.4:54321" // arbitrary public IP
	w := httptest.NewRecorder()
	a.handleAriaDryRun(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestAriaDryRunRefusesUnconfiguredAria(t *testing.T) {
	t.Parallel()
	a := &apiServer{aria: &Aria{}} // no cloudBase / cloudTok
	req := httptest.NewRequest("POST", "/api/aria/dryrun", strings.NewReader(`{"paneTail":"x","instruction":"y"}`))
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	a.handleAriaDryRun(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestAriaDryRunRefusesEmptyFields(t *testing.T) {
	t.Parallel()
	a := &apiServer{aria: &Aria{cloudBase: "https://example.test", cloudTok: "tok"}}
	cases := []string{
		`{"paneTail":"","instruction":"y"}`,
		`{"paneTail":"x","instruction":""}`,
		`{"paneTail":"   ","instruction":"y"}`,
	}
	for _, body := range cases {
		req := httptest.NewRequest("POST", "/api/aria/dryrun", bytes.NewReader([]byte(body)))
		req.RemoteAddr = "127.0.0.1:54321"
		w := httptest.NewRecorder()
		a.handleAriaDryRun(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body=%q status = %d, want 400", body, w.Code)
		}
	}
}
