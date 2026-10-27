package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type conditionalTestConn struct {
	mu          sync.Mutex
	buf         bytes.Buffer
	closed      bool
	beforeWrite func()
	writeError  error
	short       bool
	writesAt    []time.Time
}

func (c *conditionalTestConn) Read([]byte) (int, error) { return 0, io.EOF }
func (c *conditionalTestConn) Write(b []byte) (int, error) {
	if c.beforeWrite != nil {
		c.beforeWrite()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writesAt = append(c.writesAt, time.Now())
	if c.writeError != nil {
		return 0, c.writeError
	}
	if c.short {
		return c.buf.Write(b[:len(b)-1])
	}
	return c.buf.Write(b)
}
func (c *conditionalTestConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}
func (*conditionalTestConn) LocalAddr() net.Addr              { return nil }
func (*conditionalTestConn) RemoteAddr() net.Addr             { return nil }
func (*conditionalTestConn) SetDeadline(time.Time) error      { return nil }
func (*conditionalTestConn) SetReadDeadline(time.Time) error  { return nil }
func (*conditionalTestConn) SetWriteDeadline(time.Time) error { return nil }
func (c *conditionalTestConn) data() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.buf.Bytes())
}

func conditionalFixture(t *testing.T) (*apiServer, *Pane, *conditionalTestConn, conditionalSubmitRequest, *http.ServeMux) {
	t.Helper()
	if !conditionalSubmitSupported() {
		t.Skip("Linux conditional foreground probe")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	conn := &conditionalTestConn{}
	p := &Pane{ID: "conditional-pane", Pid: 100, CreatedAt: time.Now(), Cwd: root,
		Status: PaneRunning, Agent: agentCodex, CodexThreadID: "fixture-session", agentConn: conn, buffer: newTestRing()}
	p.buffer.Write([]byte("› \n  gpt-6-sol · /fixture\n"))
	mgr := NewManager(t.TempDir())
	mgr.panes[p.ID] = p
	a := &apiServer{mgr: mgr, conditionalSubmitProbe: func(context.Context, int) (conditionalSubmitProcess, error) {
		return conditionalSubmitProcess{Provider: "codex", Pid: 102, StartTime: "12345", Cwd: root}, nil
	}}
	mux := http.NewServeMux()
	a.registerConditionalSubmitRoutes(mux)
	mux.HandleFunc("POST /api/panes/{id}/inject", a.injectPane)
	ready := a.runConditionalSubmit(context.Background(), p.ID, nil)
	if ready.outcome != "ready" {
		t.Fatalf("fixture not ready: %+v", ready)
	}
	return a, p, conn, conditionalSubmitRequest{conditionalSubmitCondition: ready.condition, Text: "RECORDED_HANDOFF fixture"}, mux
}

func conditionalRequest(t *testing.T, mux http.Handler, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, bytes.NewReader(raw)))
	var value map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &value); err != nil {
		t.Fatalf("non-JSON %d: %s", rec.Code, rec.Body.String())
	}
	return rec, value
}

