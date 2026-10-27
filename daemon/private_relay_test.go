package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// privateRelayFixture is a daemon with one approved device, serving the
// private relay endpoint as the relay tunnel would (markRelayTransport).
func privateRelayFixture(t *testing.T) (*apiServer, testDevice, *httptest.Server) {
	t.Helper()
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
		_, _ = w.Write([]byte(r.Method + " " + string(body)))
	})
	inner.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			mt, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			_ = c.WriteMessage(mt, append([]byte("echo:"), msg...))
		}
	})
	inner.HandleFunc("/api/early", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		_, _ = w.Write([]byte("no thanks")) // answers without reading the body
	})
	inner.HandleFunc("/api/stuck", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // never reads the body
	})
	inner.HandleFunc("/api/big", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		_, _ = w.Write([]byte(strings.Repeat("z", 3_000_000)))
	})
	a.privateRelayInner = markRelayTransport(inner)
	srv := httptest.NewServer(markRelayTransport(http.HandlerFunc(a.handlePrivateRelay)))
	t.Cleanup(srv.Close)
	return a, d, srv
}

func dialPrivateRelay(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+privateRelayPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return ws
}

func (d testDevice) signer(t *testing.T) func([]byte) string {
	return func(msg []byte) string { return d.sign(t, msg) }
}

