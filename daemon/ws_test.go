package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func newWSTestHarness(t *testing.T) (*Pane, *httptest.Server) {
	t.Helper()
	p := &Pane{
		ID:     "p1",
		Cols:   80,
		Rows:   24,
		buffer: NewRingBuffer(ringBufferBytes),
		subs:   map[int64]*subEntry{},
	}
	m := &Manager{panes: map[string]*Pane{"p1": p}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/panes/{id}/io", handleWS(m))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return p, srv
}

func dialWSTest(t *testing.T, srv *httptest.Server, query string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/panes/p1/io" + query
	header := http.Header{"Origin": []string{srv.URL}}
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial pane websocket: %v (HTTP %s)", err, resp.Status)
		}
		t.Fatalf("dial pane websocket: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func wsTestSubCount(p *Pane) int {
	p.subsMu.RLock()
	defer p.subsMu.RUnlock()
	return len(p.subs)
}

func waitForWSTestSubCount(t *testing.T, p *Pane, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if got := wsTestSubCount(p); got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("subscriber count = %d after %s; want %d", wsTestSubCount(p), timeout, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readWSTestUntilError(conn *websocket.Conn) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				errCh <- err
				return
			}
		}
	}()
	return errCh
}

func TestWSIdleAliveSurvives(t *testing.T) {
	oldPingInterval, oldReadDeadline := wsPingInterval, wsReadDeadline
	wsPingInterval = 50 * time.Millisecond
	wsReadDeadline = 250 * time.Millisecond
	t.Cleanup(func() {
		wsPingInterval = oldPingInterval
		wsReadDeadline = oldReadDeadline
	})

	p, srv := newWSTestHarness(t)
	conn := dialWSTest(t, srv, "")
	readErr := readWSTestUntilError(conn) // Gorilla's default handler auto-pongs while reading.
	waitForWSTestSubCount(t, p, 1, time.Second)

	time.Sleep(time.Second) // Four read-deadline windows and twenty ping ticks.
	select {
	case err := <-readErr:
		t.Fatalf("idle-alive websocket closed: %v", err)
	default:
	}
	if got := wsTestSubCount(p); got != 1 {
		t.Fatalf("subscriber count after idle keepalive = %d; want 1", got)
	}
}

