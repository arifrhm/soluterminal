package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func shinoqueTestToken(t *testing.T, token string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SHINOQUE_HOME", home)
	if token != "" {
		if err := os.WriteFile(filepath.Join(home, "relay-token"), []byte(token+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func shinoqueTestProxy(t *testing.T, upstream http.Handler) (string, *shinoqueTokenCache, func()) {
	t.Helper()
	server := httptest.NewServer(upstream)
	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHINOQUE_RELAY_PORT", port)
	tokens := &shinoqueTokenCache{}
	proxy := httptest.NewServer(newShinoqueRelayHandler(http.NotFoundHandler(), tokens))
	return proxy.URL, tokens, func() { proxy.Close(); server.Close() }
}

func TestShinoqueRelayHeadersAndPrefixes(t *testing.T) {
	shinoqueTestToken(t, "server-token")
	seen := make(chan *http.Request, 2)
	base, _, closeServers := shinoqueTestProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts")
		w.Header().Set("Set-Cookie", "bad=1; Path=/")
		w.Header().Set("X-Upstream", "yes")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	}))
	defer closeServers()
	for _, prefix := range []string{"/m/machine_1", "/m/machine_1/b/wsl"} {
		req, _ := http.NewRequest(http.MethodPost, base+"/shinoque/api/write?x=1&access_token=jwt", strings.NewReader("body"))
		req.Header.Set("X-Forwarded-Prefix", prefix)
		req.Header.Set("X-Forwarded-For", "1.2.3.4")
		req.Header.Set("X-Forwarded-Host", "forged.test")
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Shinoque-Relay", "forged")
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("Cookie", "solu_session=secret")
		req.Header.Set("Origin", "https://cloud.test")
		req.Header.Set("Referer", "https://cloud.test/page")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Last-Event-ID", "42")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusCreated || string(body) != "ok" {
			t.Fatalf("response = %d %q", resp.StatusCode, body)
		}
		if resp.Header.Get("Content-Security-Policy") != "sandbox allow-scripts" || resp.Header.Get("X-Upstream") != "yes" || resp.Header.Get("Set-Cookie") != "" {
			t.Fatalf("response headers = %v", resp.Header)
		}
		got := <-seen
		if got.Method != http.MethodPost || got.Host != "127.0.0.1:"+os.Getenv("SHINOQUE_RELAY_PORT") || got.URL.Path != "/api/write" || got.URL.RawQuery != "x=1" {
			t.Fatalf("request = %s %s host=%s query=%q", got.Method, got.URL.Path, got.Host, got.URL.RawQuery)
		}
		if got.Header.Get("X-Forwarded-Prefix") != prefix+"/shinoque" || got.Header.Get("X-Shinoque-Relay") != "server-token" {
			t.Fatalf("relay headers = %v", got.Header)
		}
		for _, name := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Authorization", "Cookie"} {
			if got.Header.Get(name) != "" {
				t.Errorf("forwarded %s", name)
			}
		}
		for name, want := range map[string]string{"Origin": "https://cloud.test", "Referer": "https://cloud.test/page", "Sec-Fetch-Site": "same-origin", "Accept": "text/event-stream", "Last-Event-Id": "42"} {
			if got.Header.Get(name) != want {
				t.Errorf("%s = %q, want %q", name, got.Header.Get(name), want)
			}
		}
	}
}

func TestShinoqueRelayOnlyAndAvailability(t *testing.T) {
	home := shinoqueTestToken(t, "")
	hits := 0
	base, tokens, closeServers := shinoqueTestProxy(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits++; w.WriteHeader(200) }))
	defer closeServers()
	req, _ := http.NewRequest(http.MethodGet, base+"/shinoque/", nil)
	req.Header.Set("X-Forwarded-Prefix", "/m/m1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 503 || !strings.Contains(string(body), shinoqueUnavailable) || hits != 0 {
		t.Fatalf("missing token: %d %q hits=%d", resp.StatusCode, body, hits)
	}
	if err := os.WriteFile(filepath.Join(home, "relay-token"), []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	if tokens.read() != "first" {
		t.Fatal("token did not appear")
	}
	path := filepath.Join(home, "relay-token")
	if err := os.WriteFile(path, []byte("rotated"), 0600); err != nil {
		t.Fatal(err)
	}
	nextTime := time.Now().Add(time.Second)
	if err := os.Chtimes(path, nextTime, nextTime); err != nil {
		t.Fatal(err)
	}
	if tokens.read() != "rotated" {
		t.Fatal("token rotation not observed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if tokens.read() != "" {
		t.Fatal("removed token still available")
	}

	local := denyLocalShinoque(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	for _, path := range []string{"/shinoque", "/shinoque/", "/shinoque/ws"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-Forwarded-Prefix", "/m/m1")
		r.Header.Set("X-Shinoque-Relay", "forged")
		w := httptest.NewRecorder()
		local.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Errorf("local %s = %d", path, w.Code)
		}
	}
	bad := httptest.NewRequest(http.MethodGet, "/shinoque/", nil)
	bad.Header.Set("X-Forwarded-Prefix", "/m/m1/evil")
	w := httptest.NewRecorder()
	newShinoqueRelayHandler(http.NotFoundHandler(), tokens).ServeHTTP(w, bad)
	if w.Code != 400 {
		t.Fatalf("invalid prefix = %d", w.Code)
	}
}

func TestShinoqueRelaySSEFlush(t *testing.T) {
	shinoqueTestToken(t, "stream-token")
	release := make(chan struct{})
	base, _, closeServers := shinoqueTestProxy(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = fmt.Fprint(w, "data: second\n\n")
	}))
	defer closeServers()
	defer close(release)
	req, _ := http.NewRequest(http.MethodGet, base+"/shinoque/events", nil)
	req.Header.Set("X-Forwarded-Prefix", "/m/m1")
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "data: first\n" {
		t.Fatalf("first SSE event = %q, %v", line, err)
	}
}

func TestShinoqueRelayWebSocket(t *testing.T) {
	shinoqueTestToken(t, "ws-token")
	base, _, closeServers := shinoqueTestProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/socket" || r.Header.Get("X-Shinoque-Relay") != "ws-token" {
			http.Error(w, "bad upgrade", 400)
			return
		}
		conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		kind, msg, err := conn.ReadMessage()
		if err == nil {
			_ = conn.WriteMessage(kind, msg)
		}
	}))
	defer closeServers()
	header := http.Header{"X-Forwarded-Prefix": []string{"/m/m1/b/wsl"}, "Origin": []string{"https://cloud.test"}}
	conn, resp, err := websocket.DefaultDialer.Dial(strings.Replace(base, "http://", "ws://", 1)+"/shinoque/socket", header)
	if err != nil {
		t.Fatalf("websocket dial: %v response=%v", err, resp)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	_, msg, err := conn.ReadMessage()
	if err != nil || string(msg) != "hello" {
		t.Fatalf("echo = %q, %v", msg, err)
	}
}

func TestShinoqueHealthCapability(t *testing.T) {
	home := shinoqueTestToken(t, "")
	a := &apiServer{shinoqueTokens: &shinoqueTokenCache{}}
	for _, present := range []bool{false, true} {
		if present {
			if err := os.WriteFile(filepath.Join(home, "relay-token"), []byte("token"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		w := httptest.NewRecorder()
		a.health(w, httptest.NewRequest(http.MethodGet, "/api/health", nil))
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["shinoqueRelay"] != present {
			t.Fatalf("capability = %v, want %t", body["shinoqueRelay"], present)
		}
	}
}
