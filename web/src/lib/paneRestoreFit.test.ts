import { describe, expect, it } from 'vitest';
import { computePaneRestoreTarget, type PaneRestoreSnapshot } from './paneRestoreFit';

function saved(overrides: Partial<PaneRestoreSnapshot> = {}): PaneRestoreSnapshot {
  return {
    viewportY: 120,
    linesFromBottom: 0,
    rows: 30,
    cols: 100,
    rectTop: 100,
    rectBottom: 700,
    wasAtBottom: false,
    followFromBottom: false,
    ...overrides,
  };
}

describe('computePaneRestoreTarget', () => {
  it('keeps a bottomed terminal pinned to the new bottom after restore refit', () => {
    expect(computePaneRestoreTarget(
      saved({ wasAtBottom: true, viewportY: 170, rows: 30 }),
      { ydisp: 80, length: 240, rows: 40, cols: 100, rectTop: 100, rectBottom: 700 },
    )).toMatchObject({
      targetY: 200,
      skipYdisp: false,
      isUserScrolling: false,
      mode: 'bottom',
    });
  });

  it('preserves near-bottom distance so live output does not make the thumb drift', () => {
    expect(computePaneRestoreTarget(
      saved({ followFromBottom: true, linesFromBottom: 4, viewportY: 166, rows: 30 }),
      { ydisp: 166, length: 230, rows: 40, cols: 100, rectTop: 100, rectBottom: 700 },
    )).toMatchObject({
      targetY: 186,
      skipYdisp: false,
      isUserScrolling: true,
      mode: 'follow-bot(4)',
    });
  });

  it('offsets absolute history reads for bottom-anchored panes whose top edge moved', () => {
    expect(computePaneRestoreTarget(
      saved({ viewportY: 120, rows: 30, rectTop: 100, rectBottom: 700 }),
      { ydisp: 120, length: 300, rows: 36, cols: 100, rectTop: 70, rectBottom: 700 },
    )).toMatchObject({
      targetY: 114,
      skipYdisp: false,
      isUserScrolling: true,
      mode: 'abs-botA(120-6)',
    });
  });

  it('trusts xterm reflow when columns changed for a history reader', () => {
    expect(computePaneRestoreTarget(
      saved({ viewportY: 120, rows: 30, cols: 100 }),
      { ydisp: 97, length: 300, rows: 30, cols: 82, rectTop: 100, rectBottom: 700 },
    )).toMatchObject({
      targetY: 97,
      skipYdisp: true,
      isUserScrolling: true,
      mode: 'reflow-trust(xterm yd=97)',
    });
  });
});
