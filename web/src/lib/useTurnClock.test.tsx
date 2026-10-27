import { act, renderHook } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useTurnClock } from './useTurnClock';
import { useStore } from '../store';

afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); });
describe('turn clock', () => {
  it('advances every five seconds, suspends hidden ticks and refreshes at restoration', () => {
    vi.useFakeTimers(); vi.setSystemTime(1_791_178_668_011);
    const interval = vi.spyOn(globalThis, 'setInterval');
    const clear = vi.spyOn(globalThis, 'clearInterval');
    let visibility = 'visible';
    vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visibility as DocumentVisibilityState);
    const { result, unmount } = renderHook(() => useTurnClock(42));
    const original = result.current;
    act(() => vi.advanceTimersByTime(5_000));
    expect(result.current).toBe(original + 5_000);
    act(() => { visibility = 'hidden'; document.dispatchEvent(new Event('visibilitychange')); });
    act(() => vi.advanceTimersByTime(60_000));
    expect(result.current).toBe(original + 5_000);
    act(() => { visibility = 'visible'; document.dispatchEvent(new Event('visibilitychange')); });
    expect(result.current).toBe(original + 65_000);
    for (const event of ['focus', 'pageshow']) {
      vi.setSystemTime(Date.now() + 600);
      act(() => window.dispatchEvent(new Event(event)));
      expect(result.current).toBe(Date.now());
    }
    const ownedInterval = interval.mock.results.at(-1)!.value;
    unmount();
    expect(clear).toHaveBeenCalledWith(ownedInterval);
  });
  it('refreshes on workspace and selection changes without resetting the completion epoch', () => {
    vi.useFakeTimers(); vi.setSystemTime(1_791_178_668_011);
    const { result, rerender } = renderHook(({ ended }) => useTurnClock(ended), { initialProps: { ended: 100 } });
    vi.setSystemTime(Date.now() + 45_000);
    act(() => useStore.setState({ activeWorkspace: 'clock-test-restore' }));
    expect(result.current).toBe(Date.now());
    vi.setSystemTime(Date.now() + 4_000);
    rerender({ ended: 200 });
    expect(result.current).toBe(Date.now());
  });
});
