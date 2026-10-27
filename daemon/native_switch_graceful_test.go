package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureNativePane builds a native-mode Pane wired to an in-memory agent
// connection whose write side we capture as decoded JSONL frames. It lets the
// graceful-interrupt path actually write its EncodeInterrupt frame so a test can
// assert it landed. The returned frames() accessor decodes every FrameData
// payload written so far.
func captureNativePane(t *testing.T, id string) (p *Pane, frames func() []map[string]any) {
	t.Helper()
	c1, c2 := net.Pipe()
	t.Cleanup(func() { _ = c1.Close(); _ = c2.Close() })

	var mu sync.Mutex
	var raw [][]byte
	go func() {
		for {
			ft, payload, err := ReadFrame(c2)
			if err != nil {
				return
			}
			if ft == FrameData {
				mu.Lock()
				raw = append(raw, append([]byte(nil), payload...))
				mu.Unlock()
			}
		}
	}()

	p = &Pane{
		ID:        id,
		Kind:      "native",
		Status:    PaneRunning,
		Cwd:       "/tmp",
		Cmd:       []string{"claude", "-p"},
		done:      make(chan struct{}),
		agentConn: c1,
		buffer:    NewRingBuffer(1024),
		subs:      map[int64]*subEntry{},
	}
	p.nativeAdapter = NewClaudeAdapter()
	p.nativeSubs = map[int64]chan NativeEvent{}
	p.nativeStop = make(chan struct{})

	frames = func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		out := make([]map[string]any, 0, len(raw))
		for _, b := range raw {
			// A single write may carry multiple newline-delimited JSON frames.
			for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				var m map[string]any
				if json.Unmarshal([]byte(line), &m) == nil {
					out = append(out, m)
				}
			}
		}
		return out
	}
	return p, frames
}

// TestIsAgentWorkingNative_SetAndClearedAcrossTurn proves the native in-turn flag
// is the IsAgentWorking() source: false at rest, true once streaming starts,
// false again on turn_end.
func TestIsAgentWorkingNative_SetAndClearedAcrossTurn(t *testing.T) {
	p := newTestNativePane()

	if p.IsAgentWorking() {
		t.Fatal("fresh native pane reports working; want idle")
	}
	// Assistant message starts streaming → working.
	p.emitNativeEvent(NativeEvent{Type: EvTextDelta, Text: "hel"})
	if !p.IsAgentWorking() {
		t.Fatal("after a text_delta the native pane must report working")
	}
	// turn_end clears it.
	p.emitNativeEvent(NativeEvent{Type: EvTurnEnd, Subtype: "success"})
	if p.IsAgentWorking() {
		t.Fatal("after turn_end the native pane must report idle")
	}
	// An error also clears it.
	p.emitNativeEvent(NativeEvent{Type: EvToolCall, Name: "Bash"})
	if !p.IsAgentWorking() {
		t.Fatal("a tool_call must mark the pane working")
	}
	p.emitNativeEvent(NativeEvent{Type: EvError, Message: "boom"})
	if p.IsAgentWorking() {
		t.Fatal("after error the native pane must report idle")
	}
}

// TestIsAgentWorkingNative_UserTurnMarksWorking proves submitting a user turn
// marks the pane working BEFORE any byte streams back (the markNativeUserTurn
// path used by paneNativeInput).
func TestIsAgentWorkingNative_UserTurnMarksWorking(t *testing.T) {
	p := newTestNativePane()
	if p.IsAgentWorking() {
		t.Fatal("fresh native pane should be idle")
	}
	p.markNativeUserTurn()
	if !p.IsAgentWorking() {
		t.Fatal("after a submitted user turn the pane must report working")
	}
}

// TestIsAgentWorkingNative_IgnoresReplay proves transcript-seeded (FromReplay)
// events do NOT light the in-turn flag — a reattach must not look working.
func TestIsAgentWorkingNative_IgnoresReplay(t *testing.T) {
	p := newTestNativePane()
	p.emitNativeEvent(NativeEvent{Type: EvText, Text: "old", FromReplay: true})
	if p.IsAgentWorking() {
		t.Fatal("replayed events must not mark the pane working")
	}
}