func TestWSAppLevelPingExtendsDeadline(t *testing.T) {
	oldPingInterval, oldReadDeadline := wsPingInterval, wsReadDeadline
	wsPingInterval = 50 * time.Millisecond
	// Host-load headroom: the app heartbeat ticks every 100ms, so a 250ms
	// deadline leaves only ~150ms of scheduling margin and flakes under a
	// loaded full-suite race run (observed close 1006). A 500ms deadline
	// keeps the proof intact (without app frames the socket still dies well
	// before the 2s observation window) with ~400ms of margin.
	wsReadDeadline = 500 * time.Millisecond
	t.Cleanup(func() {
		wsPingInterval = oldPingInterval
		wsReadDeadline = oldReadDeadline
	})

	p, srv := newWSTestHarness(t)
	conn := dialWSTest(t, srv, "")
	conn.SetPingHandler(func(string) error { return nil }) // Consume server pings without a WS pong.
	readErr := readWSTestUntilError(conn)
	waitForWSTestSubCount(t, p, 1, time.Second)

	stopPings := make(chan struct{})
	writeErr := make(chan error, 1)
	t.Cleanup(func() { close(stopPings) })
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopPings:
				return
			case <-ticker.C:
				if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`)); err != nil {
					writeErr <- err
					return
				}
			}
		}
	}()

	time.Sleep(2 * time.Second) // Four read-deadline windows of app heartbeats.
	select {
	case err := <-readErr:
		t.Fatalf("app-heartbeat websocket closed: %v", err)
	case err := <-writeErr:
		t.Fatalf("app heartbeat write failed: %v", err)
	default:
	}
	if got := wsTestSubCount(p); got != 1 {
		t.Fatalf("subscriber count with app heartbeats = %d; want 1", got)
	}
}

func TestWSMobileToleratesBackgrounding(t *testing.T) {
	oldPingInterval := wsPingInterval
	oldReadDeadline, oldMobileReadDeadline := wsReadDeadline, wsMobileReadDeadline
	wsPingInterval = 50 * time.Millisecond
	wsReadDeadline = 200 * time.Millisecond
	wsMobileReadDeadline = time.Second
	t.Cleanup(func() {
		wsPingInterval = oldPingInterval
		wsReadDeadline = oldReadDeadline
		wsMobileReadDeadline = oldMobileReadDeadline
	})

	p, srv := newWSTestHarness(t)
	conn := dialWSTest(t, srv, "?mobile=1")
	conn.SetPingHandler(func(string) error { return nil }) // Simulate a suspended app: no pong or app ping.
	readErr := readWSTestUntilError(conn)
	waitForWSTestSubCount(t, p, 1, time.Second)

	time.Sleep(600 * time.Millisecond)
	if got := wsTestSubCount(p); got != 1 {
		t.Fatalf("mobile subscriber was reaped at desktop deadline: count=%d", got)
	}

	waitForWSTestSubCount(t, p, 0, 1200*time.Millisecond)
	select {
	case <-readErr:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("mobile client did not observe close after mobile read deadline")
	}
}

func TestWSTeardownNotQuantizedToPingTicker(t *testing.T) {
	oldPingInterval, oldReadDeadline := wsPingInterval, wsReadDeadline
	wsPingInterval = 10 * time.Second
	wsReadDeadline = time.Second
	t.Cleanup(func() {
		wsPingInterval = oldPingInterval
		wsReadDeadline = oldReadDeadline
	})

	p, srv := newWSTestHarness(t)
	conn := dialWSTest(t, srv, "")
	waitForWSTestSubCount(t, p, 1, time.Second)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read initial replay frame: %v", err)
	}
	if err := conn.NetConn().Close(); err != nil {
		t.Fatalf("force-close websocket TCP connection: %v", err)
	}

	// Before readerDone woke the writer, this deregistration waited for the
	// deliberately-long 10s ping tick and failed this one-second bound.
	waitForWSTestSubCount(t, p, 0, time.Second)
}

func TestStripTerminalQuerySequences(t *testing.T) {
	t.Parallel()
	esc := "\x1b"

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "DA1 query bare",
			input: esc + "[c",
			want:  "",
		},
		{
			name:  "DA1 query with param",
			input: esc + "[0c",
			want:  "",
		},
		{
			name:  "DA2 query",
			input: esc + "[>c",
			want:  "",
		},
		{
			name:  "DA3 query",
			input: esc + "[=c",
			want:  "",
		},
		{
			name:  "CPR request",
			input: esc + "[6n",
			want:  "",
		},
		{
			name:  "DSR request",
			input: esc + "[5n",
			want:  "",
		},
		{
			name:  "private DSR",
			input: esc + "[?6n",
			want:  "",
		},
		{
			name:  "normal text unchanged",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "SGR (color) unchanged",
			input: esc + "[32mgreen" + esc + "[0m",
			want:  esc + "[32mgreen" + esc + "[0m",
		},
		{
			name:  "cursor movement unchanged",
			input: esc + "[2A" + esc + "[10;1H",
			want:  esc + "[2A" + esc + "[10;1H",
		},
		{
			name:  "DA query stripped, surrounding text kept",
			input: "before" + esc + "[c" + "after",
			want:  "beforeafter",
		},
		{
			name:  "multiple DA queries stripped",
			input: esc + "[c" + esc + "[>c" + esc + "[c",
			want:  "",
		},
		{
			name:  "DA mixed with color",
			input: esc + "[32m" + esc + "[c" + "text" + esc + "[0m",
			want:  esc + "[32m" + "text" + esc + "[0m",
		},
		{
			name: "erased DA1 reply (xterm response) — kept, not a query",
			// xterm.js replies with ESC[?1;2c — final byte is 'c' so this
			// WILL be stripped. That's fine: if it's in the ring buffer it
			// means a previous terminal emulator wrote it as input; we don't
			// want to replay it either.
			input: esc + "[?1;2c",
			want:  "",
		},
		{
			name:  "erase-in-line (CSI K) unchanged",
			input: esc + "[2K",
			want:  esc + "[2K",
		},
		{
			name:  "OSC 10 foreground query stripped",
			input: "a" + esc + "]10;?" + esc + "\\b",
			want:  "ab",
		},
		{
			name:  "OSC 11 background query stripped",
			input: "a" + esc + "]11;?\x07b",
			want:  "ab",
		},
		{
			name:  "OSC 12 cursor color query stripped",
			input: "a" + esc + "]12;?" + esc + "\\b",
			want:  "ab",
		},
		{
			name:  "OSC 4 palette query stripped",
			input: "a" + esc + "]4;1;?" + esc + "\\b",
			want:  "ab",
		},
		{
			name:  "OSC color set unchanged",
			input: "a" + esc + "]11;rgb:0000/0000/0000" + esc + "\\b",
			want:  "a" + esc + "]11;rgb:0000/0000/0000" + esc + "\\b",
		},
		{
			name:  "OSC title unchanged",
			input: "a" + esc + "]0;soluterminal\x07b",
			want:  "a" + esc + "]0;soluterminal\x07b",
		},
		{
			name:  "truncated ESC kept",
			input: "\x1b",
			want:  "\x1b",
		},
		{
			name:  "truncated CSI kept",
			input: "\x1b[",
			want:  "\x1b[",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := string(stripTerminalQuerySequences([]byte(tc.input)))
			if got != tc.want {
				t.Errorf("stripTerminalQuerySequences(%q) = %q; want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseTerminalQueryDimensions(t *testing.T) {
	t.Parallel()
	cols, rows, ok := parseTerminalQueryDimensions("132", "43")
	if !ok || cols != 132 || rows != 43 {
		t.Fatalf("normal query dimensions = %dx%d ok=%v, want 132x43 ok=true", cols, rows, ok)
	}

	cols, rows, ok = parseTerminalQueryDimensions("999999", "999999")
	if !ok {
		t.Fatal("large query dimensions rejected instead of capped")
	}
	if cols != maxTerminalCols || rows != maxTerminalRows {
		t.Fatalf("large query dimensions = %dx%d, want capped %dx%d",
			cols, rows, maxTerminalCols, maxTerminalRows)
	}

	if _, _, ok := parseTerminalQueryDimensions("0", "24"); ok {
		t.Fatal("zero cols accepted")
	}
	if _, _, ok := parseTerminalQueryDimensions("abc", "24"); ok {
		t.Fatal("invalid cols accepted")
	}
}
