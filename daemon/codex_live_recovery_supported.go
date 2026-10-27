//go:build linux || windows

package main

// The private control-plane relay serializes TUI submissions with credential
// handoffs. It never proxies model traffic or changes an approval response.
import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const codexRecoveryLimit = 4
const codexRecoveryWindow = 5 * time.Minute

// Codex 0.163 plugin catalogs exceed 16 MiB even before the first turn.
// Allow larger engine responses while retaining a finite per-message bound;
// terminal requests and the private credential RPC keep their 16 MiB limits.
const codexLiveRelayServerReadLimit = 64 << 20

type codexLivePending struct {
	method   string
	params   json.RawMessage
	recovery bool
}

type codexLiveRecovery struct {
	// gate also covers credential RPCs, so an idle observation and handoff
	// cannot race a TUI turn/start, resume, compact, or approval submission.
	gate          sync.Mutex
	mu            sync.Mutex
	upstream      *websocket.Conn
	connected     bool
	pending       map[string]codexLivePending
	active        map[string]string
	background    map[string]bool
	thread        string
	turn          string
	lastParams    json.RawMessage
	lastCompleted string
	attempts      int
	firstFailure  time.Time
	retryAt       time.Time
	recoveryState string
	sequence      uint64
	inputPath     string
	inputMark     string
}

func newCodexLiveRecovery() *codexLiveRecovery {
	return &codexLiveRecovery{pending: map[string]codexLivePending{}, active: map[string]string{}, background: map[string]bool{}}
}

// Input (including a draft or Escape) cancels a queued automatic continuation.
// Only a subsequent explicit turn/start rearms a new recovery budget.
func (s *codexLiveRecovery) userInput() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retryAt = time.Time{}
	s.recoveryState = "cancelled"
}

func (s *codexLiveRecovery) observeClient(line codexRPCLine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if line.Method == "" {
		return
	} // approval responses pass through unchanged
	var target struct {
		ThreadID  string `json:"threadId"`
		Ephemeral bool   `json:"ephemeral"`
	}
	_ = json.Unmarshal(line.Params, &target)
	background := s.background[target.ThreadID] || (line.Method == "thread/start" && target.Ephemeral)
	if !background {
		switch line.Method {
		case "turn/start", "turn/steer", "turn/interrupt", "thread/start", "thread/resume", "thread/fork", "thread/rollback", "thread/compact/start", "account/login/start", "account/logout":
			s.retryAt = time.Time{}
			s.recoveryState = "cancelled"
		}
	}
	if line.Method == "turn/start" && !background {
		s.attempts, s.firstFailure = 0, time.Time{}
		s.inputMark = ""
		if s.inputPath != "" {
			f, err := lockCodexLiveInput(s.inputPath)
			if err == nil {
				raw, readErr := io.ReadAll(f)
				if readErr == nil {
					s.inputMark = string(raw)
				}
				unlockCodexLiveInput(f)
			}
		}
		s.recoveryState = ""
		var params struct {
			ThreadID string `json:"threadId"`
		}
		if json.Unmarshal(line.Params, &params) == nil {
			s.thread, s.turn, s.lastParams = params.ThreadID, "", append(json.RawMessage(nil), line.Params...)
		}
	}
	if len(line.ID) > 0 && (line.Method == "initialize" || strings.HasPrefix(line.Method, "turn/") || line.Method == "thread/start" || line.Method == "thread/resume" || line.Method == "thread/fork" || line.Method == "thread/compact/start" || line.Method == "account/logout" || line.Method == "account/login/start") {
		s.pending[string(line.ID)] = codexLivePending{method: line.Method, params: append(json.RawMessage(nil), line.Params...)}
	}
}

