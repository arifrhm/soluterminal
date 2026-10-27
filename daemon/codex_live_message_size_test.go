//go:build linux || windows

package main

import (
	"bytes"
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func liveRelayMessagePair(t *testing.T) (client, peer *websocket.Conn) {
	t.Helper()
	remote, connections := liveSocket(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	local := filepath.Join(codexLiveTestDir(t), "terminal.sock")
	closeRelay, err := startCodexLiveRelay(ctx, local, remote, newCodexLiveRecovery())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeRelay)
	client = dialLiveSocket(t, local)
	select {
	case peer = <-connections:
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not connect to engine")
	}
	t.Cleanup(func() { peer.Close() })
	return client, peer
}

func TestCodexLiveRelayLargePluginCatalog(t *testing.T) {
	client, peer := liveRelayMessagePair(t)
	request := []byte(`{"id":"plugins","method":"plugin/list","params":{}}`)
	if err := client.WriteMessage(websocket.TextMessage, request); err != nil {
		t.Fatal(err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, got, err := peer.ReadMessage(); err != nil || !bytes.Equal(got, request) {
		t.Fatalf("plugin request was not forwarded: %v", err)
	}
	// Codex 0.163.0 returned this many bytes during the reported fresh launch.
	const catalogBytes = 17_366_147
	prefix := `{"id":"plugins","result":{"marketplaces":[{"name":"fixture","description":"`
	suffix := `"}],"marketplaceLoadErrors":[],"featuredPluginIds":[]}}`
	catalog := []byte(prefix + strings.Repeat("x", catalogBytes-len(prefix)-len(suffix)) + suffix)
	_ = peer.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := peer.WriteMessage(websocket.TextMessage, catalog); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, got, err := client.ReadMessage(); err != nil || !bytes.Equal(got, catalog) {
		t.Fatalf("catalog must arrive intact without reconnecting: %v (received %d of %d bytes)", err, len(got), len(catalog))
	}
	// Startup must leave the same connection usable for subsequent operations.
	next := []byte(`{"id":2,"method":"account/read","params":{}}`)
	if err := client.WriteMessage(websocket.TextMessage, next); err != nil {
		t.Fatal(err)
	}
	if _, got, err := peer.ReadMessage(); err != nil || !bytes.Equal(got, next) {
		t.Fatalf("connection unusable after catalog: %v", err)
	}
	response := []byte(`{"id":2,"result":{"account":null}}`)
	if err := peer.WriteMessage(websocket.TextMessage, response); err != nil {
		t.Fatal(err)
	}
	if _, got, err := client.ReadMessage(); err != nil || !bytes.Equal(got, response) {
		t.Fatalf("follow-up response was not forwarded: %v", err)
	}
}

func TestCodexLiveRelayRejectsOversizedMessages(t *testing.T) {
	for _, tc := range []struct {
		name   string
		server bool
		size   int
	}{
		{"server response above 64 MiB", true, (64 << 20) + 1},
		{"client request above 16 MiB", false, (16 << 20) + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, peer := liveRelayMessagePair(t)
			sender, receiver := client, peer
			if tc.server {
				sender, receiver = peer, client
			}
			_ = sender.SetWriteDeadline(time.Now().Add(10 * time.Second))
			written := make(chan struct{})
			go func() {
				defer close(written)
				w, err := sender.NextWriter(websocket.TextMessage)
				if err != nil {
					return
				}
				defer w.Close()
				// Stream a valid JSON object over many frames, so the bound must
				// apply to the entire message rather than each fragment.
				if _, err = w.Write([]byte(`{"padding":"`)); err != nil {
					return
				}
				chunk := bytes.Repeat([]byte("x"), 1<<20)
				for left := tc.size - len(`{"padding":""}`); left > 0; {
					n := min(left, len(chunk))
					if _, err = w.Write(chunk[:n]); err != nil {
						return
					}
					left -= n
				}
				_, _ = w.Write([]byte(`"}`))
			}()
			_ = receiver.SetReadDeadline(time.Now().Add(10 * time.Second))
			_, _, err := receiver.ReadMessage()
			if err == nil {
				t.Fatal("oversized message crossed the relay")
			}
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				t.Fatalf("relay did not promptly reject oversized message: %v", err)
			}
			sender.Close()
			select {
			case <-written:
			case <-time.After(5 * time.Second):
				t.Fatal("oversized sender did not stop")
			}
		})
	}
}
