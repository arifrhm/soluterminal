import { describe, expect, it } from 'vitest';
import { isTerminalAnswer, ReplayAnswerGate } from './terminalReplies';

describe('isTerminalAnswer', () => {
  it('matches the answers a replayed query provokes', () => {
    for (const answer of [
      '\x1b[?12;2$y', //           DECRPM, after vim's `CSI ? 12 $ p`
      '\x1b[12;2$y',
      '\x1b[24;80R', //            CPR
      '\x1b[?24;80;1R', //         DECXCPR
      '\x1b[?1;2c', //             DA1
      '\x1b[>0;276;0c', //         DA2
      '\x1b[0n', //                DSR
      '\x1b[8;24;80t', //          text-area size report
      '\x1b]11;rgb:1f1f/1f1f/1f1f\x1b\\', // OSC 11 colour answer
      '\x1b]10;rgb:ffff/ffff/ffff\x07',
      '\x1bP1$r0m\x1b\\', //       DECRQSS
      '\x1bP>|xterm.js(5.5)\x1b\\', // XTVERSION
    ]) {
      expect(isTerminalAnswer(answer), JSON.stringify(answer)).toBe(true);
    }
  });

  it('never matches typing, keys or mouse reports', () => {
    for (const input of [
      'a', 'ls -la\r', '12;2$y', '\r', '\x7f', '\x03',
      '\x1b[A', '\x1b[1;5C', //    arrows, ctrl-arrow
      '\x1b[15~', '\x1b[3~', //    F5, Delete
      '\x1bOP', //                 F1
      '\x1b[I', '\x1b[O', //       focus reports
      '\x1b[<0;10;5M', '\x1b[<0;10;5m', // SGR mouse
      '\x1b[200~pasted\x1b[201~', // bracketed paste
      '\x1b', //                   Escape
    ]) {
      expect(isTerminalAnswer(input), JSON.stringify(input)).toBe(false);
    }
  });
});

describe('ReplayAnswerGate', () => {
  it('drops answers only while a history replay parses', () => {
    const gate = new ReplayAnswerGate();
    expect(gate.drops('\x1b[?12;2$y')).toBe(false); // live: the asker gets it
    const end = gate.begin(false);
    expect(gate.drops('\x1b[?12;2$y')).toBe(true);
    expect(gate.drops('ls\r')).toBe(false); // typing always goes through
    end();
    end(); // idempotent
    expect(gate.drops('\x1b[?12;2$y')).toBe(false);
  });

  it('never drops during an exact resume, whose queries may be live', () => {
    const gate = new ReplayAnswerGate();
    const end = gate.begin(true);
    expect(gate.drops('\x1b[24;80R')).toBe(false);
    expect(gate.drops('\x1b]11;rgb:1f1f/1f1f/1f1f\x1b\\')).toBe(false);
    end();
  });

  it('reset clears a replay cut short by teardown', () => {
    const gate = new ReplayAnswerGate();
    gate.begin(false);
    gate.reset();
    expect(gate.drops('\x1b[?1;2c')).toBe(false);
  });
});