// Returns true only for a response to our own recovery request. Notifications
// (including approval requests) still reach the original TUI connection.
func (s *codexLiveRecovery) observeServer(line codexRPCLine, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if line.Method == "" && len(line.ID) > 0 {
		p, ok := s.pending[string(line.ID)]
		if !ok {
			return false
		}
		delete(s.pending, string(line.ID))
		var target struct {
			ThreadID  string `json:"threadId"`
			Ephemeral bool   `json:"ephemeral"`
		}
		_ = json.Unmarshal(p.params, &target)
		if p.method == "thread/start" && target.Ephemeral && line.Error == nil {
			var result struct {
				Thread struct {
					ID string `json:"id"`
				} `json:"thread"`
			}
			if json.Unmarshal(line.Result, &result) == nil && result.Thread.ID != "" {
				s.background[result.Thread.ID] = true
			}
		}
		if p.method == "turn/start" && !s.background[target.ThreadID] {
			if line.Error != nil {
				s.retryAt, s.recoveryState = time.Time{}, "needs-attention"
			} else {
				var params struct {
					ThreadID string `json:"threadId"`
				}
				var result struct {
					Turn struct {
						ID string `json:"id"`
					} `json:"turn"`
				}
				if json.Unmarshal(p.params, &params) == nil && json.Unmarshal(line.Result, &result) == nil && params.ThreadID != "" && result.Turn.ID != "" {
					s.thread, s.turn, s.lastParams = params.ThreadID, result.Turn.ID, p.params
					// A fast turn can complete before its start reply arrives.
					if s.lastCompleted != result.Turn.ID {
						s.active[params.ThreadID] = result.Turn.ID
					}
				}
			}
		}
		return p.recovery
	}
	var event struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  *struct {
				Info json.RawMessage `json:"codexErrorInfo"`
			} `json:"error"`
		} `json:"turn"`
	}
	if line.Method != "turn/started" && line.Method != "turn/completed" {
		return false
	}
	if json.Unmarshal(line.Params, &event) != nil || event.ThreadID == "" || event.Turn.ID == "" {
		return false
	}
	if line.Method == "turn/started" {
		s.active[event.ThreadID] = event.Turn.ID
		if event.ThreadID == s.thread {
			s.turn = event.Turn.ID
		}
		return false
	}
	if s.active[event.ThreadID] == event.Turn.ID {
		delete(s.active, event.ThreadID)
	}
	if event.ThreadID != s.thread || event.Turn.ID != s.turn || s.lastCompleted == event.Turn.ID {
		return false
	}
	s.lastCompleted = event.Turn.ID
	s.retryAt = time.Time{}
	code := ""
	if event.Turn.Error != nil {
		_ = json.Unmarshal(event.Turn.Error.Info, &code)
	}
	if event.Turn.Status != "failed" || (code != "serverOverloaded" && code != "server_overloaded") {
		s.recoveryState = ""
		return false
	}
	if s.recoveryState == "cancelled" {
		return false
	}
	if s.firstFailure.IsZero() {
		s.firstFailure = now
	}
	if s.attempts >= codexRecoveryLimit || now.Sub(s.firstFailure) >= codexRecoveryWindow {
		s.recoveryState = "exhausted"
		return false
	}
	delay := (15 * time.Second) << s.attempts
	// Independent panes must not all retry the same model simultaneously.
	delay += time.Duration(rand.Int64N(int64(delay / 5)))
	if now.Add(delay).After(s.firstFailure.Add(codexRecoveryWindow)) {
		s.recoveryState = "exhausted"
		return false
	}
	s.retryAt, s.recoveryState = now.Add(delay), "waiting"
	return false
}

func (s *codexLiveRecovery) busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending) > 0 || len(s.active) > 0
}

func (s *codexLiveRecovery) status() (string, int64, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.retryAt.IsZero() && s.inputPath != "" {
		raw, err := os.ReadFile(s.inputPath)
		if err != nil || s.inputMark == "" || string(raw) != s.inputMark {
			s.retryAt, s.recoveryState = time.Time{}, "cancelled"
		}
	}
	at := int64(0)
	if !s.retryAt.IsZero() {
		at = s.retryAt.UnixMilli()
	}
	return s.recoveryState, at, s.attempts
}

// Caller owns gate, which excludes a simultaneous TUI request. Check all
// loaded threads, including delegated children and standalone compactions.
// Codex reports systemError for an inactive failed turn; active work takes
// precedence over that status. It is safe for handoff, not itself retry evidence.
func codexLiveThreadsIdle(ctx context.Context, rpc *codexLiveRPC) bool {
	var cursor *string
	for page := 0; page < 10; page++ {
		b, err := rpc.call(ctx, "thread/loaded/list", map[string]any{"cursor": cursor, "limit": 100})
		var list struct {
			Data []string `json:"data"`
			Next *string  `json:"nextCursor"`
		}
		if err != nil || json.Unmarshal(b, &list) != nil || list.Data == nil {
			return false
		}
		for _, id := range list.Data {
			b, err = rpc.call(ctx, "thread/read", map[string]any{"threadId": id})
			var read struct {
				Thread struct {
					Status struct {
						Type string `json:"type"`
					} `json:"status"`
				} `json:"thread"`
			}
			if err != nil || json.Unmarshal(b, &read) != nil || (read.Thread.Status.Type != "idle" && read.Thread.Status.Type != "systemError") {
				return false
			}
		}
		if list.Next == nil {
			return true
		}
		cursor = list.Next
	}
	return false
}

