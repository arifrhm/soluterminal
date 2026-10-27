import { describe, expect, it } from 'vitest';
import { ENTER_GAP_BRACKETED_PASTE_MS, ENTER_GAP_CLAUDE_MS, enterGapMs } from './ptySubmit';

// Locked-in numbers — these have to match daemon/pty_submit.go.
// If you change one side, change the other AND update this test.
describe('enterGapMs', () => {
  it('returns the longer Claude gap when bracketed paste is off', () => {
    expect(enterGapMs(false)).toBe(400);
    expect(enterGapMs(false)).toBe(ENTER_GAP_CLAUDE_MS);
  });

  it('returns the shorter bracketed-paste gap when on', () => {
    expect(enterGapMs(true)).toBe(100);
    expect(enterGapMs(true)).toBe(ENTER_GAP_BRACKETED_PASTE_MS);
  });

  it('exposes both constants for callers that want them named', () => {
    expect(ENTER_GAP_CLAUDE_MS).toBeGreaterThan(ENTER_GAP_BRACKETED_PASTE_MS);
  });
});
