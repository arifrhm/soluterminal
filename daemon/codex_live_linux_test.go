//go:build linux

package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestCodexLiveRPCOutlivesConnectDeadline(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "app.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	connected := make(chan *websocket.Conn, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err == nil {
			connected <- c
		}
	})}
	defer server.Close()
	go server.Serve(ln)
	dialCtx, cancel := context.WithCancel(context.Background())
	rpc, err := newCodexLiveRPC(dialCtx, socket)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer rpc.conn.Close()
	peer := <-connected
	defer peer.Close()
	cancel() // expired boot timeout must not kill later refresh requests
	if err := peer.WriteJSON(map[string]any{"id": 10, "method": "account/chatgptAuthTokens/refresh", "params": map[string]any{"reason": "unauthorized"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-rpc.requests:
		if got.Method != "account/chatgptAuthTokens/refresh" {
			t.Fatal("wrong request")
		}
	case <-rpc.done:
		t.Fatal("live auth connection inherited its completed dial context")
	case <-time.After(time.Second):
		t.Fatal("refresh request was not delivered")
	}
}

func TestCodexLiveTerminalSelection(t *testing.T) {
	for _, cmd := range [][]string{{"codex"}, {"codex", "resume", "id", "-c", "developer_instructions=keep this"}, {"/usr/bin/codex", "fork", "id"}} {
		if !codexLiveTerminalArgs(cmd) {
			t.Fatalf("eligible command rejected: %q", cmd)
		}
	}
	for _, cmd := range [][]string{{"claude"}, {"codex", "exec", "hello"}, {"codex", "app-server"}, {"codex", "resume", "id", "--remote", "unix://somewhere"}, {"codex", "--profile", "private"}, {"codex", "--oss"}, {"codex", "-c", "model_provider=other"}} {
		if codexLiveTerminalArgs(cmd) {
			t.Fatalf("external/headless command hijacked: %q", cmd)
		}
	}
}

func TestCodexLiveServerOverridesPreservePolicy(t *testing.T) {
	args := codexLiveServerArgs([]string{"resume", "id", "-c", "developer_instructions=exact text", "--config=approval_policy=\"on-request\"", "--enable", "some_feature", "--sandbox", "read-only"}, "/tmp/private.sock")
	got := strings.Join(args, "\n")
	for _, want := range []string{"developer_instructions=exact text", "--config=approval_policy=\"on-request\"", "--enable\nsome_feature", `cli_auth_credentials_store="ephemeral"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in server argv", want)
		}
	}
	if strings.Contains(got, "resume") || strings.Contains(got, "--sandbox") {
		t.Fatal("TUI arguments leaked to app-server command parser")
	}
}

func TestCodexLiveReadTokenRejectsInvalidLogin(t *testing.T) {
	p := filepath.Join(t.TempDir(), "auth.json")
	for _, body := range []string{`{}`, `{"OPENAI_API_KEY":"fixture"}`, `{"tokens":{"access_token":"fixture"}}`, strings.Repeat("x", maxCodexAuthBytes+1)} {
		_ = os.WriteFile(p, []byte(body), 0600)
		if _, err := readCodexLiveToken(p); err == nil {
			t.Fatal("invalid OAuth login accepted")
		}
	}
}

// Opt-in real-CLI test, with synthetic accounts and a localhost Responses
// server. No model calls or real account credentials are used. This verifies
// request authentication, not merely a sidebar/file or account/read change.

func TestCodexLiveExitShellKeepsDaemonAddress(t *testing.T) {
	cmd, err := codexLiveSpawnCommand("/usr/bin/solud", "/data", "http://127.0.0.1:8787", []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	script := cmd[len(cmd)-1]
	exportAt := strings.Index(script, "export "+paneDaemonURLEnv+"='http://127.0.0.1:8787'; exec ")
	if exportAt < 0 || exportAt < strings.Index(script, "codex-terminal") {
		t.Fatalf("exit shell lacks the daemon address after Codex exits: %s", script)
	}
	cmd, _ = codexLiveSpawnCommand("/usr/bin/solud", "/data", "http://example.com:8787", []string{"codex"})
	if strings.Contains(cmd[len(cmd)-1], paneDaemonURLEnv) {
		t.Fatal("non-loopback daemon address exported")
	}
}
