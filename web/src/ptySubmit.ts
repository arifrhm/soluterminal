// PTY submit timing — see feedback_pty_submit_split.md.
//
// When we feed a user-authored line into a PTY-hosted TUI, we MUST
// split the body and the trailing \r across two writes. Bubble Tea
// (Claude Code) and other paste-burst detectors will swallow a \r
// that arrives in the same read() as the body bytes. The fix is:
// write body, wait `enterGapMs`, write \r as its own frame.
//
// Numbers were proven on the voice-auto-send path:
//   - Claude / plain-text PTY input: 400ms — Bubble Tea's paste
//     window is ~50ms but WS+PTY buffering adds headroom, and
//     shorter values regressed in field testing.
//   - Bracketed-paste path (Codex/other TUIs/shells wrapping body
//     in \x1b[200~ ... \x1b[201~): 100ms — paste mode itself
//     resets burst state, so a small margin is enough.
//
// The Go counterpart (daemon/pty_submit.go) MUST stay in sync —
// same numbers, same semantics. If you change one, change the other.

export const ENTER_GAP_CLAUDE_MS = 400;
export const ENTER_GAP_BRACKETED_PASTE_MS = 100;

// Runtime-tunable overrides. The daemon serves the resolved gaps (honoring
// the SOLU_ENTER_GAP_* env vars) at GET /api/enter-gap; api.ts fetches them
// once at startup and calls setEnterGapConfig below. Until that returns —
// and whenever the daemon doesn't carry the value — these stay at the
// compiled-in defaults, so any submit before config-load uses the proven
// 400/100 and can never break. This lets Benjamin tune the 400ms with data
// without a rebuild. Keep in sync with daemon/pty_submit.go.
let enterGapClaudeMs = ENTER_GAP_CLAUDE_MS;
let enterGapBracketedPasteMs = ENTER_GAP_BRACKETED_PASTE_MS;

// setEnterGapConfig overrides the runtime gaps from the daemon's
// /api/enter-gap response. Each value is applied only when it's a finite,
// non-negative number; anything else (undefined, NaN, negative) leaves the
// safe default in place. Idempotent — safe to call again on reconnect.
export function setEnterGapConfig(cfg: { claudeMs?: number; defaultMs?: number }): void {
  if (typeof cfg.claudeMs === 'number' && Number.isFinite(cfg.claudeMs) && cfg.claudeMs >= 0) {
    enterGapClaudeMs = cfg.claudeMs;
  }
  if (typeof cfg.defaultMs === 'number' && Number.isFinite(cfg.defaultMs) && cfg.defaultMs >= 0) {
    enterGapBracketedPasteMs = cfg.defaultMs;
  }
}

// enterGapMs returns the gap (in ms) between writing the body and
// the trailing \r. Pass `useBracketedPaste = true` when the body is
// being sent inside \x1b[200~ ... \x1b[201~ (Codex / shells / other
// TUIs that opt into bracketed paste). Claude Code uses raw input,
// so its callers pass `false`. Returns the runtime-configured value
// (set via setEnterGapConfig) or the compiled-in 400/100 default.
export function enterGapMs(useBracketedPaste: boolean): number {
  return useBracketedPaste ? enterGapBracketedPasteMs : enterGapClaudeMs;
}

export type PaneSender = (bytes: string) => boolean;

// submitToPane writes a user-authored line to a pane via its raw sender,
// applying the body/Enter split this module exists to enforce: write the
// body, wait enterGapMs, then write the trailing \r as its own frame so a
// paste-burst detector (Bubble Tea / Claude Code) doesn't swallow it.
//
// This is the single front door every non-keyboard submit path should use
// (voice auto-send, broadcast, mobile quick-reply) so the timing can never
// drift between them. Defaults to the Claude/raw 400ms gap, which is the
// safe universal choice — harmless for shells, correct for Bubble Tea.
//
// Returns whether the BODY write succeeded; the \r is best-effort (if the
// pane closed in the gap the body is already lost too, and the caller's
// recovery handles it).
export function submitToPane(
  sender: PaneSender,
  body: string,
  opts?: { appendEnter?: boolean; useBracketedPaste?: boolean },
): boolean {
  const appendEnter = opts?.appendEnter ?? true;
  const ok = sender(body);
  if (ok && appendEnter) {
    const gap = enterGapMs(opts?.useBracketedPaste ?? false);
    setTimeout(() => {
      sender('\r');
    }, gap);
  }
  return ok;
}