// Called under gate, only after credentials are settled and all work is idle.
func (s *codexLiveRecovery) recover(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retryAt.IsZero() || now.Before(s.retryAt) || s.upstream == nil || len(s.pending) != 0 || len(s.active) != 0 {
		return nil
	}
	if now.Sub(s.firstFailure) >= codexRecoveryWindow {
		s.retryAt, s.recoveryState = time.Time{}, "exhausted"
		return nil
	}
	if s.inputPath != "" {
		f, err := lockCodexLiveInput(s.inputPath)
		if err != nil {
			s.retryAt, s.recoveryState = time.Time{}, "cancelled"
			return err
		}
		defer unlockCodexLiveInput(f)
		raw, err := io.ReadAll(f)
		if err != nil || s.inputMark == "" || string(raw) != s.inputMark {
			s.retryAt, s.recoveryState = time.Time{}, "cancelled"
			return nil
		}
	}
	var params map[string]json.RawMessage
	if json.Unmarshal(s.lastParams, &params) != nil {
		return errors.New("recovery turn configuration unavailable")
	}
	// A new continuation must not reuse the original message's deduplication ID.
	delete(params, "clientUserMessageId")
	delete(params, "toolOutput")
	params["input"] = json.RawMessage(`[{"type":"text","text":"Continue the interrupted work from the current conversation. The previous turn stopped because the model was temporarily at capacity. Preserve completed work and tool results; inspect any uncertain side effects before repeating an action."}]`)
	encoded, err := json.Marshal(params)
	if err != nil {
		return err
	}
	s.sequence++
	id, _ := json.Marshal(struct {
		Recovery uint64 `json:"recovery"`
	}{s.sequence})
	// A string ID cannot collide with the TUI's numeric request IDs. Refuse a
	// client-supplied collision in the relay rather than misroute its response.
	key, _ := json.Marshal("solu-recovery:" + string(id))
	request, _ := json.Marshal(map[string]any{"id": json.RawMessage(key), "method": "turn/start", "params": params})
	s.pending[string(key)] = codexLivePending{method: "turn/start", params: encoded, recovery: true}
	s.retryAt, s.recoveryState = time.Time{}, "retrying"
	s.attempts++
	_ = s.upstream.SetWriteDeadline(now.Add(5 * time.Second))
	// Ambiguous writes are never retried: we cannot know whether a turn began.
	return s.upstream.WriteMessage(websocket.TextMessage, request)
}

func startCodexLiveRelay(ctx context.Context, local, remote string, state *codexLiveRecovery) (func(), error) {
	ln, err := net.Listen("unix", local)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(local, 0600); err != nil {
		ln.Close()
		return nil, err
	}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		state.mu.Lock()
		if state.connected {
			state.mu.Unlock()
			http.Error(w, "terminal already connected", 409)
			return
		}
		state.connected = true
		state.mu.Unlock()
		defer func() {
			state.mu.Lock()
			state.connected = false
			state.upstream = nil
			state.retryAt = time.Time{}
			state.recoveryState = "cancelled"
			state.mu.Unlock()
		}()
		down, err := (&websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }}).Upgrade(w, req, nil)
		if err != nil {
			return
		}
		defer down.Close()
		dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second, NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", remote)
		}}
		up, _, err := dialer.DialContext(ctx, "ws://localhost/", nil)
		if err != nil {
			return
		}
		defer up.Close()
		up.SetReadLimit(codexLiveRelayServerReadLimit)
		down.SetReadLimit(16 << 20)
		state.mu.Lock()
		state.upstream = up
		state.mu.Unlock()
		finished := make(chan struct{})
		defer close(finished)
		go func() {
			select {
			case <-ctx.Done():
				up.Close()
				down.Close()
			case <-finished:
			}
		}()
		go func() {
			defer up.Close()
			defer down.Close()
			for {
				kind, data, err := down.ReadMessage()
				if err != nil {
					return
				}
				var line codexRPCLine
				if json.Unmarshal(data, &line) != nil {
					return
				}
				var id string
				if json.Unmarshal(line.ID, &id) == nil && strings.HasPrefix(id, "solu-recovery:") {
					return
				}
				state.gate.Lock()
				state.observeClient(line)
				_ = up.SetWriteDeadline(time.Now().Add(5 * time.Second))
				err = up.WriteMessage(kind, data)
				state.gate.Unlock()
				if err != nil {
					return
				}
			}
		}()
		for {
			kind, data, err := up.ReadMessage()
			if err != nil {
				return
			}
			var line codexRPCLine
			if json.Unmarshal(data, &line) != nil {
				return
			}
			if state.observeServer(line, time.Now()) {
				continue
			}
			_ = down.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if down.WriteMessage(kind, data) != nil {
				return
			}
		}
	})
	go server.Serve(ln)
	return func() { server.Close() }, nil
}

// Cross-process input admission. Real terminal stdin remains a TTY; wrapping
// it in io.Reader would give Codex a pipe and break terminal initialization.
// The daemon holds this lease before forwarding input, while recovery holds it
// through its RPC write. A draft can therefore cancel a queued continuation
// without a read/check/write race. Only the wrapper creates the marker file.
func (p *Pane) codexLiveInputLease(b []byte) (func(), error) {
	if p.codexLiveDataDir == "" || len(b) == 0 || isTerminalReplyOnly(b) {
		return func() {}, nil
	}
	path := filepath.Join(p.codexLiveDataDir, "codex-live", p.ID+".input")
	f, err := lockCodexLiveInput(path)
	if os.IsNotExist(err) {
		return func() {}, nil
	}
	if err != nil {
		return nil, err
	}
	mark := []byte(strconv.FormatInt(time.Now().UnixNano(), 10))
	if err = f.Truncate(0); err == nil {
		_, err = f.WriteAt(mark, 0)
	}
	if err != nil {
		unlockCodexLiveInput(f)
		return nil, errors.New("could not coordinate terminal input")
	}
	return func() { unlockCodexLiveInput(f) }, nil
}