// TestIsAgentWorkingTerminal_BusyState proves terminal panes dispatch to
// computeState recency: fresh activity reads working; a long-idle pane doesn't.
func TestIsAgentWorkingTerminal_BusyState(t *testing.T) {
	p := &Pane{
		ID:     "term",
		Kind:   "",
		Status: PaneRunning,
		buffer: NewRingBuffer(1024),
	}
	// Fresh activity → StateBusy → working.
	p.LastActivityMs = time.Now().UnixMilli()
	if !p.IsAgentWorking() {
		t.Fatal("terminal pane with fresh activity must report working")
	}
	// Long idle → StateIdle → not working.
	p.LastActivityMs = time.Now().Add(-10 * time.Second).UnixMilli()
	if p.IsAgentWorking() {
		t.Fatal("long-idle terminal pane must report idle")
	}
}

// TestGracefulInterruptNative_WritesInterruptBeforeReturn proves a working native
// pane is sent an EncodeInterrupt frame, the in-turn flag clears (so the wait
// settles), and wasMidTurn is reported true.
func TestGracefulInterruptNative_WritesInterruptBeforeReturn(t *testing.T) {
	p, frames := captureNativePane(t, "pane_intr")
	// Mark working, then have the settle-wait observe a clear once the interrupt
	// is sent: simulate the agent's result by clearing the flag from a goroutine
	// shortly after the call begins.
	p.markNativeUserTurn()
	go func() {
		time.Sleep(40 * time.Millisecond)
		p.nativeInTurn.Store(false) // mimic the result/turn_end ack
	}()

	api := &apiServer{}
	snap := p.Snapshot()
	start := time.Now()
	wasMidTurn := api.gracefullyInterruptForSwitch(p, snap, "") // native → terminal
	elapsed := time.Since(start)

	if !wasMidTurn {
		t.Fatal("wasMidTurn = false, want true for a working native pane")
	}
	if elapsed >= switchModeSettleTimeout {
		t.Fatalf("settle took %v (>= hard cap %v) — should have returned once the flag cleared", elapsed, switchModeSettleTimeout)
	}
	// Give the capture goroutine a beat to drain the pipe.
	deadline := time.Now().Add(time.Second)
	var sawInterrupt bool
	for time.Now().Before(deadline) {
		for _, f := range frames() {
			if f["type"] == "control_request" {
				if req, ok := f["request"].(map[string]any); ok && req["subtype"] == "interrupt" {
					sawInterrupt = true
				}
			}
		}
		if sawInterrupt {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !sawInterrupt {
		t.Fatalf("no interrupt control_request frame written; frames=%v", frames())
	}
}

// TestGracefulInterruptNative_NoOpWhenIdle proves an idle pane is not interrupted
// and wasMidTurn is false (no frame written, instant return).
func TestGracefulInterruptNative_NoOpWhenIdle(t *testing.T) {
	p, frames := captureNativePane(t, "pane_idle")
	// not working.
	api := &apiServer{}
	if api.gracefullyInterruptForSwitch(p, p.Snapshot(), "") {
		t.Fatal("idle native pane reported mid-turn")
	}
	time.Sleep(50 * time.Millisecond)
	if got := frames(); len(got) != 0 {
		t.Fatalf("idle pane was sent %d frames, want 0: %v", len(got), got)
	}
}

// TestSwitchModeSetsSwitchNoticeWhenMidTurn drives the full handler: a native
// pane that is mid-turn is switched to terminal; the handler must (a) interrupt
// before the kill and (b) stamp the switchNotice on the new pane DTO with the
// correct shape (interrupted/fromKind/at).
func TestSwitchModeSetsSwitchNoticeWhenMidTurn(t *testing.T) {
	t.Setenv("SOLU_NATIVE_RENDERER", "1")
	// Point HOME at a temp dir so the trust pre-seed can't touch the real file.
	t.Setenv("HOME", t.TempDir())

	mgr := NewManager(t.TempDir())
	store := &failingPaneStore{}

	orig, _ := captureNativePane(t, "pane_mid")
	orig.ClaudeSessionID = "sess-mid"
	mgr.panes[orig.ID] = orig
	orig.markNativeUserTurn() // mid-turn

	api := &apiServer{
		mgr:   mgr,
		panes: store,
		spawnPane: func(o SpawnOpts) (*Pane, error) {
			np := switchTestPane(o.ID, o.Kind, o.Cmd, o.ClaudeSessionID)
			mgr.panes[np.ID] = np
			return np, nil
		},
		killPane: func(id string) error { delete(mgr.panes, id); return nil },
	}

	req := httptest.NewRequest(http.MethodPost, "/api/panes/pane_mid/switch-mode", strings.NewReader(`{"kind":""}`))
	req.SetPathValue("id", "pane_mid")
	rec := httptest.NewRecorder()
	api.handleSwitchMode(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	var dto paneDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode DTO: %v; body=%q", err, rec.Body.String())
	}
	if dto.SwitchNotice == nil {
		t.Fatal("switchNotice missing; want interrupted notice after a mid-turn switch")
	}
	if !dto.SwitchNotice.Interrupted {
		t.Fatal("switchNotice.interrupted = false, want true")
	}
	if dto.SwitchNotice.FromKind != "native" {
		t.Fatalf("switchNotice.fromKind = %q, want native (the SOURCE kind)", dto.SwitchNotice.FromKind)
	}
	if dto.SwitchNotice.At <= 0 {
		t.Fatalf("switchNotice.at = %d, want a positive unix-millis timestamp", dto.SwitchNotice.At)
	}
}

// TestSwitchModeNoNoticeWhenIdle proves a switch on an IDLE pane carries no
// switch notice (nothing was interrupted).
func TestSwitchModeNoNoticeWhenIdle(t *testing.T) {
	t.Setenv("SOLU_NATIVE_RENDERER", "1")
	t.Setenv("HOME", t.TempDir())

	mgr := NewManager(t.TempDir())
	store := &failingPaneStore{}
	orig, _ := captureNativePane(t, "pane_calm")
	orig.ClaudeSessionID = "sess-calm"
	mgr.panes[orig.ID] = orig
	// NOT mid-turn.

	api := &apiServer{
		mgr:   mgr,
		panes: store,
		spawnPane: func(o SpawnOpts) (*Pane, error) {
			np := switchTestPane(o.ID, o.Kind, o.Cmd, o.ClaudeSessionID)
			mgr.panes[np.ID] = np
			return np, nil
		},
		killPane: func(id string) error { delete(mgr.panes, id); return nil },
	}

	req := httptest.NewRequest(http.MethodPost, "/api/panes/pane_calm/switch-mode", strings.NewReader(`{"kind":""}`))
	req.SetPathValue("id", "pane_calm")
	rec := httptest.NewRecorder()
	api.handleSwitchMode(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	var dto paneDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode DTO: %v", err)
	}
	if dto.SwitchNotice != nil {
		t.Fatalf("switchNotice = %+v, want nil for an idle switch", dto.SwitchNotice)
	}
}

// TestSwitchNoticeClearedOnNativeInput proves the notice is cleared once the user
// submits the next native user turn (paneNativeInput).
func TestSwitchNoticeClearedOnNativeInput(t *testing.T) {
	p, _ := captureNativePane(t, "pane_clear")
	p.setSwitchNotice("native")
	if p.switchNoticeSnapshot() == nil {
		t.Fatal("setSwitchNotice did not record a notice")
	}

	api := &apiServer{mgr: NewManager(t.TempDir())}
	api.mgr.panes[p.ID] = p

	req := httptest.NewRequest(http.MethodPost, "/api/panes/pane_clear/native/input", strings.NewReader(`{"text":"continue"}`))
	req.SetPathValue("id", "pane_clear")
	rec := httptest.NewRecorder()
	api.paneNativeInput(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("native input status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if p.switchNoticeSnapshot() != nil {
		t.Fatal("switch notice not cleared after the next user turn")
	}
	if got := p.Snapshot().LastPrompts; len(got) != 1 || got[0] != "continue" {
		t.Fatalf("native prompt history = %#v, want original user text only", got)
	}
}

// TestNativeInputEmitsUserTextWhenMidTurn proves the prompt-vanish root-cause fix:
// a submit while nativeInTurn is ALREADY true (a steer, OR a stale-true flag from a
// missed settle edge) STILL emits the input-time user_text AND arms echo-suppression
// — the old `freshTurn` gate skipped both on this path, which is how a prompt could
// vanish while the reply streamed.
func TestNativeInputEmitsUserTextWhenMidTurn(t *testing.T) {
	p, _ := captureNativePane(t, "pane_steer")
	// Simulate the agent already working (or a stale-true flag): freshTurn==false.
	p.nativeInTurn.Store(true)

	api := &apiServer{mgr: NewManager(t.TempDir())}
	api.mgr.panes[p.ID] = p

	req := httptest.NewRequest(http.MethodPost, "/api/panes/pane_steer/native/input", strings.NewReader(`{"text":"steer me"}`))
	req.SetPathValue("id", "pane_steer")
	rec := httptest.NewRecorder()
	api.paneNativeInput(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("native input status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}

	// The input-time user_text must be in the ring even though the pane was in-turn.
	users := 0
	for _, ty := range ringTypes(p) {
		if ty == EvUserText {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("want exactly one input-time user_text on a mid-turn submit, got %d", users)
	}
	// Suppression must be armed so the steer's own CLI replay echo is dropped (no dup).
	if !p.consumeEchoSuppress("steer me") {
		t.Fatal("suppression not armed for a mid-turn submit (steer echo would double-render)")
	}
}

// --- ensureFolderTrusted (trust pre-seed) -------------------------------------

// TestEnsureFolderTrusted_FlipsAndPreserves proves ensureFolderTrusted flips the
// per-project boolean while preserving EVERY other key/value (including a
// credentials-looking field), using a temp HOME — never the real ~/.claude.json.
func TestEnsureFolderTrusted_FlipsAndPreserves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude.json")

	cwd := "/work/repo-A"
	fixture := map[string]any{
		"numStartups":   7,
		"oauthAccount":  map[string]any{"emailAddress": "x@example.com"},
		"primaryApiKey": "sk-ant-SECRET-DO-NOT-DROP",
		"some":          []any{"a", "b", "c"},
		"projects": map[string]any{
			cwd: map[string]any{
				"hasTrustDialogAccepted": false,
				"history":                []any{"prompt-1", "prompt-2"},
				"allowedTools":           []any{"Bash"},
			},
			"/work/repo-B": map[string]any{
				"hasTrustDialogAccepted": true,
				"history":                []any{"keep-me"},
			},
		},
	}
	writeJSONFile(t, path, fixture, 0o600)

	if err := ensureFolderTrusted(cwd); err != nil {
		t.Fatalf("ensureFolderTrusted: %v", err)
	}

	got := readJSONFile(t, path)

	// Top-level credentials/keys preserved.
	if got["primaryApiKey"] != "sk-ant-SECRET-DO-NOT-DROP" {
		t.Fatalf("primaryApiKey lost/changed: %#v", got["primaryApiKey"])
	}
	if n, _ := got["numStartups"].(float64); n != 7 {
		t.Fatalf("numStartups = %v, want 7", got["numStartups"])
	}
	oauth, _ := got["oauthAccount"].(map[string]any)
	if oauth == nil || oauth["emailAddress"] != "x@example.com" {
		t.Fatalf("oauthAccount lost/changed: %#v", got["oauthAccount"])
	}

	projects, _ := got["projects"].(map[string]any)
	if projects == nil {
		t.Fatal("projects map gone")
	}
	// Target flipped.
	entry, _ := projects[cwd].(map[string]any)
	if entry == nil {
		t.Fatalf("project entry for %q gone", cwd)
	}
	if v, _ := entry["hasTrustDialogAccepted"].(bool); !v {
		t.Fatalf("hasTrustDialogAccepted = %v, want true (flipped)", entry["hasTrustDialogAccepted"])
	}
	// Sibling fields in the SAME entry preserved.
	hist, _ := entry["history"].([]any)
	if len(hist) != 2 || hist[0] != "prompt-1" {
		t.Fatalf("target entry history lost: %#v", entry["history"])
	}
	if tools, _ := entry["allowedTools"].([]any); len(tools) != 1 || tools[0] != "Bash" {
		t.Fatalf("target entry allowedTools lost: %#v", entry["allowedTools"])
	}
	// OTHER project untouched.
	other, _ := projects["/work/repo-B"].(map[string]any)
	if other == nil {
		t.Fatal("sibling project /work/repo-B gone")
	}
	if v, _ := other["hasTrustDialogAccepted"].(bool); !v {
		t.Fatal("sibling project trust flag changed")
	}
	if h, _ := other["history"].([]any); len(h) != 1 || h[0] != "keep-me" {
		t.Fatalf("sibling project history lost: %#v", other["history"])
	}

	// File mode preserved.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file mode = %v, want 0600", perm)
	}
}

// TestEnsureFolderTrusted_IdempotentNoOp proves that when the flag is already
// true, the file is NOT rewritten (mtime unchanged) — a true no-op.
func TestEnsureFolderTrusted_IdempotentNoOp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude.json")

	cwd := "/already/trusted"
	writeJSONFile(t, path, map[string]any{
		"projects": map[string]any{
			cwd: map[string]any{"hasTrustDialogAccepted": true},
		},
	}, 0o600)

	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}
	// Make any rewrite observable via mtime.
	old := before.ModTime().Add(-2 * time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	beforeMtime := old

	if err := ensureFolderTrusted(cwd); err != nil {
		t.Fatalf("ensureFolderTrusted: %v", err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !after.ModTime().Equal(beforeMtime) {
		t.Fatalf("file was rewritten (mtime %v != %v); want no-op when already trusted", after.ModTime(), beforeMtime)
	}
}

// TestEnsureFolderTrusted_CreatesEntryWhenMissing proves a cwd with no entry (or
// no projects map) gets a fresh trusted entry, preserving other keys.
func TestEnsureFolderTrusted_CreatesEntryWhenMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude.json")

	writeJSONFile(t, path, map[string]any{
		"primaryApiKey": "sk-keep",
		// no projects map at all
	}, 0o644)

	cwd := "/fresh/dir"
	if err := ensureFolderTrusted(cwd); err != nil {
		t.Fatalf("ensureFolderTrusted: %v", err)
	}
	got := readJSONFile(t, path)
	if got["primaryApiKey"] != "sk-keep" {
		t.Fatalf("primaryApiKey lost: %#v", got["primaryApiKey"])
	}
	projects, _ := got["projects"].(map[string]any)
	entry, _ := projects[cwd].(map[string]any)
	if entry == nil {
		t.Fatalf("entry for %q not created", cwd)
	}
	if v, _ := entry["hasTrustDialogAccepted"].(bool); !v {
		t.Fatal("new entry not trusted")
	}
	// Mode preserved.
	info, _ := os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("file mode = %v, want 0644", perm)
	}
}

// TestEnsureFolderTrusted_MissingFileNoError proves a non-existent ~/.claude.json
// is a silent no-op (claude creates it on first run).
func TestEnsureFolderTrusted_MissingFileNoError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := ensureFolderTrusted("/anything"); err != nil {
		t.Fatalf("ensureFolderTrusted on missing file = %v, want nil (no-op)", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Fatal("ensureFolderTrusted created the file; want it left absent")
	}
}

func writeJSONFile(t *testing.T, path string, v any, mode os.FileMode) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(path, b, mode); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return m
}

// captureTerminalPane builds a live terminal-mode Pane wired to an in-memory
// agent connection whose raw FrameData payloads we capture — the terminal
// interrupt path writes raw ESC bytes (not JSONL), so the native frame decoder
// doesn't fit here.
func captureTerminalPane(t *testing.T, id string) (p *Pane, payloads func() [][]byte) {
	t.Helper()
	c1, c2 := net.Pipe()
	t.Cleanup(func() { _ = c1.Close(); _ = c2.Close() })

	var mu sync.Mutex
	var raw [][]byte
	go func() {
		for {
			ft, payload, err := ReadFrame(c2)
			if err != nil {
				return
			}
			if ft == FrameData {
				mu.Lock()
				raw = append(raw, append([]byte(nil), payload...))
				mu.Unlock()
			}
		}
	}()

	p = &Pane{
		ID:        id,
		Kind:      "",
		Status:    PaneRunning,
		Cwd:       "/tmp",
		Cmd:       []string{"claude"},
		done:      make(chan struct{}),
		agentConn: c1,
		buffer:    NewRingBuffer(8192),
		subs:      map[int64]*subEntry{},
	}
	payloads = func() [][]byte {
		mu.Lock()
		defer mu.Unlock()
		out := make([][]byte, len(raw))
		copy(out, raw)
		return out
	}
	return p, payloads
}

// claudePromptFixture is a buffer tail that looksLikeClaudePrompt matches (the
// legacy box-drawing prompt from state_test.go's table).
const claudePromptFixture = "some content above\n" +
	"╭──────────────────╮\n" +
	"│ >                │\n" +
	"╰──────────────────╯\n" +
	"  ? for shortcuts\n"

// TestGracefulInterruptTerminal_PromptSignalSkipsSecondEsc proves the terminal
// interrupt is signal-driven, not fixed-sleep: when Claude's "interrupted"
// prompt box appears quickly after the first ESC, the second ESC is skipped and
// the interrupt returns well under the old fixed ~500ms wait.
func TestGracefulInterruptTerminal_PromptSignalSkipsSecondEsc(t *testing.T) {
	p, payloads := captureTerminalPane(t, "pane_term_intr")
	p.LastActivityMs = time.Now().UnixMilli() // IsAgentWorking() == true (busy recency)

	// Simulate Claude aborting on the first ESC: the prompt box redraw lands in
	// the ring shortly after the interrupt begins.
	go func() {
		time.Sleep(40 * time.Millisecond)
		_, _ = p.buffer.Write([]byte(claudePromptFixture))
	}()

	api := &apiServer{}
	snap := p.Snapshot()
	start := time.Now()
	wasMidTurn := api.gracefullyInterruptForSwitch(p, snap, "native")
	elapsed := time.Since(start)

	if !wasMidTurn {
		t.Fatal("working terminal pane must report wasMidTurn=true")
	}
	// Old behavior: a fixed 150ms+350ms = 500ms. Signal-driven: first wait sees
	// the prompt at ~40-75ms, second wait exits on its immediate cond() check.
	if elapsed >= 400*time.Millisecond {
		t.Fatalf("interrupt took %v — prompt signal did not short-circuit the fixed sleeps", elapsed)
	}
	escs := 0
	for _, b := range payloads() {
		if len(b) == 1 && b[0] == 0x1b {
			escs++
		}
	}
	if escs != 1 {
		t.Fatalf("ESC writes = %d, want 1 (second ESC must be skipped once the prompt is visible)", escs)
	}
}

// TestGracefulInterruptTerminal_NoSignalKeepsWorstCase proves the bounded
// fallback is unchanged: when no prompt ever appears, both ESCs are sent and
// the interrupt waits out (about) the same worst-case window as before.
func TestGracefulInterruptTerminal_NoSignalKeepsWorstCase(t *testing.T) {
	p, payloads := captureTerminalPane(t, "pane_term_intr2")
	p.LastActivityMs = time.Now().UnixMilli()

	api := &apiServer{}
	snap := p.Snapshot()
	start := time.Now()
	wasMidTurn := api.gracefullyInterruptForSwitch(p, snap, "native")
	elapsed := time.Since(start)

	if !wasMidTurn {
		t.Fatal("working terminal pane must report wasMidTurn=true")
	}
	wantMin := switchModeSecondInterruptDelay + switchModeTerminalGrace - 50*time.Millisecond
	if elapsed < wantMin {
		t.Fatalf("interrupt returned at %v with no settle signal, want ≳%v (bounded worst case preserved)", elapsed, wantMin)
	}
	escs := 0
	for _, b := range payloads() {
		if len(b) == 1 && b[0] == 0x1b {
			escs++
		}
	}
	if escs != 2 {
		t.Fatalf("ESC writes = %d, want 2 (no signal → stubborn-TUI second ESC)", escs)
	}
}

// TestSwitchNoticeReasonRoundTrip locks in the generalized notice contract:
// reason rides the snapshot (and the wire) for resume-repair / fresh-start,
// stays omitted for the legacy switch-interrupt (back-compat: old clients see
// the exact same JSON), and fresh-start is informational (interrupted=false).
func TestSwitchNoticeReasonRoundTrip(t *testing.T) {
	p := &Pane{}

	p.setSwitchNotice("native") // legacy switch-interrupt
	n := p.switchNoticeSnapshot()
	if n == nil || !n.Interrupted || n.FromKind != "native" || n.Reason != "" {
		t.Fatalf("legacy notice = %+v, want interrupted native with empty reason", n)
	}
	b, _ := json.Marshal(n)
	if strings.Contains(string(b), "reason") {
		t.Fatalf("legacy notice JSON leaks a reason field: %s (wire back-compat broken)", b)
	}

	p.setSwitchNoticeReason("native", "resume-repair", true)
	n = p.switchNoticeSnapshot()
	if n == nil || !n.Interrupted || n.Reason != "resume-repair" {
		t.Fatalf("resume-repair notice = %+v", n)
	}
	if b, _ := json.Marshal(n); !strings.Contains(string(b), `"reason":"resume-repair"`) {
		t.Fatalf("resume-repair JSON = %s, want reason on the wire", b)
	}

	p.setSwitchNoticeReason("", "fresh-start", false)
	n = p.switchNoticeSnapshot()
	if n == nil || n.Interrupted || n.FromKind != "terminal" || n.Reason != "fresh-start" {
		t.Fatalf("fresh-start notice = %+v, want informational terminal fresh-start", n)
	}

	p.clearSwitchNotice()
	if p.switchNoticeSnapshot() != nil {
		t.Fatal("clear did not drop the notice")
	}
}
