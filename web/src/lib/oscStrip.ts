// Strips replay-unsafe OSC sequences from a byte stream:
//
//   - OSC 4 / 10 / 11 / 12 *queries* (the `?` form). xterm.js auto-replies
//     to these by writing the response to its onData handler — which we
//     forward to the PTY. Correct for live queries (a TUI asks → answer
//     comes back). But on WebSocket reconnect the daemon replays the
//     ring buffer to xterm; any historical query in that buffer fires
//     the auto-reply again, except the asking TUI is long gone, so the
//     reply lands at whatever's currently foreground (a shell prompt,
//     usually) and gets echoed as literal text. Stripping the queries
//     before they reach xterm closes that loop. The SET form (e.g.
//     `ESC ] 11 ; rgb:0000/0000/0000 ESC \`) is left intact — those
//     affect rendering and the user expects the replay to restore
//     visual state.
//
//   - OSC 52 (all forms). The handler in Pane.tsx routes OSC 52 set
//     payloads to navigator.clipboard.writeText, which is how Claude
//     Code's `/copy` (and vim's `+y`, tmux save-buffer, etc.) reach
//     the system clipboard. On replay there's no live "copy intent"
//     — re-feeding historical OSC 52 bytes overwrites whatever the
//     user has on their clipboard *right now* with whatever was
//     copied earlier in the session, every time they switch into the
//     pane. Notably wrecks mobile: switching into a pane on the
//     phone replays the ring buffer, so the phone's clipboard gets
//     clobbered with stale terminal-side payloads. OSC 52 has no
//     rendering side-effect, so dropping it from replay is lossless.
//
// State-machine, not regex: OSC sequences can split across the chunk
// boundaries that the daemon hands us, and a regex pass over the
// joined buffer would still work but allocates 2× memory for an 8MB
// replay. The state machine processes in place, single pass.

const ESC = 0x1b;
const RBRACKET = 0x5d;
const BACKSLASH = 0x5c;
const BEL = 0x07;
const SEMI = 0x3b;
const QUEST = 0x3f;
const ZERO = 0x30;
const NINE = 0x39;

type Mode = 'normal' | 'esc' | 'osc' | 'osc-esc';

export interface OscStripState {
  mode: Mode;
  // Buffered OSC body bytes (between `ESC ]` and the terminator). Only
  // populated while mode === 'osc'. Capped to keep a malformed
  // unterminated sequence from growing without bound; on overflow we
  // bail out and emit the buffered bytes literally so we don't drop
  // unrelated downstream content.
  oscBuf: number[];
}

export function newOscStripState(): OscStripState {
  return { mode: 'normal', oscBuf: [] };
}

const OSC_BUF_LIMIT = 4096;

// Returns true iff the OSC body should be dropped from a replay stream.
// Two cases:
//   1) Color query (`4|10|11|12; … ? …`) — see file header.
//   2) Any OSC 52 (`52; …`) — clipboard set, see file header.
function bodyIsReplayUnsafe(body: number[]): boolean {
  let n = 0;
  let i = 0;
  let any = false;
  while (i < body.length && body[i] >= ZERO && body[i] <= NINE) {
    n = n * 10 + (body[i] - ZERO);
    any = true;
    i++;
  }
  if (!any) return false;
  if (i >= body.length || body[i] !== SEMI) return false;
  if (n === 52) return true;
  if (n !== 4 && n !== 10 && n !== 11 && n !== 12) return false;
  for (let k = i + 1; k < body.length; k++) {
    if (body[k] === QUEST) return true;
  }
  return false;
}

