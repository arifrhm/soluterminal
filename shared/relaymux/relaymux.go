package relaymux

import (
	"io"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

// InstanceHeader is the relay handshake response header that names the cloud
// instance holding the tunnel. The same random ID is reported as `instance` by
// the cloud's GET /api/health, so a daemon can notice that new traffic is
// being routed to a different instance (a deploy) while its tunnel still sits
// on the old one, and move its tunnel over. Older clouds omit it.
const InstanceHeader = "X-Solu-Cloud-Instance"

func YamuxConfig() *yamux.Config {
	c := yamux.DefaultConfig()
	c.KeepAliveInterval = 15 * time.Second
	c.ConnectionWriteTimeout = 20 * time.Second
	c.LogOutput = io.Discard
	return c
}

type Listener struct {
	Session *yamux.Session
}

func (l *Listener) Accept() (net.Conn, error) { return l.Session.Accept() }
func (l *Listener) Close() error              { return l.Session.Close() }
func (l *Listener) Addr() net.Addr            { return Addr{} }

type Addr struct{}

func (Addr) Network() string { return "yamux" }
func (Addr) String() string  { return "relay" }

type WebSocketAdapter struct {
	c  *websocket.Conn
	r  io.Reader
	mu sync.Mutex
}

func NewWebSocketAdapter(c *websocket.Conn) *WebSocketAdapter {
	c.SetReadLimit(0)
	return &WebSocketAdapter{c: c}
}

func (a *WebSocketAdapter) Read(p []byte) (int, error) {
	for {
		if a.r == nil {
			_, r, err := a.c.NextReader()
			if err != nil {
				return 0, err
			}
			a.r = r
		}
		n, err := a.r.Read(p)
		if err == io.EOF {
			a.r = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (a *WebSocketAdapter) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.c.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (a *WebSocketAdapter) Close() error { return a.c.Close() }
