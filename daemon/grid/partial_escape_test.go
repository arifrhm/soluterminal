package grid

import (
	"strings"
	"testing"
)

// TestFeedHandlesPartialEscapeAcrossCalls is a contract test against
// charmbracelet/x/vt: a CSI sequence split across two Feed() calls
// must still be interpreted as one cursor-move, not rendered as two
// chunks of literal text.
//
// Background: 2026-05-02, a debug-grid probe of pane_4aa85895… briefly
// showed gridText containing the literal characters "[20;62H" — the
// ANSI cursor-position escape `\x1b[20;62H` minus its leading 0x1B.
// Investigation cleared both the daemon's frame protocol (binary-safe,
// length-prefixed) and the vt parser (state-machine buffers ESC across
// byte boundaries), so we couldn't pin a root cause. This test pins
// the parser-side half of the contract: if it ever regresses (e.g. a
// future vt update changes byte-by-byte buffering semantics), we
// catch it immediately instead of via mysterious cell-content
// artifacts.
func TestFeedHandlesPartialEscapeAcrossCalls(t *testing.T) {
	t.Parallel()
	type chunkPlan struct {
		name   string
		chunks [][]byte
	}
	full := []byte("\x1b[20;62HX") // move cursor to (20,62), then print 'X'

	plans := []chunkPlan{
		{
			name:   "single chunk (control)",
			chunks: [][]byte{full},
		},
		{
			name:   "split between ESC and CSI body",
			chunks: [][]byte{{0x1b}, []byte("[20;62HX")},
		},
		{
			name:   "split inside CSI parameters",
			chunks: [][]byte{[]byte("\x1b[20;"), []byte("62HX")},
		},
		{
			name:   "split right before final byte",
			chunks: [][]byte{[]byte("\x1b[20;62"), []byte("HX")},
		},
		{
			name:   "byte-by-byte feed",
			chunks: byteByByte(full),
		},
	}

	for _, plan := range plans {
		t.Run(plan.name, func(t *testing.T) {
			em := New(80, 24)
			for _, c := range plan.chunks {
				em.Feed(c)
			}
			snap := em.Snapshot()

			// 'X' must land at row 20, col 62 (1-based in escape →
			// 0-based as row=19, col=61 in the cell grid).
			if got := cellChar(snap, 61, 19); got != "X" {
				t.Errorf("X not at (61,19): got %q", got)
			}

			// And no literal "[20;62H" must appear anywhere in
			// reconstructed cell content. If the parser had dropped
			// the partial ESC and rendered the rest as text, we'd see
			// it on row 0 (initial cursor position).
			var gridText strings.Builder
			for y := 0; y < snap.Rows; y++ {
				gridText.WriteString(rowAt(snap, y))
				gridText.WriteByte('\n')
			}
			if strings.Contains(gridText.String(), "[20;62H") {
				t.Errorf("found literal CSI text [20;62H in cell content; "+
					"parser dropped a partial escape. gridText:\n%s",
					gridText.String())
			}
		})
	}
}

func byteByByte(b []byte) [][]byte {
	out := make([][]byte, len(b))
	for i, x := range b {
		out[i] = []byte{x}
	}
	return out
}
