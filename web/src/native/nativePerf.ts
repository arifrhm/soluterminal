// Phase-0 instrumentation for the Focus / native-renderer latency work.
//
// EVERYTHING here is a NO-OP unless explicitly enabled, so it is free to leave in
// production. Turn it on from the browser devtools console with:
//
//   localStorage.setItem('solu.nativePerf', '1'); location.reload();
//
// (or set `window.__soluNativePerf = true` before a pane mounts). Remove the key to
// disable. With the flag on, the helpers emit `[native-perf]`-tagged console.debug
// lines you can read directly or filter in the console:
//
//   • turn timeline   — submit → first delta → turn-end, per pane, with elapsed ms
//                        since submit (i.e. time-to-first-token and turn duration).
//   • publish counter — how many React state bumps (setEvents) a turn caused. This
//                        is the number rAF-coalescing is meant to collapse.
//   • render counter  — per streaming group view, how many times it re-rendered
//                        during a turn (the "memo defeated by fresh props" signal).
//
// Compare these numbers against terminal mode to localise where "laggier" lives,
// and before/after a fix to prove it landed (honours the "data before speculative
// fixes" rule — measure first, then change, then re-measure).

import { useRef } from 'react';

let cached: boolean | null = null;

// nativePerfEnabled — read the opt-in flag ONCE (memoised; a reload re-evaluates).
// Guarded so a missing window/localStorage (SSR, locked-down env) never throws.
export function nativePerfEnabled(): boolean {
  if (cached !== null) return cached;
  try {
    cached =
      (typeof window !== 'undefined' &&
        (window as unknown as { __soluNativePerf?: boolean }).__soluNativePerf === true) ||
      (typeof localStorage !== 'undefined' && localStorage.getItem('solu.nativePerf') === '1');
  } catch {
    cached = false;
  }
  return cached ?? false;
}

function now(): number {
  return typeof performance !== 'undefined' && performance.now ? performance.now() : Date.now();
}

function short(paneId: string): string {
  return paneId.length > 6 ? paneId.slice(0, 6) : paneId;
}

// Per-pane turn-start clock so first-delta / turn-end can report elapsed ms.
const turnStart = new Map<string, number>();
const sawFirstDelta = new Set<string>();
const publishCount = new Map<string, number>();

// perfTurnSubmit — call on optimistic send. Stamps the per-pane turn clock.
export function perfTurnSubmit(paneId: string): void {
  if (!nativePerfEnabled()) return;
  turnStart.set(paneId, now());
  sawFirstDelta.delete(paneId);
  publishCount.set(paneId, 0);
  // eslint-disable-next-line no-console
  console.debug(`[native-perf] ${short(paneId)} submit`);
}

// perfFirstDelta — call on the FIRST streamed text/thinking delta of a turn. Logs
// time-to-first-token since submit. Fires at most once per turn (reset on submit/end).
export function perfFirstDelta(paneId: string): void {
  if (!nativePerfEnabled()) return;
  if (sawFirstDelta.has(paneId)) return;
  sawFirstDelta.add(paneId);
  const t0 = turnStart.get(paneId);
  const dt = t0 != null ? Math.round(now() - t0) : -1;
  // eslint-disable-next-line no-console
  console.debug(`[native-perf] ${short(paneId)} first-delta +${dt}ms`);
}

// perfTurnEnd — call when a turn settles (turn_end / usage / error). Logs the turn
// duration since submit and the publish (setEvents) count for the turn.
export function perfTurnEnd(paneId: string): void {
  if (!nativePerfEnabled()) return;
  const t0 = turnStart.get(paneId);
  const dt = t0 != null ? Math.round(now() - t0) : -1;
  const pubs = publishCount.get(paneId) ?? 0;
  // eslint-disable-next-line no-console
  console.debug(`[native-perf] ${short(paneId)} turn-end +${dt}ms (${pubs} publishes)`);
  sawFirstDelta.delete(paneId);
}

// perfPublish — call each time the hook actually bumps React state (setEvents).
// Counts state bumps per turn so the rAF-coalescing win is measurable.
export function perfPublish(paneId: string): void {
  if (!nativePerfEnabled()) return;
  publishCount.set(paneId, (publishCount.get(paneId) ?? 0) + 1);
}

// useRenderCount — hook the streaming group views call to log how many times they
// re-render. The useRef call is unconditional (hook rules); only the log is gated.
export function useRenderCount(name: string): void {
  const n = useRef(0);
  n.current += 1;
  if (nativePerfEnabled()) {
    // eslint-disable-next-line no-console
    console.debug(`[native-perf] render ${name} #${n.current}`);
  }
}
