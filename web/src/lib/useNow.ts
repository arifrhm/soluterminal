import { useCallback, useSyncExternalStore } from 'react';

// One shared module-level interval per cadence with subscriber counting:
// every relative-time cell in a 500-row table rides the same 30s tick, and
// the 1s elapsed tick exists only while a running-task cell is mounted.
// Ticks re-render subscribers; they must trigger zero network activity.

interface Tick {
  now: number;
  timer: ReturnType<typeof setInterval> | null;
  listeners: Set<() => void>;
}

const ticks = new Map<number, Tick>();

function entry(intervalMs: number): Tick {
  let tick = ticks.get(intervalMs);
  if (!tick) {
    tick = { now: Date.now(), timer: null, listeners: new Set() };
    ticks.set(intervalMs, tick);
  }
  return tick;
}

function subscribeTo(intervalMs: number, listener: () => void): () => void {
  const tick = entry(intervalMs);
  tick.listeners.add(listener);
  if (!tick.timer) {
    tick.now = Date.now();
    tick.timer = setInterval(() => {
      tick.now = Date.now();
      tick.listeners.forEach((notify) => notify());
    }, intervalMs);
  }
  return () => {
    tick.listeners.delete(listener);
    if (tick.listeners.size === 0 && tick.timer) {
      clearInterval(tick.timer);
      tick.timer = null;
    }
  };
}

export function useNow(intervalMs: number): number {
  const subscribe = useCallback((listener: () => void) => subscribeTo(intervalMs, listener), [intervalMs]);
  const getSnapshot = useCallback(() => entry(intervalMs).now, [intervalMs]);
  return useSyncExternalStore(subscribe, getSnapshot);
}
