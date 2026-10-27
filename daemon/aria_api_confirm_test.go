package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Regression: /api/aria/confirm must resolve confirms regardless of the
// chat kill-switch. The pane-badge CANCEL button on a cautious-mode
// auto-approve countdown posts here, and the countdown's server-side
// AfterFunc only no-ops if the user's cancel got through first — a 503
// here means cautious-mode approvals become uncancelable.
func TestAriaConfirmResolvesAutonomousCountdown(t *testing.T) {
	t.Parallel()
	aria := &Aria{
		uiBus:    newUIBus(),
		confirms: map[string]*ariaPendingConfirm{},
	}
	pc := &ariaPendingConfirm{ch: make(chan bool, 1)}
	aria.confirms["cf_auto"] = pc

	srv := &apiServer{aria: aria}
	req := httptest.NewRequest("POST", "/api/aria/confirm",
		bytes.NewBufferString(`{"id":"cf_auto","approve":false}`))
	w := httptest.NewRecorder()
	srv.handleAriaConfirm(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (cancel must not be gated)", w.Code)
	}
	select {
	case approved := <-pc.ch:
		if approved {
			t.Fatal("cancel resolved as approve")
		}
	default:
		t.Fatal("confirm was not resolved")
	}
	if _, still := aria.confirms["cf_auto"]; still {
		t.Fatal("confirm entry not consumed")
	}
}
