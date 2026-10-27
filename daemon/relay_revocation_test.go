package main

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A relay fixture: the gate in front of a WebSocket echo and an event stream,
// served as the relay tunnel would (markRelayTransport).
func relayRevocationFixture(t *testing.T) (*apiServer, testDevice, *httptest.Server) {
	t.Helper()
	a := trustedDevicesTestServer(t)
	d := newTestDevice(t)
	approveTestDevice(t, a, d)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
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
			_ = c.WriteMessage(mt, msg)
		}
	})
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: hello\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	srv := httptest.NewServer(markRelayTransport(a.relayDeviceGate(mux)))
	t.Cleanup(srv.Close)
	return a, d, srv
}

func wsURL(srv *httptest.Server, path string) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + path
}

func expectWSClosed(t *testing.T, c *websocket.Conn, what string) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, _, err := c.ReadMessage(); err != nil {
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				t.Fatalf("%s: connection still open after access was withdrawn", what)
			}
			return
		}
	}
}

// Turning on "Only devices I approve" ends connections opened unsigned while
// the policy was "any": not only new requests are refused.
func TestApprovedOnlyClosesOpenUnsignedRelayConnections(t *testing.T) {
	a, _, srv := relayRevocationFixture(t)
	ws, _, err := websocket.DefaultDialer.Dial(wsURL(srv, "/api/ws"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	if err := ws.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	if _, msg, err := ws.ReadMessage(); err != nil || string(msg) != "ping" {
		t.Fatalf("echo before the switch = %q, %v", msg, err)
	}
	resp, err := http.Get(srv.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	events := bufio.NewReader(resp.Body)
	if line, _ := events.ReadString('\n'); !strings.HasPrefix(line, "data: hello") {
		t.Fatalf("event stream before the switch = %q", line)
	}

	if err := a.trustedDevices.setPolicy(remoteAccessApproved); err != nil {
		t.Fatal(err)
	}
	expectWSClosed(t, ws, "unsigned WebSocket")
	done := make(chan error, 1)
	go func() {
		for {
			if _, err := events.ReadString('\n'); err != nil {
				done <- err
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("unsigned event stream still open after approved-only was turned on")
	}
}

// Removing a device ends its open relay connections.
func TestRemovingADeviceClosesItsRelayConnections(t *testing.T) {
	a, d, srv := relayRevocationFixture(t)
	if err := a.trustedDevices.setPolicy(remoteAccessApproved); err != nil {
		t.Fatal(err)
	}
	signed := d.signedRequest(t, "GET", "/api/ws", "", time.Now(), "ws-1")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL(srv, "/api/ws"), signed.Header)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	if err := ws.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	if _, msg, err := ws.ReadMessage(); err != nil || string(msg) != "ping" {
		t.Fatalf("signed echo = %q, %v", msg, err)
	}
	other := newTestDevice(t)
	approveTestDevice(t, a, other)
	_ = a.trustedDevices.remove(deviceID(other.spki)) // someone else: stays open
	if err := ws.WriteMessage(websocket.TextMessage, []byte("still")); err != nil {
		t.Fatal(err)
	}
	if _, msg, err := ws.ReadMessage(); err != nil || string(msg) != "still" {
		t.Fatalf("removing another device closed this one: %q, %v", msg, err)
	}
	if err := a.trustedDevices.remove(deviceID(d.spki)); err != nil {
		t.Fatal(err)
	}
	expectWSClosed(t, ws, "removed device's WebSocket")
}

// With only approved devices allowed, reminder names don't reach the cloud.
func TestReminderSyncHidesNamesWhenPrivate(t *testing.T) {
	items := []reminderSyncItem{{SchedID: "s1", Name: "call the lawyer", RunAt: time.Unix(1_900_000_000, 0)}}
	p := &Pusher{}
	if got := p.reminderPayload(items); got[0].Name != "call the lawyer" {
		t.Fatalf("name hidden without the private policy: %+v", got)
	}
	p.private = func() bool { return true }
	got := p.reminderPayload(items)
	if got[0].Name == "call the lawyer" || got[0].SchedID != "s1" || !got[0].RunAt.Equal(items[0].RunAt) {
		t.Fatalf("private payload = %+v", got)
	}
	if items[0].Name != "call the lawyer" {
		t.Fatal("reminderPayload modified the caller's items")
	}
}

// An approved device that signs while the policy is still "any" keeps its
// connection when approved-only is turned on; unsigned ones are cut.
func TestApprovedOnlyKeepsSignedConnectionsOfApprovedDevices(t *testing.T) {
	a, d, srv := relayRevocationFixture(t)
	signed := d.signedRequest(t, "GET", "/api/ws", "", time.Now(), "ws-any-1")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL(srv, "/api/ws"), signed.Header)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	unsigned, _, err := websocket.DefaultDialer.Dial(wsURL(srv, "/api/ws"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unsigned.Close()
	if err := a.trustedDevices.setPolicy(remoteAccessApproved); err != nil {
		t.Fatal(err)
	}
	expectWSClosed(t, unsigned, "unsigned WebSocket")
	if err := ws.WriteMessage(websocket.TextMessage, []byte("kept")); err != nil {
		t.Fatal(err)
	}
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, msg, err := ws.ReadMessage(); err != nil || string(msg) != "kept" {
		t.Fatalf("approved device's connection was cut: %q, %v", msg, err)
	}
}
