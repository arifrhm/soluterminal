package grid

import (
	"bytes"
	"testing"
	"time"
)

// TestColorQueryProducesReply pins the underlying vt library's
// OSC-color auto-reply contract. As of charmbracelet/x/vt
// v0.0.0-20260422, OSC 10 / 11 / 12 ? queries each produce a reply
// (foreground, background, cursor color); OSC 4 ; N ; ? palette
// queries are silently consumed.
//
// This is the contract that makes the "drain seed-feed replies" fix
// in pane_grid.ensureGrid necessary AND sufficient: necessary
// because any OSC 10/11/12 ? in the historical ring buffer would
// otherwise inject "\x1b]NN;rgb:.../.../...\x1b\\" into PTY input,
// sufficient because palette queries (the other common shape) get
// no answer from this layer.
//
// Caveat: a single OSC reply emitted right at the end of Feed can
// still be "in flight" between vt's io.Pipe and our e.out buffer
// when DrainReplies() returns; that's why pane_grid sleeps briefly
// before draining. This test mirrors that sleep so the assertion
// reflects what production code observes.
func TestColorQueryProducesReply(t *testing.T) {
	cases := []struct {
		name      string
		query     []byte
		wantReply bool
	}{
		{"OSC 10 foreground", []byte("\x1b]10;?\x1b\\"), true},
		{"OSC 11 background", []byte("\x1b]11;?\x1b\\"), true},
		{"OSC 12 cursor", []byte("\x1b]12;?\x1b\\"), true},
		{"OSC 4 palette (vt silent)", []byte("\x1b]4;1;?\x1b\\"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			em := New(80, 24)
			em.Feed(tc.query)
			// Match the settling delay used in pane_grid.ensureGrid;
			// this test is asserting the contract the production
			// caller observes, not vt's microsecond-level timing.
			time.Sleep(30 * time.Millisecond)
			reply := em.DrainReplies()
			if tc.wantReply {
				if len(reply) == 0 {
					t.Fatalf("expected non-empty reply for %q, got none", tc.query)
				}
				if !bytes.HasPrefix(reply, []byte{0x1b, ']'}) {
					t.Errorf("reply does not start with ESC ]: %q", reply)
				}
				if !bytes.Contains(reply, []byte("rgb:")) {
					t.Errorf("reply does not contain rgb:: %q", reply)
				}
			} else {
				if len(reply) != 0 {
					t.Errorf("expected NO reply for %q, got %q (vt now answers this — strip in pane_grid still works but update the comment)", tc.query, reply)
				}
			}
		})
	}
}

// TestSeedFeedDrainAfterShortSleep mirrors what pane_grid.ensureGrid
// actually does: feed a realistic ring-buffer payload that contains
// historical OSC 11 queries, sleep briefly to let vt's reply pipe
// drain into e.out, then DrainReplies() and discard. The test fails
// if the drain misses any reply — meaning a reply would otherwise
// reach the pump and get written to PTY input as visible gibberish.
//
// The sleep here MUST match (or exceed) the one in ensureGrid; if
// ensureGrid's sleep ever drops below the value here, this test
// catches it before the gibberish bug returns.
func TestSeedFeedDrainAfterShortSleep(t *testing.T) {
	const ensureGridSettleDelay = 30 * time.Millisecond

	payload := bytes.Join([][]byte{
		[]byte("\x1b]11;?\x1b\\"),
		[]byte("hello\r\n"),
		[]byte("\x1b]10;?\x1b\\"),
		[]byte("\x1b]11;?\x1b\\"),
		[]byte("more output\r\n"),
		[]byte("\x1b]11;?\x1b\\"),
	}, nil)

	em := New(80, 24)
	em.Feed(payload)
	time.Sleep(ensureGridSettleDelay)
	reply := em.DrainReplies()

	got := bytes.Count(reply, []byte("rgb:"))
	if got < 3 {
		t.Fatalf("expected ≥3 rgb: replies after sleep+drain (one per OSC 11 ?), got %d.\nreply=%q", got, reply)
	}

	// A subsequent drain after another short wait must be empty:
	// proves the first drain pulled everything in flight.
	time.Sleep(5 * time.Millisecond)
	tail := em.DrainReplies()
	if len(tail) != 0 {
		t.Errorf("second drain non-empty (settle delay too short?): %q", tail)
	}
}
