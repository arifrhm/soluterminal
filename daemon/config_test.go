package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func withDefaultSupaCreds(t *testing.T, url, anonKey string) {
	t.Helper()
	oldURL := defaultSupaURL
	oldAnonKey := defaultSupaAnonKey
	defaultSupaURL = url
	defaultSupaAnonKey = anonKey
	t.Cleanup(func() {
		defaultSupaURL = oldURL
		defaultSupaAnonKey = oldAnonKey
	})
}

func TestResolvedSupaCredsUsesLinkDefaults(t *testing.T) {
	t.Setenv("SOLU_SUPABASE_URL", "")
	t.Setenv("SOLU_SUPABASE_ANON_KEY", "")
	withDefaultSupaCreds(t, "https://default.example", "default-anon")

	url, anonKey := resolvedSupaCreds()
	if url != "https://default.example" || anonKey != "default-anon" {
		t.Fatalf("resolvedSupaCreds = (%q, %q), want link defaults", url, anonKey)
	}
}

func TestResolvedSupaCredsEnvOverridesLinkDefaults(t *testing.T) {
	t.Setenv("SOLU_SUPABASE_URL", "https://env.example")
	t.Setenv("SOLU_SUPABASE_ANON_KEY", "env-anon")
	withDefaultSupaCreds(t, "https://default.example", "default-anon")

	url, anonKey := resolvedSupaCreds()
	if url != "https://env.example" || anonKey != "env-anon" {
		t.Fatalf("resolvedSupaCreds = (%q, %q), want env creds", url, anonKey)
	}
}

func TestHandleConfigMissingCredsReturns404(t *testing.T) {
	t.Setenv("SOLU_SUPABASE_URL", "")
	t.Setenv("SOLU_SUPABASE_ANON_KEY", "")
	withDefaultSupaCreds(t, "", "")

	rec := httptest.NewRecorder()
	(&apiServer{}).handleConfig(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandleConfigWithCredsReturnsLocalConfig(t *testing.T) {
	t.Setenv("SOLU_SUPABASE_URL", "")
	t.Setenv("SOLU_SUPABASE_ANON_KEY", "")
	withDefaultSupaCreds(t, "https://default.example", "default-anon")

	rec := httptest.NewRecorder()
	(&apiServer{}).handleConfig(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body struct {
		SupabaseURL     string `json:"supabaseUrl"`
		SupabaseAnonKey string `json:"supabaseAnonKey"`
		IsLocal         bool   `json:"isLocal"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if body.SupabaseURL != "https://default.example" || body.SupabaseAnonKey != "default-anon" || !body.IsLocal {
		t.Fatalf("body = %+v, want local config with link defaults", body)
	}
}
