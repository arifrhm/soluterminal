package main

import (
	"sync/atomic"
	"testing"
)

func TestIsTerminalReplyOnly(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want bool
	}{
		// Autonomous terminal traffic — must NOT count as user input.
		{"focus in", "\x1b[I", true},
		{"focus out", "\x1b[O", true},
		{"focus in+out burst", "\x1b[I\x1b[O", true},
		{"cursor position report", "\x1b[24;80R", true},
		{"extended cursor report (DECXCPR)", "\x1b[?24;80;1R", true},
		{"DA1 reply", "\x1b[?1;2c", true},
		{"DA2 reply", "\x1b[>0;276;0c", true},
		{"DSR status ok", "\x1b[0n", true},
		{"DECRPM mode report", "\x1b[?2026;2$y", true},
		{"kitty flags reply", "\x1b[?1u", true},
		{"SGR mouse wheel", "\x1b[<65;40;12M", true},
		{"SGR mouse release", "\x1b[<0;40;12m", true},
		{"legacy X10 mouse", "\x1b[M \x21\x22", true},
		{"OSC color reply BEL", "\x1b]11;rgb:1e1e/2e2e/3e3e\x07", true},
		{"OSC color reply ST", "\x1b]10;rgb:ffff/ffff/ffff\x1b\\", true},
		{"OSC reply split mid-frame", "\x1b]52;c;aGVsbG8", true},
		{"DCS DECRQSS reply", "\x1bP1$r0m\x1b\\", true},
		{"empty bracketed paste", "\x1b[200~\x1b[201~", true},
		{"window size report", "\x1b[8;24;80t", true},
		{"reply burst", "\x1b[I\x1b[24;80R\x1b]11;rgb:00/00/00\x07", true},
		{"empty payload", "", true},

		// Real input — must count.
		{"plain text", "hello", false},
		{"enter", "\r", false},
		{"menu digit", "1", false},
		{"escape key", "\x1b", false},
		{"alt+x", "\x1bx", false},
		{"arrow up", "\x1b[A", false},
		{"app-mode arrow", "\x1bOA", false},
		{"shift-tab", "\x1b[Z", false},
		{"page up", "\x1b[5~", false},
		{"delete", "\x1b[3~", false},
		{"F5", "\x1b[15~", false},
		{"kitty key event (no ?)", "\x1b[97u", false},
		{"paste with content", "\x1b[200~hello\x1b[201~", false},
		{"chatter then typing", "\x1b[Ihi", false},
		{"typing then chatter", "y\x1b[I", false},
		{"truncated CSI", "\x1b[24;8", false},
		{"bare DA query form", "\x1b[c", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTerminalReplyOnly([]byte(tc.in)); got != tc.want {
				t.Errorf("isTerminalReplyOnly(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// A focus report must not shift the answered-prompt baseline: before the
// filter, ANY client→PTY byte advanced LastInputStrippedPos, so a glance at
// the workspace re-armed awaiting-input (infinite "Claude is waiting for
// you") and marked real approval menus as answered.
func TestWriteSkipsInputMarkersForTerminalReplies(t *testing.T) {
	t.Parallel()
	p := connectedPane(t)
	atomic.StoreInt64(&p.TotalStrippedChars, 5000)

	if _, err := p.Write([]byte("\x1b[I\x1b[24;80R")); err != nil {
		t.Fatalf("Write(chatter): %v", err)
	}
	if got := atomic.LoadInt64(&p.LastInputStrippedPos); got != 0 {
		t.Fatalf("LastInputStrippedPos = %d after pure chatter, want 0 (unchanged)", got)
	}
	if got := atomic.LoadInt64(&p.LastInputMs); got != 0 {
		t.Fatalf("LastInputMs = %d after pure chatter, want 0 (unchanged)", got)
	}

	if _, err := p.Write([]byte("y\r")); err != nil {
		t.Fatalf("Write(typing): %v", err)
	}
	if got := atomic.LoadInt64(&p.LastInputStrippedPos); got != 5000 {
		t.Fatalf("LastInputStrippedPos = %d after real typing, want 5000", got)
	}
	if got := atomic.LoadInt64(&p.LastInputMs); got == 0 {
		t.Fatal("LastInputMs not stamped after real typing")
	}
}
