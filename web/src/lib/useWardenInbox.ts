import { useEffect, useSyncExternalStore } from 'react';
import { operatorFeaturesAllowed, useOperatorFeatures } from '../entitlements';
import { api } from '../api';
import type { WardenRun } from '../types';
import { pruneWardenRead } from './wardenRead';

// Shared poll for the Warden's runs — module singleton so the inbox feed, the
// topbar bubble, and the footer chip read ONE fetch, mirroring useFeedbackInbox.
// GET /api/warden/status returns a calm empty set when the Warden is off or on
// an older daemon, so ambient polling is cheap and safe. Poll only while a
// consumer is mounted and the tab is visible.

const POLL_MS = 120_000;

let runs: WardenRun[] = [];
let listeners = new Set<() => void>();
let timer: ReturnType<typeof setInterval> | null = null;
let inflight = false;

async function refresh() {
  if (inflight || document.hidden) return;
  // Warden briefs and agent reports are team-only surfaces; a public install
  // never polls for them.
  if (!operatorFeaturesAllowed()) return;
  inflight = true;
  try {
    const res = await api.getWardenStatus();
    const next = res.runs ?? [];
    // Keep the read ledger bounded to runs that still exist.
    const pruned = pruneWardenRead(next);
    if (pruned || JSON.stringify(next) !== JSON.stringify(runs)) {
      runs = next;
      listeners.forEach((l) => l());
    }
  } catch {
    // Warden disabled / old daemon / hiccup: keep the last-known list.
  } finally {
    inflight = false;
  }
}

function subscribe(l: () => void): () => void {
  listeners.add(l);
  if (!timer) {
    void refresh();
    timer = setInterval(() => void refresh(), POLL_MS);
  }
  return () => {
    listeners.delete(l);
    if (listeners.size === 0 && timer) {
      clearInterval(timer);
      timer = null;
    }
  };
}

const NO_RUNS: WardenRun[] = [];

export function useWardenInbox(): WardenRun[] {
  const team = useOperatorFeatures();
  const snap = useSyncExternalStore(subscribe, () => runs);
  useEffect(() => {
    const onVis = () => { if (!document.hidden) void refresh(); };
    document.addEventListener('visibilitychange', onVis);
    return () => document.removeEventListener('visibilitychange', onVis);
  }, []);
  // The verdict usually lands after first mount; fetch as soon as it does
  // rather than waiting for the next poll tick.
  useEffect(() => {
    if (team) void refresh();
  }, [team]);
  return team ? snap : NO_RUNS;
}

/** Notify inbox consumers after a local read-ledger change (see wardenRead).
 *  The read state lives in a SEPARATE module, so the runs array itself is
 *  unchanged — reassign it to a fresh reference so useSyncExternalStore
 *  actually re-renders (Object.is would otherwise bail out). */
export function notifyWardenInbox(): void {
  runs = runs.slice();
  listeners.forEach((l) => l());
}

// Test hook: reset module state between tests.
export function __resetWardenInboxForTests(next: WardenRun[] = []): void {
  runs = next;
  listeners.forEach((l) => l());
}
