package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// The browser client (web/src/lib/privateRelay) against this daemon's real
// private relay endpoint: the TypeScript channel, HTTP client, event stream
// reader and WebSocket client run in Node (vitest) and talk to the handler
// below through the actual handshake, records and mux. Opt-in because it
// needs the web dependencies installed:
//
//	SOLU_PRIVATE_RELAY_WEB_E2E=1 go test -run TestPrivateRelayWebClientEndToEnd .
func TestPrivateRelayWebClientEndToEnd(t *testing.T) {
	if os.Getenv("SOLU_PRIVATE_RELAY_WEB_E2E") != "1" {
		t.Skip("set SOLU_PRIVATE_RELAY_WEB_E2E=1 (needs web/node_modules)")
	}
	a := trustedDevicesTestServer(t)
	d := newTestDevice(t)
	approveTestDevice(t, a, d)

	inner := http.NewServeMux()
	inner.HandleFunc("/api/echo", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !relayTransport(r) || privateRelayDevice(r) != deviceID(d.spki) {
			http.Error(w, "missing private relay context", 500)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Echo-Prefix", r.Header.Get("X-Forwarded-Prefix"))
		w.Header().Set("X-Echo-Auth", r.Header.Get("Authorization"))
		w.Header().Set("X-Echo-Query", r.URL.RawQuery)
		w.Header().Set("X-Echo-Type", r.Header.Get("Content-Type"))
		w.Header().Set("Content-Length", fmt.Sprint(len(r.Method)+1+len(body)))
		_, _ = w.Write([]byte(r.Method + " " + string(body)))
	})
	inner.HandleFunc("/api/chunked", func(w http.ResponseWriter, r *http.Request) {
		f := w.(http.Flusher)
		for i := 0; i < 5; i++ {
			fmt.Fprintf(w, "part-%d;", i)
			f.Flush()
		}
	})
	inner.HandleFunc("/api/ui/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		fmt.Fprint(w, ": hello\n\n")
		f.Flush()
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "data: {\"type\":\"tick\",\"n\":%d}\n\n", i)
			f.Flush()
			select {
			case <-time.After(20 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
	})
	inner.HandleFunc("/api/flood", func(w http.ResponseWriter, r *http.Request) {
		// 4 MiB, far beyond a stream's credit: a client that stops reading
		// makes this handler wait, and gets every byte once it reads again.
		w.Header().Set("Content-Length", fmt.Sprint(128*(32<<10)))
		for i := 0; i < 128; i++ {
			chunk := []byte(strings.Repeat(string(rune('a'+i%26)), 32<<10))
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	inner.HandleFunc("/api/upload", func(w http.ResponseWriter, r *http.Request) {
		// Reads slowly, so the client's sends have to wait for credit.
		buf := make([]byte, 16<<10)
		total, sum := 0, 0
		for {
			n, err := r.Body.Read(buf)
			for _, b := range buf[:n] {
				sum = (sum*31 + int(b)) % 1000000007
			}
			total += n
			if total%(512<<10) < n {
				time.Sleep(10 * time.Millisecond)
			}
			if err != nil {
				break
			}
		}
		fmt.Fprintf(w, "%d %d", total, sum)
	})
	inner.HandleFunc("/api/remove-me", func(w http.ResponseWriter, r *http.Request) {
		// Stands in for the person removing this device at the computer.
		go func() {
			time.Sleep(50 * time.Millisecond)
			_ = a.trustedDevices.remove(privateRelayDevice(r))
		}()
		w.WriteHeader(http.StatusAccepted)
	})
	inner.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{Subprotocols: []string{"solu-auth"}, CheckOrigin: originProtected}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteMessage(websocket.TextMessage, []byte("hello:"+r.URL.RawQuery))
		for {
			mt, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			if string(msg) == "bye" {
				_ = c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4001, "server says bye"))
				return
			}
			_ = c.WriteMessage(mt, append([]byte("echo:"), msg...))
		}
	})
	a.privateRelayInner = markRelayTransport(inner)

	// Two more daemons standing in for this computer after a restart with a
	// new identity (a reinstall, or someone posing as it): same machine and
	// backend, the same approved device, other identity keys.
	daemons := map[string]*apiServer{"1": a}
	for _, n := range []string{"2", "3"} {
		other := trustedDevicesTestServer(t)
		approveTestDevice(t, other, d)
		other.privateRelayInner = markRelayTransport(inner)
		daemons[n] = other
	}
	// The relay in front: forwards the channel to the current daemon, or
	// answers 503 while it is "down". /control/* lets the web test restart it.
	var (
		mu      sync.Mutex
		current = a
		down    bool
		conns   = map[net.Conn]struct{}{}
	)
	front := http.NewServeMux()
	front.Handle(privateRelayPath, markRelayTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cur, isDown := current, down
		mu.Unlock()
		if isDown {
			http.Error(w, "machine offline", http.StatusServiceUnavailable)
			return
		}
		cur.handlePrivateRelay(w, r)
	})))
	front.HandleFunc("/control/down", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		down = true
		open := make([]net.Conn, 0, len(conns))
		for c := range conns {
			open = append(open, c)
		}
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		go func() { // after this response: every channel (and its sockets) drops
			time.Sleep(50 * time.Millisecond)
			for _, c := range open {
				_ = c.Close()
			}
		}()
	})
	front.HandleFunc("/control/up", func(w http.ResponseWriter, r *http.Request) {
		next := daemons[r.URL.Query().Get("daemon")]
		if next == nil {
			http.Error(w, "no such daemon", http.StatusBadRequest)
			return
		}
		mu.Lock()
		current, down = next, false
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewUnstartedServer(front)
	srv.Config.ConnState = func(c net.Conn, st http.ConnState) {
		mu.Lock()
		defer mu.Unlock()
		if st == http.StateNew {
			conns[c] = struct{}{}
		} else if st == http.StateClosed {
			delete(conns, c)
		}
	}
	srv.Start()
	defer srv.Close()

	devD, _ := d.key.Bytes()
	devPub, _ := d.key.PublicKey.Bytes()
	b64 := base64.RawURLEncoding.EncodeToString
	cfg := map[string]any{
		"url":      "ws" + strings.TrimPrefix(srv.URL, "http") + privateRelayPath,
		"deviceId": deviceID(d.spki),
		"target":   testMachine + "/wsl",
		"pinned":   a.trustedDevices.daemonSPKI(),
		"pinned2":  daemons["2"].trustedDevices.daemonSPKI(),
		"control":  srv.URL,
		"deviceJwk": map[string]string{
			"kty": "EC", "crv": "P-256", "d": b64(devD), "x": b64(devPub[1:33]), "y": b64(devPub[33:]),
		},
	}
	raw, _ := json.Marshal(cfg)
	cfgPath := filepath.Join(t.TempDir(), "private-relay-e2e.json")
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	web, _ := filepath.Abs("../web")
	// One file at a time: the second restarts the daemon behind the relay.
	for _, file := range []string{"src/lib/privateRelay/e2e.test.ts", "src/lib/privateRelay/e2eStrict.test.ts"} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		cmd := exec.CommandContext(ctx, "npx", "vitest", "run", file)
		cmd.Dir = web
		cmd.Env = append(os.Environ(), "SOLU_PRIVATE_RELAY_E2E_CONFIG="+cfgPath)
		out, err := cmd.CombinedOutput()
		cancel()
		t.Logf("vitest %s:\n%s", file, out)
		if err != nil {
			t.Fatalf("web client end to end (%s): %v", file, err)
		}
		if strings.Contains(string(out), "skipped") && !strings.Contains(string(out), "passed") {
			t.Fatalf("web client end to end (%s) did not run", file)
		}
	}
}