// Strips OSC color queries from `input`, threading state across calls.
// Returns the cleaned slice. Caller passes the SAME `state` object
// across successive calls to handle sequences split mid-flight.
export function stripOscColorQueries(input: Uint8Array, state: OscStripState): Uint8Array {
  // Output can exceed input.length when state carries an in-flight
  // sequence in from a PREVIOUS call: a buffered OSC body re-emitted
  // literally (ESC ] + oscBuf + terminator) or a swallowed ESC re-emitted
  // (ESC + byte). Those carried bytes were counted against the previous
  // call's input.length, so this call must reserve headroom for them —
  // otherwise the tail of the cleaned chunk silently falls off the end
  // of a too-small Uint8Array (JS ignores out-of-bounds writes), punching
  // a hole in the replay. Harmless when the daemon shipped the replay as
  // one frame (state always started clean); the daemon now chunks replay
  // at 128 KiB, so cross-call carry is routine. +8 covers ESC ] framing
  // (2), an osc-esc carried ESC (1), the terminator (≤2), and slack.
  const out = new Uint8Array(input.length + state.oscBuf.length + 8);
  let oi = 0;

  const flushOscBufLiteral = () => {
    // Re-emit the buffered partial OSC bytes (ESC ] + accumulated
    // body) literally — used when we hit the buffer cap or when the
    // sequence turns out not to be a color query.
    out[oi++] = ESC;
    out[oi++] = RBRACKET;
    for (const b of state.oscBuf) out[oi++] = b;
    state.oscBuf.length = 0;
  };

  for (let i = 0; i < input.length; i++) {
    const b = input[i];

    if (state.mode === 'normal') {
      if (b === ESC) {
        state.mode = 'esc';
      } else {
        out[oi++] = b;
      }
      continue;
    }

    if (state.mode === 'esc') {
      if (b === RBRACKET) {
        state.mode = 'osc';
        state.oscBuf.length = 0;
      } else {
        // Not an OSC introducer — emit the swallowed ESC and the
        // current byte verbatim. Caller relies on ESC + final-byte
        // CSI / SS3 / charset designations passing through.
        out[oi++] = ESC;
        out[oi++] = b;
        state.mode = 'normal';
      }
      continue;
    }

    if (state.mode === 'osc') {
      if (b === BEL) {
        if (bodyIsReplayUnsafe(state.oscBuf)) {
          // drop
        } else {
          out[oi++] = ESC;
          out[oi++] = RBRACKET;
          for (const x of state.oscBuf) out[oi++] = x;
          out[oi++] = BEL;
        }
        state.oscBuf.length = 0;
        state.mode = 'normal';
        continue;
      }
      if (b === ESC) {
        // Could be the start of ST (ESC \) — defer the decision
        // until we see the next byte (possibly in the next chunk).
        state.mode = 'osc-esc';
        continue;
      }
      state.oscBuf.push(b);
      if (state.oscBuf.length > OSC_BUF_LIMIT) {
        flushOscBufLiteral();
        state.mode = 'normal';
      }
      continue;
    }

    // mode === 'osc-esc' — saw ESC inside an OSC body. If next byte
    // is `\` it's ST (terminator); otherwise the ESC was a data byte
    // and we re-process the current byte under 'osc' mode.
    if (b === BACKSLASH) {
      if (bodyIsReplayUnsafe(state.oscBuf)) {
        // drop
      } else {
        out[oi++] = ESC;
        out[oi++] = RBRACKET;
        for (const x of state.oscBuf) out[oi++] = x;
        out[oi++] = ESC;
        out[oi++] = BACKSLASH;
      }
      state.oscBuf.length = 0;
      state.mode = 'normal';
      continue;
    }
    // ESC was a stray data byte inside the OSC body — push it,
    // then re-handle current byte as if we were still in 'osc'.
    state.oscBuf.push(ESC);
    if (state.oscBuf.length > OSC_BUF_LIMIT) {
      flushOscBufLiteral();
      state.mode = 'normal';
      // Re-emit the current byte verbatim since we've bailed.
      out[oi++] = b;
      continue;
    }
    state.mode = 'osc';
    i--; // re-process this byte under the new mode
  }

  return out.subarray(0, oi);
}
