import { describe, expect, it } from 'vitest';
import { RebuildBackoff, REBUILD_MAX_MS } from './rebuildBackoff';

describe('RebuildBackoff', () => {
  // After the device is removed, the UI event stream used to be rebuilt every
  // 2 s against a daemon answering device_not_approved (~2.5 requests/s).
  it('doubles from 2 s to a 30 s cap across consecutive failures, and resets on open', () => {
    const b = new RebuildBackoff();
    const delays = Array.from({ length: 7 }, () => b.fail());
    expect(delays).toEqual([2_000, 4_000, 8_000, 16_000, 30_000, 30_000, 30_000]);
    expect(Math.max(...delays)).toBe(REBUILD_MAX_MS);
    b.reset();
    expect(b.fail()).toBe(2_000);
  });

  it('keeps a refused stream to a few attempts a minute', () => {
    const b = new RebuildBackoff();
    let elapsed = 0;
    let attempts = 0;
    while (elapsed < 120_000) {
      elapsed += b.fail();
      attempts++;
    }
    expect(attempts).toBeLessThan(10); // was 60 at a fixed 2 s
  });
});
