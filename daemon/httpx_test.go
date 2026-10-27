package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONSuccessWithinCap(t *testing.T) {
	t.Parallel()
	var dst struct {
		Name string `json:"name"`
	}
	body := `{"name":"pane"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))

	if !decodeJSON(rec, req, &dst, int64(len(body)+1)) {
		t.Fatal("decodeJSON returned false")
	}
	if dst.Name != "pane" {
		t.Fatalf("Name = %q, want pane", dst.Name)
	}
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("response = (%d, %q), want (200, empty)", rec.Code, rec.Body.String())
	}
}

func TestDecodeJSONErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		body     string
		maxBytes int64
	}{
		{name: "malformed", body: `{oops`, maxBytes: 64},
		{name: "over cap", body: `{"ok":true}`, maxBytes: int64(len(`{"ok":true}`) - 1)},
		{name: "empty", body: ``, maxBytes: 64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dst map[string]any
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))

			if decodeJSON(rec, req, &dst, tt.maxBytes) {
				t.Fatal("decodeJSON returned true")
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if got := rec.Body.String(); got != "invalid json\n" {
				t.Fatalf("body = %q, want %q", got, "invalid json\n")
			}
			if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
				t.Fatalf("Content-Type = %q, want text/plain; charset=utf-8", got)
			}
		})
	}
}

func TestDecodeJSONBodyExactlyAtCap(t *testing.T) {
	t.Parallel()
	body := `{"ok":true}`
	var dst struct {
		OK bool `json:"ok"`
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))

	if !decodeJSON(rec, req, &dst, int64(len(body))) {
		t.Fatalf("decodeJSON returned false; response=%q", rec.Body.String())
	}
	if !dst.OK {
		t.Fatal("OK = false, want true")
	}
}

func TestDecodeJSONRemainsLenient(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"unknown":true}`},
		{name: "trailing data", body: `{}garbage`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dst struct{}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))

			if !decodeJSON(rec, req, &dst, int64(len(tt.body))) {
				t.Fatalf("decodeJSON returned false; response=%q", rec.Body.String())
			}
		})
	}
}

func TestJSONError(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()

	jsonError(rec, http.StatusConflict, "boom")

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := rec.Body.String(); got != "{\"error\":\"boom\"}\n" {
		t.Fatalf("body = %q, want JSON error envelope", got)
	}
}