func TestConditionalSubmitCapabilityAndSplitFrames(t *testing.T) {
	a, p, conn, req, mux := conditionalFixture(t)
	_, caps := conditionalRequest(t, mux, "GET", "/api/pane-input/capabilities", nil)
	if caps["supported"] != true || caps["terminalOnly"] != true || caps["conditionalSubmitVersion"] != float64(1) || caps["receipt"] != "transport_only" {
		t.Fatal(caps)
	}
	_, ready := conditionalRequest(t, mux, "GET", "/api/panes/conditional-pane/conditional-submit", nil)
	if ready["outcome"] != "ready" || ready["condition"] == nil || len(conn.data()) != 0 || p.LastInputMs != 0 || p.LastAckedChoicePromptPos != 0 {
		t.Fatal("GET mutated input", ready)
	}
	callbacks := 0
	p.onPromptsCommit = func(current *Pane) {
		callbacks++
		if !p.mu.TryLock() {
			t.Error("callback inside pane lock")
			return
		}
		p.mu.Unlock()
		if !p.agentWriteMu.TryLock() {
			t.Error("callback inside writer lock")
			return
		}
		p.agentWriteMu.Unlock()
		if !a.mgr.mu.TryLock() {
			t.Error("callback inside manager lease")
			return
		}
		a.mgr.mu.Unlock()
		if !a.mgr.withCurrentPane(current, func() { _ = current.Snapshot() }) {
			t.Error("lost current callback")
		}
	}
	rec, result := conditionalRequest(t, mux, "POST", "/api/panes/conditional-pane/conditional-submit", req)
	if rec.Code != 200 || result["outcome"] != "transport_written" || result["bytes"] != float64(len(req.Text)+1) {
		t.Fatal(rec.Code, result)
	}
	reader := bytes.NewReader(conn.data())
	kind, payload, err := ReadFrame(reader)
	if err != nil || kind != FrameData || string(payload) != "\x1b[200~"+req.Text+"\x1b[201~" {
		t.Fatalf("frame=%d %q %v", kind, payload, err)
	}
	kind, payload, err = ReadFrame(reader)
	if err != nil || kind != FrameData || string(payload) != "\r" || reader.Len() != 0 {
		t.Fatalf("Enter=%d %q %v", kind, payload, err)
	}
	if callbacks != 1 || len(p.LastPrompts) != 1 || p.LastPrompts[0] != req.Text || !p.fleetTurnAdmitted.Load() {
		t.Fatalf("bookkeeping: %d %+v", callbacks, p.LastPrompts)
	}
	before := len(conn.data())
	_, result = conditionalRequest(t, mux, "POST", "/api/panes/conditional-pane/conditional-submit", req)
	if result["outcome"] != "not_sent" || len(conn.data()) != before {
		t.Fatal("second submission before output was admitted", result)
	}
}

