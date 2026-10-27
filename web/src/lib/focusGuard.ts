// focusGuard.ts — guard against activity-driven focus theft.
//
// Background panes can reassign the SELECTED pane (store.focusedPaneId) in
// response to async activity the user never asked for: an MCP preview_file /
// preview_url opening a tile, or ARIA auto-mode spawning a pane. When that
// happens while the user is mid-sentence in ANOTHER pane's composer, the newly
// selected pane's focus effect would call .focus() and yank the keyboard caret
// away — the next keystrokes land in the wrong pane.
//
// isTypingElsewhere(target) is true when the user currently has an editable
// element focused that is NOT `target` — i.e. moving DOM focus right now would
// interrupt active typing. The pane focus effects are safe-by-default: they
// steal the caret ONLY when store.lastUserFocusId === this pane (a DELIBERATE
// selection) OR the user isn't typing elsewhere. Deliberate focus changes —
// keyboard nav (Alt+H/J/K/L, Alt+1..9, Ctrl+Shift+Arrow), clicks, command
// palette, reveal, explicit spawn — all stamp lastUserFocusId (via setFocus or
// the spawn paths), so they are NEVER blocked and pane-to-pane navigation keeps
// working. The stamp is ONE-SHOT: the focus effect consumes it (consumeUserFocus)
// right after grabbing the caret, so a later re-render/remount — notably the
// per-pane WebSocket reconnect storms since v0.0.713 — does NOT re-assert focus
// and yank the caret back. Any background focusedPaneId change (MCP open, ARIA
// spawn, diff/file/browser tile, workspace switch) leaves lastUserFocusId
// untouched, so it cannot yank the caret out of a composer the user is typing in.

// A layout reflow — an ARIA/MCP graft re-nesting the layout tree, or a warm-pane
// host being re-parented through the hidden parking lot — can BLUR the composer
// the user is typing in down to <body> in the SAME commit that a background pane
// becomes selected. At that instant document.activeElement is <body>, so a naive
// activeElement check reports "not typing" and the background pane's focus effect
// steals the caret. To survive that window we remember the last editable the user
// actually typed into and treat a just-blurred-to-body state as "still typing".
let lastTypedEl: Element | null = null;
let lastTypedAt = 0;
const RECENT_TYPING_MS = 700;
function isEditable(el: Element | null): boolean {
  if (!el) return false;
  const tag = el.tagName;
  return tag === 'TEXTAREA' || tag === 'INPUT' || (el as HTMLElement).isContentEditable === true;
}
if (typeof window !== 'undefined') {
  // 'input' = real keystrokes (the strongest "user is typing here" signal).
  window.addEventListener('input', (e) => {
    const t = e.target as Element | null;
    if (isEditable(t)) { lastTypedEl = t; lastTypedAt = Date.now(); }
  }, true);
}

export function isTypingElsewhere(target: Element | null): boolean {
  if (typeof document === 'undefined') return false;
  const ae = document.activeElement;
  if (ae && ae !== target && isEditable(ae)) return true;
  // The caret was just blurred to <body>/null by a same-commit reflow: if the
  // user typed into a DIFFERENT editable within the last RECENT_TYPING_MS, a
  // background focus grab must NOT treat that transient blur as "stopped typing".
  if (lastTypedEl && lastTypedEl !== target && lastTypedEl.isConnected
      && Date.now() - lastTypedAt < RECENT_TYPING_MS) {
    return true;
  }
  return false;
}
