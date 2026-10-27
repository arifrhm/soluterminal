package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// Exercise the production mux with its SPA catch-all: testing the proxy
// directly misses the method-specific route falling through to index.html.
func TestAriaMcpProxyUnsupportedMethodsDoNotServeSPA(t *testing.T) {
	t.Parallel()
	a := &apiServer{}
	mux := http.NewServeMux()
	a.routes(mux)
	mux.Handle("/", spaFileServer(http.FS(fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><html>app shell</html>")},
	})))
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodDelete, http.MethodPut, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/aria/mcp/aria-comms", nil)
			req.Header.Set("Accept", "text/event-stream")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
				t.Fatalf("%s: status=%d Allow=%q, want 405 and POST", method, rec.Code, rec.Header().Get("Allow"))
			}
			if strings.Contains(rec.Body.String(), "app shell") {
				t.Fatal("unsupported MCP method returned the SPA shell")
			}
		})
	}
}

func TestAriaMcpProxyURL(t *testing.T) {
	t.Parallel()
	if got := ariaMcpProxyURL("127.0.0.1:8787", "aria-comms"); got != "http://127.0.0.1:8787/api/aria/mcp/aria-comms" {
		t.Errorf("url = %q", got)
	}
	// Bound to all interfaces → still loopback for the local client.
	if got := ariaMcpProxyURL("0.0.0.0:9999", "aria-comms-scout"); got != "http://127.0.0.1:9999/api/aria/mcp/aria-comms-scout" {
		t.Errorf("url = %q", got)
	}
	// No usable addr → empty (callers fall back to the direct-gateway path).
	if got := ariaMcpProxyURL("", "x"); got != "" {
		t.Errorf("empty addr should give empty url, got %q", got)
	}
}

func TestAgentByMcpName(t *testing.T) {
	t.Parallel()
	sup := testSupervisor(t)
	_ = sup.store.set(ariaResponderConfig{Agents: []ariaAgentConfig{
		{ID: "a1", AgentKey: testAgentKey, McpName: "aria-comms"},
		{ID: "a2", AgentKey: testAgentKey2, McpName: "aria-comms-scout"},
	}})
	if a, ok := sup.agentByMcpName("aria-comms-scout"); !ok || a.ID != "a2" {
		t.Fatalf("lookup a2 = %+v ok=%v", a, ok)
	}
	if _, ok := sup.agentByMcpName("aria-comms"); !ok {
		t.Fatalf("lookup default agent failed")
	}
	if _, ok := sup.agentByMcpName("nope"); ok {
		t.Fatalf("unknown name should miss")
	}
	if _, ok := sup.agentByMcpName(""); ok {
		t.Fatalf("empty name should miss")
	}
}

func TestObserveMcpCallRecordsSend(t *testing.T) {
	t.Parallel()
	sup := testSupervisor(t)
	run := newAriaAgentRunner(ariaAgentConfig{ID: "a1", AgentKey: testAgentKey}, nil, nil, t.TempDir(), http.DefaultClient)
	sup.mu.Lock()
	sup.runners["a1"] = run
	sup.mu.Unlock()

	// A send_message tool call is recorded as delivery evidence.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send_message","arguments":{"source":"wa","chat_id":"c1","text":"hi"}}}`
	sup.observeMcpCall("a1", []byte(body))
	run.statusMu.Lock()
	act := append([]ariaActivity(nil), run.activity...)
	lastReply := run.stLastReply
	run.statusMu.Unlock()
	if len(act) != 1 || act[0].Kind != "sent" || act[0].Chat != "wa/c1" {
		t.Fatalf("activity = %+v", act)
	}
	if lastReply.IsZero() {
		t.Errorf("send should stamp lastReply")
	}

	// Non-send calls (tools/list, other tools, malformed) are ignored.
	for _, b := range []string{
		`{"method":"tools/list"}`,
		`{"method":"tools/call","params":{"name":"read_messages"}}`,
		`not json`,
	} {
		sup.observeMcpCall("a1", []byte(b))
	}
	run.statusMu.Lock()
	n := len(run.activity)
	run.statusMu.Unlock()
	if n != 1 {
		t.Fatalf("non-send calls should not record, activity count = %d", n)
	}
}

func TestHandleAriaMcpProxyForwardsWithKey(t *testing.T) {
	var gotAuth, gotBody string
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			t.Errorf("upstream path = %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	}))
	defer gw.Close()

	a := &apiServer{ariaResponder: testSupervisor(t)}
	_ = a.ariaResponder.store.set(ariaResponderConfig{Agents: []ariaAgentConfig{
		{ID: "a1", AgentKey: testAgentKey, GatewayURL: gw.URL, McpName: "aria-comms"},
	}})

	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send_message","arguments":{"source":"wa","chat_id":"c1","text":"hi"}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/aria/mcp/aria-comms", strings.NewReader(call))
	req.RemoteAddr = "127.0.0.1:55555" // loopback
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	a.routes(mux)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("proxy code = %d body=%s", rec.Code, rec.Body)
	}
	if gotAuth != "Bearer "+testAgentKey {
		t.Errorf("upstream auth = %q, want injected key", gotAuth)
	}
	if !strings.Contains(gotBody, "send_message") {
		t.Errorf("body not forwarded: %q", gotBody)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Errorf("response not passed through: %v", err)
	}
}

func TestHandleAriaMcpProxyRejectsNonLoopback(t *testing.T) {
	t.Parallel()
	a := &apiServer{ariaResponder: testSupervisor(t)}
	_ = a.ariaResponder.store.set(ariaResponderConfig{Agents: []ariaAgentConfig{
		{ID: "a1", AgentKey: testAgentKey, McpName: "aria-comms"},
	}})
	req := httptest.NewRequest(http.MethodPost, "/api/aria/mcp/aria-comms", strings.NewReader("{}"))
	req.RemoteAddr = "203.0.113.9:40000" // public IP
	req.SetPathValue("name", "aria-comms")
	rec := httptest.NewRecorder()
	a.handleAriaMcpProxy(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback code = %d, want 403", rec.Code)
	}

	// Loopback but relay-forwarded is also refused.
	req2 := httptest.NewRequest(http.MethodPost, "/api/aria/mcp/aria-comms", strings.NewReader("{}"))
	req2.RemoteAddr = "127.0.0.1:55555"
	req2.Header.Set("X-Forwarded-Prefix", "/m/somemachine")
	req2.SetPathValue("name", "aria-comms")
	rec2 := httptest.NewRecorder()
	a.handleAriaMcpProxy(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("relay-forwarded code = %d, want 403", rec2.Code)
	}

	// Unknown agent name → 404.
	req3 := httptest.NewRequest(http.MethodPost, "/api/aria/mcp/nope", strings.NewReader("{}"))
	req3.RemoteAddr = "127.0.0.1:55555"
	req3.SetPathValue("name", "nope")
	rec3 := httptest.NewRecorder()
	a.handleAriaMcpProxy(rec3, req3)
	if rec3.Code != http.StatusNotFound {
		t.Fatalf("unknown agent code = %d, want 404", rec3.Code)
	}
}
