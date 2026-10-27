package main

import (
	"testing"
	"time"
)

// Keystroke echo must not read as agent work: output landing within
// echoWindowMs of the newest user input is the TUI redrawing its composer, so
// computeState reports idle — otherwise typing in a live terminal raises a
// phantom Stop button / "working" state on every connected client.
func TestComputeState_KeystrokeEchoIsNotBusy(t *testing.T) {
	t.Parallel()
	mk := func(lastOutAgo, lastInAgo time.Duration, hasInput bool) *Pane {
		p := &Pane{ID: "echo", Kind: "", Status: PaneRunning, buffer: NewRingBuffer(1 << 15)}
		_, _ = p.buffer.Write([]byte("plain output, no prompt chrome\n"))
		p.LastActivityMs = time.Now().Add(-lastOutAgo).UnixMilli()
		if hasInput {
			p.LastInputMs = time.Now().Add(-lastInAgo).UnixMilli()
		}
		return p
	}

	// Echo: output 50ms after the last keystroke → idle, not busy.
	echo := mk(0, 50*time.Millisecond, true)
	if got := echo.computeState(); got != StateIdle {
		t.Fatalf("keystroke echo read as %s; want idle (phantom Stop button)", got)
	}
	if echo.agentMidTurnForState(echo.computeState()) {
		t.Fatal("keystroke echo must not read as agentMidTurn")
	}

	// No input at all: fresh output is real work → busy (unchanged behavior).
	noInput := mk(0, 0, false)
	if got := noInput.computeState(); got != StateBusy {
		t.Fatalf("fresh output with no input read as %s; want busy", got)
	}

	// Real turn: the submit keystroke is long past, output still flowing → busy.
	turn := mk(0, 2*time.Second, true)
	if got := turn.computeState(); got != StateBusy {
		t.Fatalf("streaming 2s after submit read as %s; want busy", got)
	}

	// Keystroke DURING real output (input newer than the last output byte):
	// strictly-ordered echo check must not reclassify the earlier output.
	typing := mk(1*time.Second, 0, true)
	if got := typing.computeState(); got != StateBusy {
		t.Fatalf("recent output preceding a keystroke read as %s; want busy", got)
	}
}