func TestConditionalSubmitRefusalsWriteNothing(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*apiServer, *Pane, *conditionalSubmitRequest)
	}{
		{"pid", func(_ *apiServer, _ *Pane, r *conditionalSubmitRequest) { r.Pid++ }},
		{"created", func(_ *apiServer, _ *Pane, r *conditionalSubmitRequest) { r.CreatedAt = r.CreatedAt.Add(time.Second) }},
		{"foreground_pid", func(_ *apiServer, _ *Pane, r *conditionalSubmitRequest) { r.ForegroundPid++ }},
		{"foreground_restart", func(_ *apiServer, _ *Pane, r *conditionalSubmitRequest) { r.ForegroundStartTime = "12346" }},
		{"provider", func(_ *apiServer, _ *Pane, r *conditionalSubmitRequest) { r.Provider = "claude" }},
		{"session_rotated", func(_ *apiServer, p *Pane, _ *conditionalSubmitRequest) { p.CodexThreadID = "another" }},
		{"session_unknown", func(_ *apiServer, p *Pane, _ *conditionalSubmitRequest) { p.CodexThreadID = "" }},
		{"cwd", func(_ *apiServer, _ *Pane, r *conditionalSubmitRequest) { r.Cwd += "/elsewhere" }},
		{"live_cwd", func(a *apiServer, _ *Pane, _ *conditionalSubmitRequest) {
			a.conditionalSubmitProbe = func(context.Context, int) (conditionalSubmitProcess, error) {
				return conditionalSubmitProcess{Provider: "codex", Pid: 102, StartTime: "12345", Cwd: "/other"}, nil
			}
		}},
		{"shell_fallback", func(a *apiServer, _ *Pane, _ *conditionalSubmitRequest) {
			a.conditionalSubmitProbe = func(context.Context, int) (conditionalSubmitProcess, error) {
				return conditionalSubmitProcess{}, errConditionalProcessUnavailable
			}
		}},
		{"native", func(_ *apiServer, p *Pane, _ *conditionalSubmitRequest) { p.Kind = "native" }},
		{"closing", func(_ *apiServer, p *Pane, _ *conditionalSubmitRequest) { p.userClosing = true }},
		{"respawning", func(_ *apiServer, p *Pane, _ *conditionalSubmitRequest) { p.respawning.Store(true) }},
		{"migrating", func(_ *apiServer, p *Pane, _ *conditionalSubmitRequest) { p.codexLiveMigrating.Store(true) }},
		{"disconnected", func(_ *apiServer, p *Pane, _ *conditionalSubmitRequest) { p.agentConn = nil }},
		{"exited", func(_ *apiServer, p *Pane, _ *conditionalSubmitRequest) { p.Status = PaneExited }},
		{"draft", func(_ *apiServer, p *Pane, _ *conditionalSubmitRequest) { p.promptRec.buf = []byte("operator draft") }},
		{"unknown_screen", func(_ *apiServer, p *Pane, _ *conditionalSubmitRequest) { p.buffer = newTestRing() }},
		{"busy", func(_ *apiServer, p *Pane, _ *conditionalSubmitRequest) {
			p.buffer.Write([]byte("Working (1s · esc to interrupt)\n"))
		}},
		{"permission", func(_ *apiServer, p *Pane, _ *conditionalSubmitRequest) {
			p.buffer.Write([]byte("❯ 1. Allow\n  2. Deny\n"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, p, conn, req, mux := conditionalFixture(t)
			tc.mutate(a, p, &req)
			rec, result := conditionalRequest(t, mux, "POST", "/api/panes/conditional-pane/conditional-submit", req)
			if rec.Code != 412 || result["outcome"] != "not_sent" || len(conn.data()) != 0 || p.LastInputMs != 0 || len(p.LastPrompts) != 0 {
				t.Fatal(rec.Code, result)
			}
		})
	}
}

func TestConditionalSubmitStrictInputAndLegacyUnchanged(t *testing.T) {
	_, _, conn, req, mux := conditionalFixture(t)
	for _, text := range []string{"", "   ", "line\nnext", "message\r", "\x1b[200~body", strings.Repeat("界", 317)} {
		req.Text = text
		rec, result := conditionalRequest(t, mux, "POST", "/api/panes/conditional-pane/conditional-submit", req)
		if rec.Code != 400 || result["outcome"] != "not_sent" {
			t.Fatal(rec.Code, result)
		}
	}
	for _, body := range []string{`{"text":"hello"}`, `{"text":"hello","unknown":true}`, `{} {}`} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/panes/conditional-pane/conditional-submit", strings.NewReader(body)))
		if rec.Code != 400 {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	if len(conn.data()) != 0 {
		t.Fatal("bad requests wrote input")
	}
	rec, result := conditionalRequest(t, mux, "POST", "/api/panes/conditional-pane/inject", map[string]any{"text": "legacy\r", "expectedPid": 999})
	if rec.Code != 200 || result["bytes"] != float64(7) {
		t.Fatal("legacy inject changed", rec.Code, result)
	}
	_, payload, err := ReadFrame(bytes.NewReader(conn.data()))
	if err != nil || string(payload) != "legacy\r" {
		t.Fatal(string(payload), err)
	}
}

func TestConditionalSubmitChangesDuringProbe(t *testing.T) {
	for _, mode := range []string{"exit", "restart", "session", "cwd", "prompt"} {
		t.Run(mode, func(t *testing.T) {
			a, p, conn, req, mux := conditionalFixture(t)
			original := a.conditionalSubmitProbe
			calls := 0
			a.conditionalSubmitProbe = func(ctx context.Context, pid int) (conditionalSubmitProcess, error) {
				calls++
				process, err := original(ctx, pid)
				if calls == 2 {
					switch mode {
					case "exit":
						return conditionalSubmitProcess{}, errConditionalProcessUnavailable
					case "restart":
						process.StartTime = "99999"
					case "session":
						p.SetCodexThreadID("rotated-during-probe")
					case "cwd":
						p.mu.Lock()
						p.Cwd = "/another-worktree"
						p.mu.Unlock()
					case "prompt":
						p.buffer.Write([]byte("Working (1s · esc to interrupt)\n"))
					}
				}
				return process, err
			}
			rec, result := conditionalRequest(t, mux, "POST", "/api/panes/conditional-pane/conditional-submit", req)
			if rec.Code != 412 || result["outcome"] != "not_sent" || len(conn.data()) != 0 {
				t.Fatal(rec.Code, result)
			}
		})
	}
}

func TestConditionalSubmitReplacementLeaseAndCallback(t *testing.T) {
	a, p, conn, req, _ := conditionalFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	conn.beforeWrite = func() { once.Do(func() { close(entered); <-release }) }
	callback := make(chan struct{})
	p.onPromptsCommit = func(current *Pane) {
		a.mgr.withCurrentPane(current, func() { _ = current.Snapshot() })
		close(callback)
	}
	result := make(chan conditionalSubmitResult, 1)
	go func() { result <- a.runConditionalSubmit(context.Background(), p.ID, &req) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("write did not start")
	}
	replaced := make(chan struct{})
	go func() { a.mgr.mu.Lock(); a.mgr.panes[p.ID] = &Pane{ID: p.ID}; a.mgr.mu.Unlock(); close(replaced) }()
	select {
	case <-replaced:
		t.Fatal("replacement bypassed membership lease")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case got := <-result:
		if got.outcome != "transport_written" {
			t.Fatal(got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("callback/queued-writer deadlock")
	}
	select {
	case <-callback:
	case <-time.After(time.Second):
		t.Fatal("callback missing")
	}
	select {
	case <-replaced:
	case <-time.After(time.Second):
		t.Fatal("replacement stuck")
	}
}

func TestConditionalSubmitDoesNotInterleaveOrdinaryInput(t *testing.T) {
	a, p, conn, req, _ := conditionalFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	conn.beforeWrite = func() { once.Do(func() { close(entered); <-release }) }
	finished := make(chan conditionalSubmitResult, 1)
	go func() { finished <- a.runConditionalSubmit(context.Background(), p.ID, &req) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("write did not start")
	}
	ordinary := make(chan error, 1)
	go func() { _, err := p.Write([]byte("operator")); ordinary <- err }()
	select {
	case err := <-ordinary:
		t.Fatal("ordinary input crossed transaction", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case result := <-finished:
		if result.outcome != "transport_written" {
			t.Fatal(result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("submit stuck")
	}
	select {
	case err := <-ordinary:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("ordinary input stuck")
	}
	r := bytes.NewReader(conn.data())
	for _, want := range []string{"\x1b[200~" + req.Text + "\x1b[201~", "\r", "operator"} {
		_, payload, err := ReadFrame(r)
		if err != nil || string(payload) != want {
			t.Fatal(string(payload), err)
		}
	}
	if r.Len() != 0 {
		t.Fatal("extra frames")
	}
}

func TestConditionalSubmitTransportFailureIsAmbiguous(t *testing.T) {
	for _, mode := range []string{"error", "short", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			a, p, conn, req, _ := conditionalFixture(t)
			switch mode {
			case "error":
				conn.writeError = errors.New("fixture transport failure")
			case "short":
				conn.short = true
			case "deadline":
				local, remote := net.Pipe()
				t.Cleanup(func() { local.Close(); remote.Close() })
				p.agentConn = local
			}
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			started := time.Now()
			result := a.runConditionalSubmit(ctx, p.ID, &req)
			if result.outcome != "unknown" || result.status != 502 || time.Since(started) > time.Second || p.LastInputMs != 0 || len(p.LastPrompts) != 0 {
				t.Fatal(result)
			}
			if mode != "deadline" && !conn.closed {
				t.Fatal("failed framing left transport usable")
			}
		})
	}
}

func TestConditionalSubmitLockWaitIsBoundedAndCancelled(t *testing.T) {
	for _, which := range []string{"manager", "input", "pane", "screen", "ring"} {
		t.Run(which, func(t *testing.T) {
			a, p, conn, req, _ := conditionalFixture(t)
			var mu sync.Locker
			var reason string
			switch which {
			case "manager":
				mu, reason = &a.mgr.mu, "membership_busy"
			case "input":
				mu, reason = &p.agentWriteMu, "input_busy"
			case "pane":
				mu, reason = &p.mu, "pane_busy"
			case "screen":
				mu, reason = &p.subsMu, "not_idle"
			case "ring":
				mu, reason = &p.buffer.mu, "screen_busy"
			}
			mu.Lock()
			defer mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			started := time.Now()
			result := a.runConditionalSubmit(ctx, p.ID, &req)
			if result.outcome != "not_sent" || result.reason != reason || len(conn.data()) != 0 || time.Since(started) > time.Second {
				t.Fatal(result)
			}
		})
	}
}

func TestConditionalSubmitCurrentPaneContextLease(t *testing.T) {
	a, p, _, _, _ := conditionalFixture(t)
	called := false
	stale := &Pane{ID: p.ID}
	current, err := a.mgr.withCurrentPaneContext(context.Background(), stale, func() { called = true })
	if current || err != nil || called {
		t.Fatal("stale pointer admitted", current, err, called)
	}
	a.mgr.mu.Lock()
	defer a.mgr.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	current, err = a.mgr.withCurrentPaneContext(ctx, p, func() { called = true })
	if current || !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatal("blocked membership not cancelled", current, err, called)
	}
}

func TestConditionalSubmitCancellationDuringProbe(t *testing.T) {
	a, p, conn, req, _ := conditionalFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := a.conditionalSubmitProbe
	a.conditionalSubmitProbe = func(ctx context.Context, pid int) (conditionalSubmitProcess, error) {
		cancel()
		return original(ctx, pid)
	}
	result := a.runConditionalSubmit(ctx, p.ID, &req)
	if result.outcome != "not_sent" || len(conn.data()) != 0 || p.fleetTurnAdmitted.Load() {
		t.Fatal(result)
	}
}

func TestConditionalSubmitFastResponseProgressIsPreserved(t *testing.T) {
	a, p, conn, req, _ := conditionalFixture(t)
	conn.beforeWrite = func() {
		// Only Enter can cause a model response; body echo is not acceptance.
		if len(conn.data()) >= 5+len("\x1b[200~"+req.Text+"\x1b[201~") {
			atomic.AddInt64(&p.TotalStrippedChars, 20)
		}
	}
	result := a.runConditionalSubmit(context.Background(), p.ID, &req)
	if result.outcome != "transport_written" || p.fleetTurnOutputWatermark.Load() != 0 || !conditionalPromptIdle(context.Background(), p, "codex") {
		t.Fatal("response progress lost behind admission latch", result)
	}
}

func TestConditionalSubmitClaudeIdleAndBusy(t *testing.T) {
	for _, busy := range []bool{false, true} {
		a, p, conn, req, _ := conditionalFixture(t)
		p.Agent, p.ClaudeSessionID = agentClaude, "claude-fixture-session"
		p.buffer = newTestRing()
		p.buffer.Write([]byte("❯\n ~ [Opus 4.8 (1M context) | ctx: 3%]\n"))
		original := a.conditionalSubmitProbe
		a.conditionalSubmitProbe = func(ctx context.Context, pid int) (conditionalSubmitProcess, error) {
			process, err := original(ctx, pid)
			process.Provider = "claude"
			return process, err
		}
		ready := a.runConditionalSubmit(context.Background(), p.ID, nil)
		if ready.outcome != "ready" {
			t.Fatal("idle Claude refused", ready)
		}
		req.conditionalSubmitCondition = ready.condition
		if busy {
			p.buffer.Write([]byte("✢ Scampering…\n"))
		}
		result := a.runConditionalSubmit(context.Background(), p.ID, &req)
		if busy {
			if result.outcome != "not_sent" || len(conn.data()) != 0 {
				t.Fatal("busy Claude accepted", result)
			}
		} else if result.outcome != "transport_written" {
			t.Fatal("idle Claude refused", result)
		}
	}
}

func TestConditionalSubmitPostProbeExitNeverClaimsReceipt(t *testing.T) {
	a, p, conn, req, _ := conditionalFixture(t)
	var exited atomic.Bool
	conn.beforeWrite = func() { exited.Store(true) } // OS exit is not synchronized by pane locks.
	result := a.runConditionalSubmit(context.Background(), p.ID, &req)
	if !exited.Load() || result.outcome != "transport_written" {
		t.Fatal(result)
	}
	rec := httptest.NewRecorder()
	result.respond(rec)
	if strings.Contains(rec.Body.String(), "received") || strings.Contains(rec.Body.String(), "modelReceipt") {
		t.Fatal(rec.Body.String())
	}
}
