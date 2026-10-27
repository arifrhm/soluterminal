import { act, renderHook } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useNow } from './useNow';

describe('useNow shared cadence', () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it('shares one interval and stops it after the final subscriber unmounts', () => {
    vi.useFakeTimers();
    const start = Date.parse('2026-08-10T12:00:00Z');
    vi.setSystemTime(start);
    const setIntervalSpy = vi.spyOn(globalThis, 'setInterval');

    const first = renderHook(() => useNow(1_000));
    const second = renderHook(() => useNow(1_000));
    expect(setIntervalSpy).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(1);

    act(() => { vi.advanceTimersByTime(1_000); });
    expect(first.result.current).toBe(start + 1_000);
    expect(second.result.current).toBe(start + 1_000);

    first.unmount();
    expect(vi.getTimerCount()).toBe(1);
    second.unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it('uses separate shared intervals for separate cadences', () => {
    vi.useFakeTimers();
    const fast = renderHook(() => useNow(1_000));
    const slow = renderHook(() => useNow(30_000));
    expect(vi.getTimerCount()).toBe(2);
    fast.unmount();
    expect(vi.getTimerCount()).toBe(1);
    slow.unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
});