func openPrivateClient(t *testing.T, a *apiServer, d testDevice, srv *httptest.Server) (*privateMux, func(context.Context, string, string) (net.Conn, error), *http.Client) {
	t.Helper()
	ws := dialPrivateRelay(t, srv)
	rec, err := privateClientHandshake(ws, deviceID(d.spki), testMachine+"/wsl", d.signer(t), a.trustedDevices.daemonSPKI(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	mux := newPrivateMux(rec, false)
	go func() { _ = mux.run() }()
	dial := func(context.Context, string, string) (net.Conn, error) { return mux.open() }
	return mux, dial, &http.Client{Transport: &http.Transport{DialContext: dial}, Timeout: 10 * time.Second}
}

func TestPrivateRelayCarriesHTTPAndWebSocket(t *testing.T) {
	a, d, srv := privateRelayFixture(t)
	_, dial, client := openPrivateClient(t, a, d, srv)

	resp, err := client.Get("http://private/api/echo")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "GET " {
		t.Fatalf("GET through the channel = %q (%d)", body, resp.StatusCode)
	}
	resp, err = client.Post("http://private/api/echo", "text/plain", strings.NewReader(strings.Repeat("x", 100_000)))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(body) != len("POST ")+100_000 {
		t.Fatalf("POST through the channel returned %d bytes", len(body))
	}

	wsc, _, err := (&websocket.Dialer{NetDialContext: dial}).Dial("ws://private/api/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer wsc.Close()
	if err := wsc.WriteMessage(websocket.BinaryMessage, []byte("keys")); err != nil {
		t.Fatal(err)
	}
	_, msg, err := wsc.ReadMessage()
	if err != nil || string(msg) != "echo:keys" {
		t.Fatalf("websocket through the channel = %q, %v", msg, err)
	}
}

func TestPrivateRelayRefusesWhatItShould(t *testing.T) {
	a, d, srv := privateRelayFixture(t)
	pinned := a.trustedDevices.daemonSPKI()
	refused := func(name, device, target string, sign func([]byte) string, at time.Time) {
		t.Helper()
		ws := dialPrivateRelay(t, srv)
		if _, err := privateClientHandshake(ws, device, target, sign, pinned, at); err == nil {
			t.Fatalf("%s: handshake succeeded", name)
		}
	}
	other := newTestDevice(t)
	refused("unapproved device", deviceID(other.spki), testMachine+"/wsl", other.signer(t), time.Now())
	refused("signature by another key", deviceID(d.spki), testMachine+"/wsl", other.signer(t), time.Now())
	refused("another machine", deviceID(d.spki), "mach_other/wsl", d.signer(t), time.Now())
	refused("stale", deviceID(d.spki), testMachine+"/wsl", d.signer(t), time.Now().Add(-5*time.Minute))

	// The device refuses a computer whose identity isn't the one it pinned.
	ws := dialPrivateRelay(t, srv)
	if _, err := privateClientHandshake(ws, deviceID(d.spki), testMachine+"/wsl", d.signer(t), other.spki, time.Now()); err == nil ||
		!strings.Contains(err.Error(), "identity") {
		t.Fatalf("mismatched identity: %v", err)
	}
}

func TestPrivateRelayRequiresTheRelayTransport(t *testing.T) {
	a, _, _ := privateRelayFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(a.handlePrivateRelay)) // no markRelayTransport
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("local private relay request = %d", resp.StatusCode)
	}
}

// A record changed in flight fails authentication.
func TestPrivateRecordsRejectTampering(t *testing.T) {
	shared := make([]byte, 32)
	c2s, s2c, err := privateKeys(shared, []byte("hello"), []byte("reply"))
	if err != nil {
		t.Fatal(err)
	}
	sealed := c2s.Seal(nil, privateNonce(0), []byte("rm -rf /"), []byte(privateRelayVersion))
	sealed[3] ^= 1
	if _, err := c2s.Open(nil, privateNonce(0), sealed, []byte(privateRelayVersion)); err == nil {
		t.Fatal("tampered record opened")
	}
	ok := c2s.Seal(nil, privateNonce(0), []byte("ls"), []byte(privateRelayVersion))
	if _, err := c2s.Open(nil, privateNonce(1), ok, []byte(privateRelayVersion)); err == nil {
		t.Fatal("record replayed at another position opened")
	}
	if _, err := s2c.Open(nil, privateNonce(0), ok, []byte(privateRelayVersion)); err == nil {
		t.Fatal("record reflected to the other direction opened")
	}
	// Different handshake bytes give different keys.
	other, _, _ := privateKeys(shared, []byte("hello!"), []byte("reply"))
	if _, err := other.Open(nil, privateNonce(0), ok, []byte(privateRelayVersion)); err == nil {
		t.Fatal("keys did not depend on the handshake transcript")
	}
}

// The server ending a stream while the client is still sending into it must
// not crash the daemon (the stream's inbound queue used to be closed under a
// concurrent send).
func TestPrivateRelaySurvivesStreamsClosedMidSend(t *testing.T) {
	a, d, srv := privateRelayFixture(t)
	_, _, client := openPrivateClient(t, a, d, srv)
	for i := 0; i < 20; i++ {
		resp, err := client.Post("http://private/api/early", "text/plain", strings.NewReader(strings.Repeat("y", 2_000_000)))
		if err == nil {
			resp.Body.Close()
		}
	}
	resp, err := client.Get("http://private/api/echo")
	if err != nil {
		t.Fatalf("channel unusable after early-closing streams: %v", err)
	}
	resp.Body.Close()
}

// Removing a device ends its live channel, WebSocket included.
func TestPrivateRelayEndsWhenTheDeviceIsRemoved(t *testing.T) {
	a, d, srv := privateRelayFixture(t)
	mux, dial, client := openPrivateClient(t, a, d, srv)
	wsc, _, err := (&websocket.Dialer{NetDialContext: dial}).Dial("ws://private/api/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer wsc.Close()
	if err := a.trustedDevices.remove(deviceID(d.spki)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-mux.done:
	case <-time.After(5 * time.Second):
		t.Fatal("channel still open after the device was removed")
	}
	_ = wsc.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := wsc.ReadMessage(); err == nil {
		t.Fatal("WebSocket inside the channel survived removal")
	}
	if resp, err := client.Get("http://private/api/echo"); err == nil {
		resp.Body.Close()
		t.Fatal("request succeeded after removal")
	}
}

// A stream whose reader stalls is reset on its own; the rest of the channel
// keeps working (no head-of-line freeze).
func TestPrivateRelayStalledStreamDoesNotFreezeTheChannel(t *testing.T) {
	a, d, srv := privateRelayFixture(t)
	_, _, client := openPrivateClient(t, a, d, srv)
	go func() {
		resp, err := client.Post("http://private/api/stuck", "text/plain", strings.NewReader(strings.Repeat("p", 8_000_000)))
		if err == nil {
			resp.Body.Close()
		}
	}()
	time.Sleep(300 * time.Millisecond)
	for i := 0; i < 5; i++ {
		resp, err := client.Get("http://private/api/echo")
		if err != nil {
			t.Fatalf("channel froze behind a stalled stream: %v", err)
		}
		resp.Body.Close()
	}
}

// The end of a response sent with Connection: close arrives in full even
// though the close frame follows right behind it.
func TestPrivateRelayDeliversDataQueuedBeforeClose(t *testing.T) {
	a, d, srv := privateRelayFixture(t)
	_, _, client := openPrivateClient(t, a, d, srv)
	for i := 0; i < 10; i++ {
		resp, err := client.Get("http://private/api/big")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || len(body) != 3_000_000 {
			t.Fatalf("round %d: got %d bytes, %v", i, len(body), err)
		}
	}
}

// A reader slower than the sender gets every byte: the sender waits for
// credit instead of overrunning the receiver (which used to reset it).
func TestPrivateRelaySlowReaderGetsEverything(t *testing.T) {
	a, d, srv := privateRelayFixture(t)
	_, _, client := openPrivateClient(t, a, d, srv)
	resp, err := client.Get("http://private/api/big")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	total, buf := 0, make([]byte, 16<<10)
	for {
		n, err := resp.Body.Read(buf)
		total += n
		if total < 600_000 {
			time.Sleep(2 * time.Millisecond) // lag well behind the sender
		}
		if err != nil {
			break
		}
	}
	if total != 3_000_000 {
		t.Fatalf("slow reader got %d of 3000000 bytes", total)
	}
}

// Data beyond the granted credit resets just that stream.
func TestPrivateMuxResetsAStreamThatIgnoresCredit(t *testing.T) {
	m := &privateMux{streams: map[uint32]*privateStream{}}
	st := &privateStream{wake: make(chan struct{}, 1), gone: make(chan struct{}), credit: privateStreamWindow, creditWake: make(chan struct{}, 1)}
	if !m.enqueue(st, make([]byte, privateStreamWindow)) {
		t.Fatal("data within the window refused")
	}
	if m.enqueue(st, []byte{1}) {
		t.Fatal("data beyond the window accepted")
	}
}

// The computer answers a device's ping, so the device can tell a dead
// connection from an idle one.
func TestPrivateRelayAnswersPings(t *testing.T) {
	a, d, srv := privateRelayFixture(t)
	ws := dialPrivateRelay(t, srv)
	rec, err := privateClientHandshake(ws, deviceID(d.spki), testMachine+"/wsl", d.signer(t), a.trustedDevices.daemonSPKI(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.send([]byte{0, 0, 0, 0, privateFramePing}); err != nil {
		t.Fatal(err)
	}
	got := make(chan []byte, 1)
	go func() {
		r, err := rec.recv()
		if err == nil {
			got <- r
		}
	}()
	select {
	case r := <-got:
		if len(r) < 5 || r[4] != privateFramePing {
			t.Fatalf("reply = %v, want a ping frame", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no reply to the ping")
	}
}

// A page that navigates away mid-handshake (1001) or drops without a close
// frame (1006) is not logged as a refusal, while a bad hello still is.
func TestPrivateRelayGoingAwayIsNotARefusal(t *testing.T) {
	_, _, srv := privateRelayFixture(t)
	logs := captureLog(t)
	waitLog := func(want string) bool {
		for i := 0; i < 50; i++ {
			if strings.Contains(logs.String(), want) {
				return true
			}
			time.Sleep(20 * time.Millisecond)
		}
		return false
	}

	ws := dialPrivateRelay(t, srv)
	_ = ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseGoingAway, ""))
	_ = ws.Close()
	if waitLog("handshake refused") {
		t.Fatalf("a page going away was logged as a refusal: %s", logs.String())
	}

	dropped := dialPrivateRelay(t, srv)
	_ = dropped.Close() // no close frame: the tab was killed
	if waitLog("handshake refused") {
		t.Fatalf("a dropped tab was logged as a refusal: %s", logs.String())
	}

	bad := dialPrivateRelay(t, srv)
	_ = bad.WriteMessage(websocket.TextMessage, []byte(`{"v":9}`))
	if !waitLog("handshake refused") {
		t.Fatalf("a bad hello was not logged: %s", logs.String())
	}
}
