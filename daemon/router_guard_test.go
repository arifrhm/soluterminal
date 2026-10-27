package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouterGuard(t *testing.T) {
	reached := false
	h := routerGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	cases := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{"desktop window", "127.0.0.1:28787", "http://127.0.0.1:28787", true},
		{"local tool without origin", "localhost:28787", "", true},
		{"dns rebinding host", "evil.example:28787", "http://evil.example:28787", false},
		{"cross-origin page", "127.0.0.1:28787", "https://evil.example", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached = false
			r := httptest.NewRequest("POST", "/api/panes", nil)
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if reached != tc.want {
				t.Fatalf("reached=%v want %v (code %d)", reached, tc.want, w.Code)
			}
		})
	}
}
