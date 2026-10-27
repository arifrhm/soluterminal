//go:build linux || windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func liveLine(t *testing.T, value string) codexRPCLine {
	t.Helper()
	var line codexRPCLine
	if err := json.Unmarshal([]byte(value), &line); err != nil {
		t.Fatal(err)
	}
	return line
}

func liveRecoveryTurn(t *testing.T, s *codexLiveRecovery) {
	t.Helper()
	s.observeClient(liveLine(t, `{"id":1,"method":"turn/start","params":{"threadId":"thread-a","model":"gpt-6-astra","effort":"high","approvalPolicy":"on-request","clientUserMessageId":"original-message","input":[{"type":"text","text":"original work"}]}}`))
	s.observeServer(liveLine(t, `{"id":1,"result":{"turn":{"id":"turn-a"}}}`), time.Now())
	s.observeServer(liveLine(t, `{"method":"turn/started","params":{"threadId":"thread-a","turn":{"id":"turn-a","status":"inProgress"}}}`), time.Now())
}

const capacityCompleted = `{"method":"turn/completed","params":{"threadId":"thread-a","turn":{"id":"turn-a","status":"failed","error":{"codexErrorInfo":"serverOverloaded"}}}}`

func TestCodexLiveRecoveryUsesTerminalEvidenceOnly(t *testing.T) {
	for _, tc := range []struct {
		name, event string
		want        bool
	}{
		{"capacity", capacityCompleted, true},
		{"internal retry", `{"method":"error","params":{"threadId":"thread-a","turnId":"turn-a","willRetry":true,"error":{"codexErrorInfo":"serverOverloaded"}}}`, false},
		{"error before completion", `{"method":"error","params":{"threadId":"thread-a","turnId":"turn-a","willRetry":false,"error":{"codexErrorInfo":"serverOverloaded"}}}`, false},
		{"quoted tool output", `{"method":"item/completed","params":{"item":{"text":"Selected model is at capacity. Please try a different model."}}}`, false},
		{"other thread", strings.ReplaceAll(capacityCompleted, "thread-a", "thread-b"), false},
		{"stale turn", strings.ReplaceAll(capacityCompleted, "turn-a", "turn-old"), false},
		{"user interrupt", strings.ReplaceAll(capacityCompleted, `"failed"`, `"interrupted"`), false},
		{"revocation", strings.ReplaceAll(capacityCompleted, `"serverOverloaded"`, `"unauthorized"`), false},
		{"quota", strings.ReplaceAll(capacityCompleted, `"serverOverloaded"`, `"usageLimitExceeded"`), false},
		{"structured other error", strings.ReplaceAll(capacityCompleted, `"serverOverloaded"`, `{"httpConnectionFailed":{"httpStatusCode":503}}`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCodexLiveRecovery()
			liveRecoveryTurn(t, s)
			s.observeServer(liveLine(t, tc.event), time.Now())
			if got := !s.retryAt.IsZero(); got != tc.want {
				t.Fatalf("scheduled=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestCodexLiveRecoveryCancellationBudgetAndDuplicateEvents(t *testing.T) {
	s := newCodexLiveRecovery()
	liveRecoveryTurn(t, s)
	now := time.Now()
	s.observeServer(liveLine(t, capacityCompleted), now)
	deadline := s.retryAt
	s.observeServer(liveLine(t, capacityCompleted), now.Add(time.Second))
	if s.retryAt != deadline {
		t.Fatal("duplicate completion extended retry")
	}
	s.observeClient(liveLine(t, `{"id":9,"method":"account/read","params":{}}`))
	if s.retryAt != deadline {
		t.Fatal("background account read cancelled recovery")
	}
	s.userInput()
	if !s.retryAt.IsZero() || s.recoveryState != "cancelled" {
		t.Fatal("typing failed to cancel")
	}
	for _, budget := range []struct {
		attempts int
		started  time.Time
	}{{4, now}, {0, now.Add(-6 * time.Minute)}} {
		s = newCodexLiveRecovery()
		liveRecoveryTurn(t, s)
		s.attempts, s.firstFailure = budget.attempts, budget.started
		s.observeServer(liveLine(t, capacityCompleted), now)
		if !s.retryAt.IsZero() || s.recoveryState != "exhausted" {
			t.Fatal("recovery budget bypassed")
		}
	}
	s = newCodexLiveRecovery()
	liveRecoveryTurn(t, s)
	s.userInput()
	s.observeServer(liveLine(t, capacityCompleted), now)
	if !s.retryAt.IsZero() {
		t.Fatal("draft entered during running turn was ignored")
	}
}

func TestCodexLiveRecoveryFastCompletionBeforeReply(t *testing.T) {
	s := newCodexLiveRecovery()
	s.observeClient(liveLine(t, `{"id":1,"method":"turn/start","params":{"threadId":"thread-a","input":[]}}`))
	s.observeServer(liveLine(t, `{"method":"turn/started","params":{"threadId":"thread-a","turn":{"id":"turn-a"}}}`), time.Now())
	s.observeServer(liveLine(t, capacityCompleted), time.Now())
	s.observeServer(liveLine(t, `{"id":1,"result":{"turn":{"id":"turn-a"}}}`), time.Now())
	if s.busy() || s.retryAt.IsZero() {
		t.Fatal("reply after completion lost recovery or rearmed completed turn")
	}
}

func TestCodexLiveRefreshIdentity(t *testing.T) {
	a := codexLiveToken{AccountID: "workspace", Subject: "user", Revision: "old"}
	for _, tc := range []struct {
		b    codexLiveToken
		same bool
	}{
		{codexLiveToken{AccountID: "workspace", Subject: "user", Revision: "new"}, true},
		{codexLiveToken{AccountID: "workspace", Subject: "another-user"}, false},
		{codexLiveToken{AccountID: "another-workspace", Subject: "user"}, false},
		{codexLiveToken{AccountID: "workspace"}, false},
	} {
		if codexLiveSameOwner(a, tc.b) != tc.same {
			t.Fatal("incorrect refresh identity")
		}
	}
}

func liveSocket(t *testing.T) (string, chan *websocket.Conn) {
	t.Helper()
	path := filepath.Join(codexLiveTestDir(t), "engine.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	connections := make(chan *websocket.Conn, 2)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err == nil {
			connections <- c
		}
	})}
	go server.Serve(ln)
	t.Cleanup(func() { server.Close() })
	return path, connections
}

func dialLiveSocket(t *testing.T, path string) *websocket.Conn {
	t.Helper()
	d := websocket.Dialer{NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	c, _, err := d.Dial("ws://localhost/", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestCodexLiveRelayRecoversSameThreadAndForwardsApprovals(t *testing.T) {
	remote, connections := liveSocket(t)
	s := newCodexLiveRecovery()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	local := filepath.Join(codexLiveTestDir(t), "terminal.sock")
	closeRelay, err := startCodexLiveRelay(ctx, local, remote, s)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRelay()
	client := dialLiveSocket(t, local)
	peer := <-connections
	defer peer.Close()
	read := func(c *websocket.Conn) codexRPCLine {
		t.Helper()
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		var line codexRPCLine
		if err := c.ReadJSON(&line); err != nil {
			t.Fatal(err)
		}
		return line
	}
	write := func(c *websocket.Conn, raw string) {
		t.Helper()
		if err := c.WriteMessage(websocket.TextMessage, []byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	write(client, `{"id":1,"method":"turn/start","params":{"threadId":"thread-a","model":"gpt-6-astra","effort":"high","approvalPolicy":"on-request","clientUserMessageId":"original-message","input":[{"type":"text","text":"original work"}]}}`)
	if read(peer).Method != "turn/start" {
		t.Fatal("turn not forwarded")
	}
	write(peer, `{"id":1,"result":{"turn":{"id":"turn-a"}}}`)
	read(client)
	// An approval is forwarded byte-for-byte, and its response goes to the same connection.
	approval := `{"id":42,"method":"item/commandExecution/requestApproval","params":{"threadId":"thread-a","turnId":"turn-a"}}`
	write(peer, approval)
	if read(client).Method != "item/commandExecution/requestApproval" {
		t.Fatal("approval missing")
	}
	write(client, `{"id":42,"result":{"decision":"decline"}}`)
	if string(read(peer).Result) != `{"decision":"decline"}` {
		t.Fatal("approval altered")
	}
	write(peer, capacityCompleted)
	read(client)
	s.mu.Lock()
	s.retryAt = time.Now().Add(-time.Second)
	s.mu.Unlock()
	s.gate.Lock()
	err = s.recover(time.Now())
	s.gate.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	recovery := read(peer)
	var params map[string]json.RawMessage
	_ = json.Unmarshal(recovery.Params, &params)
	for key, want := range map[string]string{"threadId": `"thread-a"`, "model": `"gpt-6-astra"`, "effort": `"high"`, "approvalPolicy": `"on-request"`} {
		if string(params[key]) != want {
			t.Fatalf("lost %s", key)
		}
	}
	if _, ok := params["clientUserMessageId"]; ok {
		t.Fatal("reused original message identity")
	}
	if strings.Contains(string(params["input"]), "original work") || !strings.Contains(string(params["input"]), "Preserve completed work") {
		t.Fatal("replayed original task")
	}
	write(peer, `{"id":`+string(recovery.ID)+`,"result":{"turn":{"id":"turn-b"}}}`)
	write(peer, `{"method":"turn/started","params":{"threadId":"thread-a","turn":{"id":"turn-b"}}}`)
	if read(client).Method != "turn/started" {
		t.Fatal("private request reply leaked to TUI")
	}
	write(peer, `{"method":"turn/completed","params":{"threadId":"thread-a","turn":{"id":"turn-b","status":"completed"}}}`)
	read(client)
	if s.busy() {
		t.Fatal("successful recovery left pane busy")
	}
	state, at, attempts := s.status()
	if state != "" || at != 0 || attempts != 1 {
		t.Fatalf("status=%s/%d/%d", state, at, attempts)
	}
}

func TestCodexLiveCredentialProjection(t *testing.T) {
	original := &paneCredentialInfo{Provider: "codex", Source: "launch", Account: "a", ActiveAccount: "b", RestartRequired: true}
	for _, state := range []string{"live", "switch-pending"} {
		got := codexLiveCredentialProjection(original, codexLiveStatus{State: state, Recovery: "waiting", RetryAt: 100})
		if got.RestartRequired || got.LiveState != state || got.RetryAt != 100 {
			t.Fatalf("bad projection: %+v", got)
		}
	}
	if !original.RestartRequired {
		t.Fatal("projection mutated launch metadata")
	}
}

func TestCodexLiveRecoveryInputAdmission(t *testing.T) {
	for _, input := range []string{"draft", "\x1b", "\x1b[1;1R"} {
		t.Run(fmt.Sprintf("%q", input), func(t *testing.T) {
			dir := codexLiveTestDir(t)
			path := filepath.Join(dir, "codex-live", "pane.input")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("initial"), 0600); err != nil {
				t.Fatal(err)
			}
			s := newCodexLiveRecovery()
			s.inputPath = path
			liveRecoveryTurn(t, s)
			s.observeServer(liveLine(t, capacityCompleted), time.Now())
			pane := &Pane{ID: "pane", codexLiveDataDir: dir}
			release, err := pane.codexLiveInputLease([]byte(input))
			if err != nil {
				t.Fatal(err)
			}
			release()
			state, _, _ := s.status()
			if input == "\x1b[1;1R" {
				if state != "waiting" {
					t.Fatal("terminal reply cancelled recovery")
				}
			} else if state != "cancelled" {
				t.Fatal("draft or escape failed to cancel recovery")
			}
		})
	}
	dir := codexLiveTestDir(t)
	pane := &Pane{ID: "old-wrapper", codexLiveDataDir: dir}
	release, err := pane.codexLiveInputLease([]byte("continue"))
	if err != nil {
		t.Fatal("legacy pane input was blocked", err)
	}
	release()
}

func TestCodexLiveThreadIdleIncludesChildrenAndUnknownStates(t *testing.T) {
	for _, status := range []string{"idle", "active", "notLoaded", "systemError", ""} {
		t.Run(status, func(t *testing.T) {
			socket, connections := liveSocket(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			rpc, err := newCodexLiveRPC(ctx, socket)
			if err != nil {
				t.Fatal(err)
			}
			defer rpc.conn.Close()
			peer := <-connections
			defer peer.Close()
			go func() {
				for {
					var req codexRPCLine
					if peer.ReadJSON(&req) != nil {
						return
					}
					var result any
					switch req.Method {
					case "thread/loaded/list":
						var params struct {
							Cursor *string `json:"cursor"`
						}
						_ = json.Unmarshal(req.Params, &params)
						if params.Cursor == nil {
							result = map[string]any{"data": []string{"root"}, "nextCursor": "second"}
						} else {
							result = map[string]any{"data": []string{"child"}, "nextCursor": nil}
						}
					case "thread/read":
						var params struct {
							ThreadID string `json:"threadId"`
						}
						_ = json.Unmarshal(req.Params, &params)
						value := "idle"
						if params.ThreadID == "child" {
							value = status
						}
						result = map[string]any{"thread": map[string]any{"status": map[string]string{"type": value}}}
					}
					if peer.WriteJSON(map[string]any{"id": req.ID, "result": result}) != nil {
						return
					}
				}
			}()
			if got := codexLiveThreadsIdle(ctx, rpc); got != (status == "idle" || status == "systemError") {
				t.Fatalf("idle=%v child=%q", got, status)
			}
		})
	}
}

func TestCodexLiveRecoveryIgnoresTemporaryTitleThreads(t *testing.T) {
	s := newCodexLiveRecovery()
	liveRecoveryTurn(t, s)
	s.observeClient(liveLine(t, `{"id":"title-start","method":"thread/start","params":{"ephemeral":true}}`))
	s.observeServer(liveLine(t, capacityCompleted), time.Now())
	if s.retryAt.IsZero() {
		t.Fatal("hidden thread creation cancelled recovery")
	}
	s.observeServer(liveLine(t, `{"id":"title-start","result":{"thread":{"id":"title-thread"}}}`), time.Now())
	s.observeClient(liveLine(t, `{"id":"title-turn","method":"turn/start","params":{"threadId":"title-thread","input":[]}}`))
	s.observeServer(liveLine(t, `{"id":"title-turn","result":{"turn":{"id":"title-turn"}}}`), time.Now())
	s.observeServer(liveLine(t, `{"method":"turn/started","params":{"threadId":"title-thread","turn":{"id":"title-turn"}}}`), time.Now())
	if !s.busy() {
		t.Fatal("hidden work did not defer handoff")
	}
	s.observeServer(liveLine(t, `{"method":"turn/completed","params":{"threadId":"title-thread","turn":{"id":"title-turn","status":"completed"}}}`), time.Now())
	if s.thread != "thread-a" || s.retryAt.IsZero() || s.busy() {
		t.Fatal("title completion replaced foreground recovery")
	}
}
