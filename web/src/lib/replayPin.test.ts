import { describe, expect, it } from 'vitest';
import { replayPinTarget } from './replayPin';

describe('replayPinTarget', () => {
  it('pins a full replay to the announced size after the pane shrank', () => {
    expect(replayPinTarget(false, { cols: 130, rows: 39 }, { cols: 130, rows: 35 })).toEqual({
      cols: 130,
      rows: 39,
    });
  });

  it('does not pin when the size is unchanged', () => {
    expect(replayPinTarget(false, { cols: 130, rows: 39 }, { cols: 130, rows: 39 })).toBeNull();
  });

  it('never pins an exact resume', () => {
    expect(replayPinTarget(true, { cols: 130, rows: 39 }, { cols: 130, rows: 35 })).toBeNull();
  });

  it('ignores an unknown announced size', () => {
    expect(replayPinTarget(false, { cols: 0, rows: 0 }, { cols: 130, rows: 35 })).toBeNull();
  });
});
