// Answers xterm.js writes to onData when it parses a terminal *query*: a
// program asked "where is the cursor?", "which mode is set?", "what colour
// is the background?" and xterm replies as if typed. During a reattach replay
// nothing live is asking (the program that asked is long gone, or redrew
// since), so a replayed query's answer lands at whatever is in front now,
// usually a shell, as literal text: `12;2$y` after a vim session, for one.
// oscStrip.ts already strips replayed OSC colour queries; this catches the
// answers to every other kind while a replay is being parsed.
//
// Only answer shapes match. Typing, arrows, most function keys (`ESC [ A`,
// `ESC [ 15 ~`), paste, focus and mouse reports (`ESC [ < … M`) never do.
// A modified F1-F4 (Shift+F3 is `ESC [ 1 ; 2 R`) has the same shape as a
// cursor report, so one pressed during the few milliseconds a history replay
// parses is dropped; the filter is only on while that replay parses.
const CSI_ANSWER = new RegExp(
  '^\\x1b\\[(?:' +
    '\\??[\\d;]*R' + //        cursor position report (CPR / DECXCPR)
    '|[?>=][\\d;]*c' + //      device attributes (DA1 / DA2 / DA3)
    '|\\??[\\d;]*n' + //       device status report
    '|\\??[\\d;]*\\$y' + //    mode report (DECRPM)
    '|[\\d;]+t' + //           window / text-area size reports
    ')$',
);
// OSC answers (colours, palette, clipboard) and DCS answers (DECRQSS,
// XTGETTCAP, XTVERSION), each ending in BEL or ST.
const STRING_ANSWER = /^(?:\x1b\][\d;]*[^\x07\x1b]*(?:\x07|\x1b\\)|\x1bP[\s\S]*\x1b\\)$/;

/** Whether data is a terminal's automatic answer to a query, not typing. */
export function isTerminalAnswer(data: string): boolean {
  return CSI_ANSWER.test(data) || STRING_ANSWER.test(data);
}

/**
 * Whether answers are being dropped: on while a history replay parses, never
 * for an exact resume (its output is new to this surface, and a program still
 * waiting on a query it sent during the gap must get the answer).
 */
export class ReplayAnswerGate {
  private parsing = 0;

  /** A replay starts parsing; returns its end(). */
  begin(isResume: boolean): () => void {
    if (isResume) return () => {};
    this.parsing++;
    let ended = false;
    return () => {
      if (ended) return;
      ended = true;
      this.parsing = Math.max(0, this.parsing - 1);
    };
  }

  /** Teardown: a replay cut short never reaches its end(). */
  reset(): void {
    this.parsing = 0;
  }

  /** Whether data from onData should be dropped now. */
  drops(data: string): boolean {
    return this.parsing > 0 && isTerminalAnswer(data);
  }
}
