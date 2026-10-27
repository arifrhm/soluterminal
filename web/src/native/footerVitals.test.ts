import { describe, expect, it } from 'vitest';
import type { NativeEvent } from './nativeEvents';
import { contextUsageFraction, formatRateLimitReset } from './footerVitals';

function usage(fields: Partial<NativeEvent>): NativeEvent {
  return { seq: 1, paneId: 'p1', type: 'usage', fromReplay: false, ...fields };
}

describe('contextUsageFraction', () => {
  it('adds Claude uncached, cache-read, and cache-write prompt tokens', () => {
    expect(contextUsageFraction(usage({
      inputTokens: 10_000,
      cachedInputTokens: 70_000,
      cacheCreationTokens: 20_000,
    }), 'claude', 'claude-haiku-4-5')).toBeCloseTo(0.5);
  });

  it('uses Codex authoritative total/window without double-counting cache', () => {
    const result = contextUsageFraction(usage({
      totalTokens: 145_684,
      inputTokens: 145_467,
      cachedInputTokens: 137_984,
      outputTokens: 217,
      contextWindowTokens: 258_400,
    }), 'codex', 'gpt-5.6-sol');
    expect(result).toBeCloseTo(145_684 / 258_400);
  });

  it('hides Codex context when the provider omitted its window', () => {
    expect(contextUsageFraction(usage({ totalTokens: 50_000 }), 'codex', 'unknown')).toBeUndefined();
  });

  it('clamps an overfull provider reading to 100%', () => {
    expect(contextUsageFraction(usage({ totalTokens: 300, contextWindowTokens: 200 }), 'codex')).toBe(1);
  });
});

describe('formatRateLimitReset', () => {
  it('shows only the time for a reset later today', () => {
    const now = new Date(2026, 6, 13, 9, 0, 0);
    const reset = new Date(2026, 6, 13, 15, 30, 0);
    const label = formatRateLimitReset(reset.getTime() / 1000, now.getTime(), 'en-US');
    expect(label).toMatch(/3:30 PM/);
    expect(label).not.toMatch(/Jul|Mon|Tue|Wed|Thu|Fri|Sat|Sun/);
  });

  it('includes a weekday and date for a later-day reset', () => {
    const now = new Date(2026, 6, 13, 9, 0, 0);
    const reset = new Date(2026, 6, 20, 15, 30, 0);
    const label = formatRateLimitReset(reset.getTime() / 1000, now.getTime(), 'en-US');
    expect(label).toMatch(/Mon, Jul 20/);
    expect(label).toMatch(/3:30 PM/);
  });
});
