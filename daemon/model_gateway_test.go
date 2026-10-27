package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPIKeyAccountsEncryptedAndRedacted(t *testing.T) {
	data := t.TempDir()
	s := newAPIKeyAccountStore(data, "anthropic")
	if err := s.save("first", "api-secret-123"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.path())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("api-secret-123")) || !bytes.Contains(raw, []byte(harnessSecretPrefix)) {
		t.Fatalf("key not sealed: %s", raw)
	}
	if _, value, err := newAPIKeyAccountStore(data, "anthropic").selected(""); err != nil || value != "api-secret-123" {
		t.Fatalf("round trip: %q %v", value, err)
	}
	a := &apiServer{dataDir: data}
	r := httptest.NewRequest(http.MethodGet, "/api/key-providers/anthropic", nil)
	r.SetPathValue("provider", "anthropic")
	w := httptest.NewRecorder()
	a.apiKeyAccounts(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "api-secret-123") {
		t.Fatalf("API leaked key: %d %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodPut, "/api/key-providers/anthropic/accounts", strings.NewReader(`{"name":"other","value":"secret"}`))
	r.SetPathValue("provider", "anthropic")
	r.Header.Set("X-Forwarded-Prefix", "/m/machine")
	w = httptest.NewRecorder()
	a.apiKeyAccounts(w, r)
	if w.Code != 403 {
		t.Fatalf("relay mutation: %d", w.Code)
	}
}

func TestModelGatewayAuthStreamingAccountsAndRevocation(t *testing.T) {
	data := t.TempDir()
	var audit bytes.Buffer
	seen := make(chan string, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Authorization") + r.Header.Get("x-api-key")
		seen <- key
		if r.Header.Get("Cookie") != "" || r.Header.Get("X-Solu-Account") != "" {
			t.Errorf("caller headers reached upstream")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(201)
		_, _ = w.Write([]byte("data: first\n\n"))
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("data: second\n\n"))
	}))
	defer upstream.Close()
	g := newModelGateway(data, log.New(&audit, "", 0))
	g.upstreamOverride = map[string]string{"anthropic": upstream.URL, "openai": upstream.URL}
	if err := g.Start("http://127.0.0.1:9791" + modelGatewayPrefix); err != nil {
		t.Fatal(err)
	}
	if err := g.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{modelGatewayTokenFile, modelGatewayDiscoveryFile} {
		info, err := os.Stat(filepath.Join(data, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("%s mode: %v %v", name, info, err)
		}
	}
	var discovery struct {
		Version    int `json:"version"`
		URL, Token string
	}
	raw, _ := os.ReadFile(filepath.Join(data, modelGatewayDiscoveryFile))
	if json.Unmarshal(raw, &discovery) != nil || discovery.Version != 1 || discovery.Token == "" {
		t.Fatal("bad discovery")
	}
	for _, provider := range []string{"anthropic", "openai"} {
		s := newAPIKeyAccountStore(data, provider)
		if err := s.save("one", provider+"-one-secret"); err != nil {
			t.Fatal(err)
		}
		if err := s.save("two", provider+"-two-secret"); err != nil {
			t.Fatal(err)
		}
		path := modelGatewayPrefix + "/" + provider + "/" + provider + "/v1/"
		if provider == "anthropic" {
			path += "messages"
		} else {
			path += "chat/completions"
		}
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"test"}`))
		r.Header.Set("Authorization", "Bearer "+discovery.Token)
		r.Header.Set("X-Solu-Account", "personal/two")
		r.Header.Set("Cookie", "solu_session=master")
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != 201 || !strings.Contains(w.Body.String(), "data: second") || strings.Contains(w.Body.String(), "secret") {
			t.Fatalf("stream: %d %q", w.Code, w.Body.String())
		}
		want := provider + "-two-secret"
		if provider == "openai" {
			want = "Bearer " + want
		}
		if got := <-seen; got != want {
			t.Fatalf("upstream got %q, want %q", got, want)
		}
	}
	for _, tc := range []struct {
		name, auth, key, prefix string
		want                    int
	}{
		{"tokenless", "", "", "", 403}, {"master", "Bearer master", "", "", 403},
		{"cookie", "", "", "", 403}, {"relay", "Bearer " + discovery.Token, "", "/m/machine", 403},
		{"scoped bearer", "Bearer " + discovery.Token, "", "", 200}, {"scoped x-api-key", "", discovery.Token, "", 200},
	} {
		r := httptest.NewRequest(http.MethodGet, modelGatewayPrefix+"/providers", nil)
		r.Header.Set("Authorization", tc.auth)
		r.Header.Set("x-api-key", tc.key)
		r.Header.Set("X-Forwarded-Prefix", tc.prefix)
		if tc.name == "cookie" {
			r.AddCookie(&http.Cookie{Name: tokenCookie, Value: discovery.Token})
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: %d", tc.name, w.Code)
		}
		if strings.Contains(w.Body.String(), "secret") {
			t.Fatalf("%s leaked key", tc.name)
		}
	}
	if strings.Contains(audit.String(), "secret") || !strings.Contains(audit.String(), "provider=openai account=personal/two") {
		t.Fatalf("audit: %s", audit.String())
	}
	if err := g.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{modelGatewayTokenFile, modelGatewayDiscoveryFile} {
		if _, err := os.Stat(filepath.Join(data, name)); !os.IsNotExist(err) {
			t.Fatalf("%s still exists: %v", name, err)
		}
	}
	r := httptest.NewRequest(http.MethodGet, modelGatewayPrefix+"/providers", nil)
	r.Header.Set("Authorization", "Bearer "+discovery.Token)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("revocation: %d", w.Code)
	}
}

func TestModelGatewayTeamAssignmentRequiresScopedBroker(t *testing.T) {
	data := t.TempDir()
	store := newHarnessCredStore(data, "deepseek")
	if err := store.bindScope("pair"); err != nil {
		t.Fatal(err)
	}
	if err := store.replaceTeam("pair", "team", map[string]harnessAccount{
		"shared": {Name: "shared", BrokerOnly: true, Billing: "api", Model: "deepseek-flash"},
	}); err != nil {
		t.Fatal(err)
	}
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	g := newModelGateway(data, log.New(io.Discard, "", 0))
	g.harnessStore = func(string) *harnessCredStore { return store }
	g.harnessScope = func(string) string { return "pair" }
	g.upstreamOverride = map[string]string{"deepseek": upstream.URL}
	if err := g.Start("http://127.0.0.1:9791" + modelGatewayPrefix); err != nil {
		t.Fatal(err)
	}
	if err := g.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	for _, provider := range g.providers() {
		if provider.Provider == "deepseek" && len(provider.Accounts) != 0 {
			t.Fatalf("team assignment advertised as a direct gateway key: %+v", provider.Accounts)
		}
	}
	if _, err := store.account("team", "shared"); err == nil {
		t.Fatal("model gateway read a team credential")
	}
	raw, err := os.ReadFile(filepath.Join(data, modelGatewayTokenFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range []string{"", "team/shared"} {
		r := httptest.NewRequest(http.MethodPost, modelGatewayPrefix+"/deepseek/openai/v1/chat/completions", strings.NewReader(`{"model":"deepseek-flash","messages":[]}`))
		r.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(raw)))
		if account != "" {
			r.Header.Set("X-Solu-Account", account)
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != http.StatusServiceUnavailable || called {
			t.Fatalf("account %q: status=%d upstreamCalled=%t", account, w.Code, called)
		}
	}
}

func TestModelGatewayAuthGateExemptionIsScoped(t *testing.T) {
	data := t.TempDir()
	g := newModelGateway(data, log.New(io.Discard, "", 0))
	if err := g.Start("http://127.0.0.1:9791" + modelGatewayPrefix); err != nil {
		t.Fatal(err)
	}
	if err := g.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(data, modelGatewayTokenFile))
	token := strings.TrimSpace(string(raw))
	a := &apiServer{dataDir: data, modelGateway: g}
	mux := http.NewServeMux()
	a.routes(mux)
	h := (&authGate{token: "master-token"}).middleware(mux)
	for _, tc := range []struct {
		path, auth string
		want       int
	}{
		{modelGatewayPrefix + "/providers", "Bearer " + token, 200},
		{modelGatewayPrefix + "/providers", "Bearer master-token", 403},
		{modelGatewayPrefix + "/providers", "", 403},
		{modelGatewayPrefix + "x/providers", "Bearer " + token, 401},
	} {
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		r.RemoteAddr = "192.0.2.2:1234"
		r.Header.Set("Authorization", tc.auth)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s %s: got %d want %d", tc.path, tc.auth, w.Code, tc.want)
		}
	}
}
