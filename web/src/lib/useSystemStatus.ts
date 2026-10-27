import { useSyncExternalStore } from 'react';
import { api, ApiError, onRoutingScopeChange } from '../api';
import type { SystemStatus } from '../api';
import { useStore } from '../store';

export const SYSTEM_POLL_MS = 10_000;
export type SystemStatusError = null | 'old-daemon' | 'unavailable' | 'network';
export interface SystemStatusSnapshot { status: SystemStatus | null; error: SystemStatusError; lastOkMs: number; polling: boolean }

const initialSnapshot = (): SystemStatusSnapshot => ({ status: null, error: null, lastOkMs: 0, polling: false });
let snapshot = initialSnapshot();
const listeners = new Set<() => void>();
let timer: ReturnType<typeof setInterval> | null = null;
let inFlightGeneration: number | null = null;
let inFlightMeasure = false;
let pendingMeasure = false;
let generation = 0;

function emit(next: SystemStatusSnapshot) {
  if (JSON.stringify(next) === JSON.stringify(snapshot)) return;
  snapshot = next;
  listeners.forEach((listener) => listener());
}

async function pollOnce(opts?: { measure?: boolean }): Promise<void> {
  if (inFlightGeneration === generation) {
    if (opts?.measure && !inFlightMeasure) pendingMeasure = true;
    return;
  }
  if (useStore.getState().updateStatus?.applying === true) return;
  const requestGeneration = generation;
  inFlightGeneration = requestGeneration;
  inFlightMeasure = opts?.measure === true;
  if (!snapshot.status) emit({ ...snapshot, polling: true });
  try {
    const status = await api.systemStatus(opts);
    if (requestGeneration !== generation) return;
    if (JSON.stringify(status) === JSON.stringify(snapshot.status) && snapshot.error === null) {
      if (snapshot.polling) emit({ ...snapshot, polling: false });
    } else {
      emit({ status, error: null, lastOkMs: Date.now(), polling: false });
    }
  } catch (error) {
    if (requestGeneration !== generation) return;
    const kind: SystemStatusError = error instanceof ApiError && error.status === 404
      ? 'old-daemon'
      : error instanceof ApiError && error.status === 503
        ? 'unavailable'
        : 'network';
    emit({ ...snapshot, status: kind === 'network' ? snapshot.status : null, error: kind, polling: false });
  } finally {
    if (inFlightGeneration === requestGeneration) {
      inFlightGeneration = null;
      inFlightMeasure = false;
    }
    if (requestGeneration === generation && pendingMeasure) {
      pendingMeasure = false;
      void pollOnce({ measure: true });
    }
  }
}

function tick() {
  if (document.visibilityState === 'hidden') return;
  void pollOnce();
}

function onVisibilityChange() { if (document.visibilityState === 'visible') tick(); }

function subscribe(listener: () => void): () => void {
  const first = listeners.size === 0;
  listeners.add(listener);
  if (first) {
    document.addEventListener('visibilitychange', onVisibilityChange);
    tick();
    timer = setInterval(tick, SYSTEM_POLL_MS);
  }
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0) {
      if (timer !== null) clearInterval(timer);
      timer = null;
      document.removeEventListener('visibilitychange', onVisibilityChange);
    }
  };
}

onRoutingScopeChange(() => {
  generation += 1;
  inFlightGeneration = null;
  inFlightMeasure = false;
  pendingMeasure = false;
  emit(initialSnapshot());
  if (listeners.size > 0) tick();
});

export function useSystemStatus(): SystemStatusSnapshot { return useSyncExternalStore(subscribe, () => snapshot); }
export function getSystemStatusSnapshot(): SystemStatusSnapshot { return snapshot; }
export function refreshSystemStatus(opts?: { measure?: boolean }): Promise<void> { return pollOnce(opts); }
export function resetSystemStatusForTests(): void {
  if (timer !== null) clearInterval(timer);
  timer = null; listeners.clear(); document.removeEventListener('visibilitychange', onVisibilityChange);
  inFlightGeneration = null; inFlightMeasure = false; pendingMeasure = false; generation += 1; snapshot = initialSnapshot();
}
