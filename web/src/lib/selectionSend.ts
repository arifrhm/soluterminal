// "Send selection to <pane>" must never execute anything in the target pane.
//
// - Control characters are stripped (an ESC could otherwise close a bracketed
//   paste early — "\x1b[201~" — and turn the rest into typed input).
// - Multi-line text is NEVER typed into a pane: it goes to the clipboard.
//   Bracketed-paste mode can't be trusted as a guard — any output in the
//   target pane can switch the terminal's flag on (`printf '\e[?2004h'`), and
//   a foreground program that doesn't speak it (sh, a REPL) would run each line.
// - Single-line text is pasted without a newline: nothing is submitted.
// The caller additionally binds the send to the target pane's process
// (pid) captured when the menu opened, so a pane that restarted in between
// never receives text meant for its previous incarnation.

export type SelectionSendPlan =
  | { action: 'paste'; text: string }
  | { action: 'refuse'; reason: 'empty' | 'multiline-unsafe'; text: string };

export function sanitizeSelection(text: string): string {
  // Normalise newlines, then drop C0/C1 controls other than tab and newline.
  return text
    .replace(/\r\n?/g, '\n')
    // eslint-disable-next-line no-control-regex
    .replace(/[\u0000-\u0008\u000b-\u001f\u007f-\u009f]/g, '')
    .replace(/\n+$/, '');
}

export function planSelectionSend(raw: string): SelectionSendPlan {
  const text = sanitizeSelection(raw);
  if (!text.trim()) return { action: 'refuse', reason: 'empty', text };
  if (text.includes('\n')) return { action: 'refuse', reason: 'multiline-unsafe', text };
  return { action: 'paste', text };
}
