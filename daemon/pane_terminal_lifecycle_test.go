package main

import (
	"bytes"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// Test actual framed readLoop handling, not an activity-stamping helper. The
// next frame's header is read only after the previous payload is processed.
func TestTerminalReplayRetainsActivityAndTranscript(t *testing.T) {
	client, server := net.Pipe()
	var received atomic.Int64
	received.Store(1791181085982)
	p := &Pane{ID: "synthetic_replay", Status: PaneRunning, agentConn: client,
		Cols: 80, Rows: 24, buffer: NewRingBuffer(4096), subs: map[int64]*subEntry{},
		done: make(chan struct{}), LastActivityMs: 1791178668011,
		frameReceivedAt: func() time.Time { return time.UnixMilli(received.Load()) }}
	go p.readLoop(nil)
	t.Cleanup(func() { server.Close(); <-p.done })
	send := func(kind FrameType, payload []byte) {
		t.Helper()
		if err := WriteFrame(server, kind, payload); err != nil {
			t.Fatal(err)
		}
		// readLoop finishes kind before accepting this next header; then subsMu
		// ensures we don't inspect a buffer while an earlier write is in progress.
		if err := WriteFrame(server, FrameDims, []byte(`{"cols":80,"rows":24}`)); err != nil {
			t.Fatal(err)
		}
		p.subsMu.Lock()
		p.subsMu.Unlock()
	}
	a, b := []byte("historical transcript\r\n"), []byte("Worked for 10s · 13:37\r\n")
	send(FrameReplay, a)
	send(FrameReplay, b)
	send(FrameSyntheticPaint, []byte("\x1b[2Jsynthetic paint"))
	send(FrameReplayEnd, nil)
	if got := atomic.LoadInt64(&p.LastActivityMs); got != 1791178668011 {
		t.Fatalf("replay reset completion-related activity: %d", got)
	}
	if got := p.buffer.Snapshot(); !bytes.Equal(got, append(a, b...)) {
		t.Fatalf("historical bytes lost or paint added: %q", got)
	}
	if got := atomic.LoadInt64(&p.TotalStrippedChars); got != int64(countStrippedChars(a)+countStrippedChars(b)) {
		t.Fatalf("stripped positions changed: %d", got)
	}
	send(FrameData, []byte("new live output\r\n"))
	if got := atomic.LoadInt64(&p.LastActivityMs); got != received.Load() {
		t.Fatalf("live data didn't stamp injected reception: %d", got)
	}
	received.Add(1000)
	send(FrameReplay, []byte("older replay after live\r\n"))
	if got := atomic.LoadInt64(&p.LastActivityMs); got != 1791181085982 {
		t.Fatalf("later replay reset live activity: %d", got)
	}
}
