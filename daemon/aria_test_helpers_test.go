package main

import (
	"sync/atomic"
	"testing"
	"time"
)

func testPaneWithTail(t *testing.T, id, title, tail string) *Pane {
	t.Helper()
	p := &Pane{
		ID:             id,
		Title:          title,
		Cmd:            []string{"bash"},
		Status:         PaneRunning,
		CreatedAt:      time.Now(),
		LastActivityMs: time.Now().Add(-5 * time.Second).UnixMilli(),
		Cols:           80,
		Rows:           24,
		buffer:         NewRingBuffer(1 << 20),
		subs:           map[int64]*subEntry{},
	}
	if _, err := p.buffer.Write([]byte(tail)); err != nil {
		t.Fatalf("buffer.Write: %v", err)
	}
	atomic.StoreInt64(&p.TotalStrippedChars, int64(len(stripANSI([]byte(tail)))))
	return p
}

func testManagerWithPane(p *Pane) *Manager {
	m := NewManager("")
	m.panes[p.ID] = p
	return m
}

func testAriaWithPane(t *testing.T, p *Pane) *Aria {
	t.Helper()
	return &Aria{
		mgr:      testManagerWithPane(p),
		uiBus:    newUIBus(),
		confirms: map[string]*ariaPendingConfirm{},
	}
}

// waitNeedsConfirm drains events until the needs_confirm for `tool`
// arrives (or fails the test after 1s) and returns its confirm id.
func waitNeedsConfirm(t *testing.T, events <-chan map[string]any, tool string) string {
	t.Helper()
	var evt map[string]any
	select {
	case evt = <-events:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for needs_confirm for %s", tool)
	}
	if evt["type"] != "needs_confirm" || evt["tool"] != tool {
		t.Fatalf("event = %#v, want needs_confirm for %s", evt, tool)
	}
	id, _ := evt["id"].(string)
	if id == "" {
		t.Fatalf("confirm event missing id: %#v", evt)
	}
	return id
}
