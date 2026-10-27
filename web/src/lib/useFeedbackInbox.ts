import { useEffect, useSyncExternalStore } from 'react';
import { api, type FeedbackEvent } from '../api';

// Shared poll for the feedback-inbox events — module singleton so the footer
// chip and the inbox feed read ONE fetch, not one each (the useAgentsStatus
// pattern, minus the cloud fan-out). The endpoint is pure-DB on the cloud and
// returns a calm empty set on unpaired machines, so ambient polling is cheap
// and safe. Poll only while some consumer is mounted and the tab is visible.

const POLL_MS = 120_000;

let events: FeedbackEvent[] = [];
let listeners = new Set<() => void>();
let timer: ReturnType<typeof setInterval> | null = null;
let inflight = false;

async function refresh() {
  if (inflight || document.hidden) return;
  inflight = true;
  try {
    const res = await api.feedbackInbox();
    const next = res.events ?? [];
    // Referential stability: only notify when the payload actually changed,
    // so the always-mounted consumers don't re-render every poll.
    if (JSON.stringify(next) !== JSON.stringify(events)) {
      events = next;
      listeners.forEach((l) => l());
    }
  } catch {
    // Old daemon (no route) / cloud hiccup: keep the last-known list. An
    // inbox that briefly shows stale feedback rows beats one that flickers.
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

export function useFeedbackInbox(): FeedbackEvent[] {
  const snap = useSyncExternalStore(subscribe, () => events);
  // Catch up when the tab becomes visible again (the interval skips hidden).
  useEffect(() => {
    const onVis = () => { if (!document.hidden) void refresh(); };
    document.addEventListener('visibilitychange', onVis);
    return () => document.removeEventListener('visibilitychange', onVis);
  }, []);
  return snap;
}

// Optimistic local read-stamp: flips the row read immediately (and notifies
// subscribers) while the POST syncs the cloud; the next poll reconciles.
export function markFeedbackEventRead(number: number): void {
  let changed = false;
  events = events.map((e) => {
    if (e.number !== number || e.readAt) return e;
    changed = true;
    return { ...e, readAt: new Date().toISOString() };
  });
  if (changed) listeners.forEach((l) => l());
  void api.feedbackInboxRead(number).catch(() => {});
}

// Test hook: reset module state between tests.
export function __resetFeedbackInboxForTests(next: FeedbackEvent[] = []): void {
  events = next;
  listeners.forEach((l) => l());
}
