package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// native_ws.go — WebSocket transport for native-renderer panes. A drop-in
// replacement for the SSE stream (native_api.go paneNativeStream).
//
// Why WS instead of SSE: the desktop in Local mode talks to the daemon over
// loopback HTTP/1.1, where the browser caps concurrent connections per origin at
// ~6. Each VISIBLE native pane held one long-lived SSE GET, plus the global
// uiEvents SSE — so a handful of native panes exhausted the pool and starved the
// reload navigation + health poll + other GETs ("daemon disconnect", dead
// Ctrl+Shift+R). WebSockets live in a SEPARATE browser pool (the one the terminal
// /io WS already uses), so native panes stop competing for the 6 HTTP slots. The
// payload + ordering mirror paneNativeStream exactly; only the transport differs.
// Input / approvals / history stay on their request/response routes.
//
// Frame protocol (so the client can discriminate without SSE named events):
//  1. control: {"kind":"epoch","nativeEpoch":N}  — FIRST, before the snapshot.
//  2. ring snapshot: raw NativeEvent JSON, oldest-first, one text frame each.
//  3. live deltas: raw NativeEvent JSON text frames.
//
// A NativeEvent always carries "seq"+"type" and never "kind", so the client
// routes on `kind === "epoch"` vs "everything else is a NativeEvent". Epoch FIRST
// is load-bearing: a daemon auto-update reattaches the same agent but rebuilds the
// ring from seq 1 under a new epoch; epoch-before-snapshot makes the client reset
// its dedup/seen cache before folding the fresh low-seq events (else the "missing
// latest messages until a Term toggle" bug).
const (
	nativeWSWriteTimeout    = 10 * time.Second // steady-state per-frame write + ping (mirrors ws.go)
	nativeWSSnapshotTimeout = 30 * time.Second // connect burst can be the whole ring across frames
	nativeWSPingInterval    = 30 * time.Second // ping cadence; pong resets the read deadline
	nativeWSReadDeadline    = 90 * time.Second // half-open detection (no pong → read errors → cleanup)
)

type nativeWSEpochFrame struct {
	Kind        string `json:"kind"` // "epoch"
	NativeEpoch uint64 `json:"nativeEpoch"`
}

// handleNativeWS upgrades to a WebSocket and streams a native pane's epoch +
// ring snapshot + live deltas. Mirrors handleWS (ws.go) for transport and
// paneNativeStream (native_api.go) for payload/ordering. Server->client only.
func handleNativeWS(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		p, ok := m.Get(id)
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("native ws upgrade: %v", err)
			return
		}

		// Subscribe BEFORE snapshotting (atomic under nativeMu) so no event slips
		// between the snapshot copy and live-loop entry — same contract as the SSE
		// handler. Any event racing in after the snapshot is buffered on ch; the
		// client dedups the snapshot/live overlap by Seq.
		_, snapshot, ch, cancel, isNative := p.SubscribeNative()
		if !isNative {
			// Not a native pane (mode flip / parser torn down) — can't send an HTTP
			// status post-upgrade, so close cleanly. (The client only opens this for
			// kind=="native" panes, so this is a rare race.)
			_ = conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "pane is not native"))
			_ = conn.Close()
			return
		}
		defer cancel()

		// --- Connect burst: written in THIS goroutine BEFORE the writer goroutine
		// starts (gorilla forbids concurrent writers), so the burst and the live
		// writer never overlap. epoch FIRST, then the ring snapshot oldest-first. ---
		ep := p.nativeEpoch.Load()
		if ep == 0 {
			ep = daemonEpoch // pane not yet ensureNative'd — mirror the DTO fallback
		}
		epFrame, _ := json.Marshal(nativeWSEpochFrame{Kind: "epoch", NativeEpoch: ep})
		_ = conn.SetWriteDeadline(time.Now().Add(nativeWSSnapshotTimeout))
		if err := conn.WriteMessage(websocket.TextMessage, epFrame); err != nil {
			_ = conn.Close()
			return
		}
		for _, ev := range snapshot {
			data, fErr := nativeEventFrame(ev) // snapshot entries marshal on demand (lean ring)
			if fErr != nil {
				continue
			}
			_ = conn.SetWriteDeadline(time.Now().Add(nativeWSSnapshotTimeout))
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				_ = conn.Close()
				return
			}
		}

		// --- Writer goroutine: live deltas (reuse the marshaled-once cache) + pings.
		// The ONLY writer from here on. On channel close (pane exited) send a close
		// frame so the reader wakes immediately instead of waiting out the deadline.
		readerDone := make(chan struct{})
		writerDone := make(chan struct{})
		go func() {
			defer close(writerDone)
			defer func() { _ = conn.Close() }()
			ticker := time.NewTicker(nativeWSPingInterval)
			defer ticker.Stop()
			for {
				select {
				case <-readerDone:
					return
				case ev, ok := <-ch:
					if !ok {
						_ = conn.WriteMessage(websocket.CloseMessage,
							websocket.FormatCloseMessage(websocket.CloseNormalClosure, "pane exited"))
						return
					}
					data, fErr := nativeEventFrame(ev)
					if fErr != nil {
						continue
					}
					_ = conn.SetWriteDeadline(time.Now().Add(nativeWSWriteTimeout))
					if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
						return
					}
				case <-ticker.C:
					_ = conn.SetWriteDeadline(time.Now().Add(nativeWSWriteTimeout))
					if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
						return
					}
				}
			}
		}()

		// --- Reader pump: no inbound app data (input/approvals/history are separate
		// routes), but we must service pong replies (reset the read deadline →
		// half-open detection) and notice a client close promptly. Mirrors ws.go. ---
		conn.SetReadLimit(1 << 20)
		_ = conn.SetReadDeadline(time.Now().Add(nativeWSReadDeadline))
		conn.SetPongHandler(func(string) error {
			_ = conn.SetReadDeadline(time.Now().Add(nativeWSReadDeadline))
			return nil
		})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
			// Inbound frames are ignored (ping/pong already covers keepalive).
		}
		// Wake the writer before joining it; see the quantized-teardown root-cause
		// note at handleWS's cleanup in ws.go.
		close(readerDone)
		_ = conn.Close()
		<-writerDone
	}
}
