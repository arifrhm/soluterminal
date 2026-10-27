package main

import (
	"encoding/binary"
	"errors"
	"io"
)

// Agent wire protocol — framed messages over a Unix socket connecting the
// daemon (client) to a `solud agent` process (server that holds one PTY).
//
// Each frame:
//   [1 byte: type][4 bytes: payload length, big-endian][N bytes: payload]
//
// The protocol is bidirectional; not all frames are valid in both directions.

// agentProtoVersion identifies the behavior of the long-lived `solud agent`
// process (agent.go, this file, and the PTY relay it runs). The agent reports
// it in FrameAgentInfo; the daemon flags a pane as running an older build ONLY
// when the pane's agent reports a lower value than this daemon's constant.
//
// BUMP THIS when agent.go, agentproto.go, or any code the agent process runs
// for the PTY relay changes behavior in a way an already-running pane would
// need a restart to pick up. Do NOT bump it for daemon-only changes: a bump
// asks the user to restart every open pane.
//
// Agents built before this constant existed do not report it; they are
// treated as agentProtoBaseline (their relay code is identical to v1).
const agentProtoVersion = 1

// agentProtoBaseline is what an agent that reports no protocol version runs.
const agentProtoBaseline = 1

// agentProtoStale reports whether an agent that reported proto (0 = not
// reported) runs an older agent protocol than this daemon ships.
func agentProtoStale(proto int) bool {
	if proto <= 0 {
		proto = agentProtoBaseline
	}
	return proto < agentProtoVersion
}

type FrameType byte

const (
	// FrameData is raw PTY bytes. Server → client: output from the PTY.
	// Client → server: keystrokes to write into the PTY.
	FrameData FrameType = 0x00

	// FrameResize is a client → server control frame with JSON {"cols":N,"rows":N}.
	FrameResize FrameType = 0x01

	// FrameExit is a server → client one-shot with JSON {"code":N}.
	FrameExit FrameType = 0x02

	// FrameReplay is server → client. Agent sends this after a new client
	// connects, containing up to N KB of recent PTY output so the terminal
	// can be redrawn without losing scrollback.
	FrameReplay FrameType = 0x03

	// FrameReplayEnd marks the end of the replay stream.
	FrameReplayEnd FrameType = 0x04

	// FrameKillRequest is client → server. Asks the agent to terminate the
	// child process (SIGTERM, then SIGKILL after 2s).
	FrameKillRequest FrameType = 0x05

	// FrameSyntheticPaint is server → client. Agent emits this between the
	// last FrameReplay chunk and FrameReplayEnd, carrying a self-contained
	// ANSI byte sequence that paints the agent's authoritative screen
	// state. Repairs Codex normal-buffer reattach, where the raw replay
	// is a stream of cursor-positioned diffs against scrollback that has
	// already been evicted from the 8 MiB ringbuf — replaying those bytes
	// into a fresh xterm leaves the visible screen blank or garbled. The
	// agent maintains a continuously-fed grid emulator since spawn-time,
	// so its snapshot is correct regardless of eviction. Sending the paint
	// AFTER raw replay (and before FrameReplayEnd) means: (a) the client
	// xterm gets scrollback from raw replay, then a clear+repaint that
	// fixes the visible region; (b) the daemon's emulators are restored
	// to authoritative state before emitCodexReplayRepair runs on the
	// FrameReplayEnd boundary. Old daemons silently drop unknown frames,
	// so the protocol bump is forward-compatible.
	FrameSyntheticPaint FrameType = 0x06

	// FrameDims is server → client, JSON {"cols":N,"rows":N}, sent as the
	// FIRST frame on every new daemon connection — before any FrameReplay.
	// It reports the PTY's CURRENT dimensions so a freshly-restarted daemon
	// (auto-update) adopts the truth instead of the stale panes.json dims
	// it was rebuilt from. Those persisted dims are only written at
	// spawn/resume-style events and drift arbitrarily far from the live PTY
	// (observed: 283×62 persisted vs 67×29 live); serving the stale value
	// from /api/panes made the reloading client seed its attach resize with
	// it, blowing the real PTY wide and garbling every pane after each
	// update. The agent owns the PTY, so it is the source of truth across
	// daemon restarts. Old daemons ignore unknown frame types, so this is
	// forward-compatible.
	FrameDims FrameType = 0x07

	// FrameAgentInfo is server → client, JSON {"version":"...","pid":N},
	// sent immediately after FrameDims on every daemon connection. It lets a
	// new daemon inventory the detached wrapper that actually owns the PTY.
	// Old daemons ignore unknown frame types, preserving wire compatibility.
	FrameAgentInfo FrameType = 0x08
)

// MaxFramePayload caps a single frame at 1 MiB — enough for any realistic
// terminal output burst, and a firm upper bound for safety.
const MaxFramePayload = 1 << 20

var ErrFrameTooLarge = errors.New("frame payload too large")

// WriteFrame serializes one frame to w.
func WriteFrame(w io.Writer, t FrameType, payload []byte) error {
	if len(payload) > MaxFramePayload {
		return ErrFrameTooLarge
	}
	var header [5]byte
	header[0] = byte(t)
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// ReadFrame reads one frame from r. Returns io.EOF on clean close.
func ReadFrame(r io.Reader) (FrameType, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	t := FrameType(header[0])
	n := binary.BigEndian.Uint32(header[1:])
	if n > MaxFramePayload {
		return 0, nil, ErrFrameTooLarge
	}
	if n == 0 {
		return t, nil, nil
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return t, payload, nil
}

func writeReplayFrames(w io.Writer, snapshot []byte) error {
	return writeReplayWithPaint(w, snapshot, nil)
}

// writeReplayWithPaint emits the on-connect replay sequence: raw historical
// bytes for client-side scrollback, then an optional FrameSyntheticPaint
// that clears + repaints the visible screen with authoritative state, then
// FrameReplayEnd so the daemon's post-replay repair runs after the grid
// emulators have been reset to truth.
func writeReplayWithPaint(w io.Writer, snapshot, paint []byte) error {
	for len(snapshot) > 0 {
		n := MaxFramePayload
		if len(snapshot) < n {
			n = len(snapshot)
		}
		if err := WriteFrame(w, FrameReplay, snapshot[:n]); err != nil {
			return err
		}
		snapshot = snapshot[n:]
	}
	for len(paint) > 0 {
		n := MaxFramePayload
		if len(paint) < n {
			n = len(paint)
		}
		if err := WriteFrame(w, FrameSyntheticPaint, paint[:n]); err != nil {
			return err
		}
		paint = paint[n:]
	}
	return WriteFrame(w, FrameReplayEnd, nil)
}
