package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A Claude permission menu as painted in a daemon PTY: no DEC 2026 frame
// brackets, and the footer plus status rows push the "1." option more than
// 512 raw bytes from the end of the payload, so the raw-output hint misses.
func claudeMenuWithLongFooter() []byte {
	var b strings.Builder
	b.WriteString("\x1b[2K Bash command\r\n   ls -la /tmp\r\n\r\n Do you want to proceed?\r\n ❯ 1. Yes\r\n   2. No\r\n\r\n Esc to cancel · Tab to amend\r\n")
	for range 12 {
		b.WriteString("\x1b[38;5;244m  ~/wsl-projects/shinoque [Opus 5.5 | ctx: 27% | main] background agent working\x1b[39m\r\n")
	}
	return []byte(b.String())
}

func startExpectWakeHarness(t *testing.T) (*Pane, *Manager, net.Conn) {
	t.Helper()
	client, server := net.Pipe()
	p := testPaneWithTail(t, "pane-expect-wake", "Expect wake", "")
	p.agentConn = client
	p.done = make(chan struct{})
	p.AutoMode = "always"
	p.ensureGrid()
	m := NewManager("")
	m.panes[p.ID] = p
	go p.readLoop(m)
	t.Cleanup(func() { server.Close(); <-p.done })
	return p, m, server
}

func waitAutoPromptWake(m *Manager, within time.Duration) bool {
	select {
	case <-m.autoPromptWake:
		return true
	case <-time.After(within):
		return false
	}
}

func TestClaudeMenuWithoutFrameMarkersMissesRawHint(t *testing.T) {
	payload := claudeMenuWithLongFooter()
	if autoPromptRawOutputHint(payload[len(payload)-512:]) {
		t.Fatal("fixture must defeat the 512-byte raw hint")
	}
	_, m, server := startExpectWakeHarness(t)
	if err := WriteFrame(server, FrameData, payload); err != nil {
		t.Fatal(err)
	}
	if waitAutoPromptWake(m, 400*time.Millisecond) {
		t.Fatal("unexpected wake without an expected-prompt window")
	}
}

// PreToolUse (or a just-landed auto-approval) opens the window, and the menu
// that follows wakes the watcher after the short settle delay instead of
// waiting for the 2s activity tick or the 5s sweeper.
func TestExpectedPromptWindowWakesOnClaudeMenu(t *testing.T) {
	p, m, server := startExpectWakeHarness(t)
	p.expectAutoPrompt()
	start := time.Now()
	if err := WriteFrame(server, FrameData, claudeMenuWithLongFooter()); err != nil {
		t.Fatal(err)
	}
	if !waitAutoPromptWake(m, time.Second) {
		t.Fatal("expected-prompt window did not wake the watcher")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("wake took %v", elapsed)
	}
}

// Back-to-back queued menus inside the window skip the 500ms raw throttle.
func TestExpectedPromptWindowSkipsRawThrottle(t *testing.T) {
	p, m, server := startExpectWakeHarness(t)
	p.expectAutoPrompt()
	if err := WriteFrame(server, FrameData, claudeMenuWithLongFooter()); err != nil {
		t.Fatal(err)
	}
	if !waitAutoPromptWake(m, time.Second) {
		t.Fatal("first menu did not wake")
	}
	p.MarkAutoModeFired()
	start := time.Now()
	if err := WriteFrame(server, FrameData, claudeMenuWithLongFooter()); err != nil {
		t.Fatal(err)
	}
	if !waitAutoPromptWake(m, time.Second) {
		t.Fatal("queued menu did not wake")
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Fatalf("queued menu wake took %v; raw throttle still applied", elapsed)
	}
}

func TestPreToolUseHookOpensExpectedPromptWindow(t *testing.T) {
	p := testPaneWithTail(t, "pane-hook-expect", "Hook expect", "")
	p.Status = PaneRunning
	p.AutoMode = "always"
	off := testPaneWithTail(t, "pane-hook-off", "Hook off", "")
	off.Status = PaneRunning
	m := NewManager("")
	m.panes[p.ID] = p
	m.panes[off.ID] = off
	srv := &apiServer{mgr: m, uiBus: newUIBus()}
	for _, id := range []string{p.ID, off.ID} {
		hookRequest(t, srv, id, `{"hook_event_name":"PreToolUse","tool_name":"Bash","session_id":"s1"}`)
	}
	if !p.autoPromptExpected() {
		t.Fatal("PreToolUse on an auto-mode pane did not open the window")
	}
	if off.autoPromptExpected() {
		t.Fatal("PreToolUse opened the window on a pane without auto-mode")
	}
}

func hookRequest(t *testing.T, srv *apiServer, paneID, body string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/tool-call", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Soluterminal-Pane", paneID)
	rec := httptest.NewRecorder()
	srv.hookToolCall(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("hook status = %d", rec.Code)
	}
}
