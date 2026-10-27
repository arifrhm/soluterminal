// Tests for the per-pane mid-turn steer queue. The queue must survive a warm-pane
// remount (the mounted NativePane re-seeds from this module map on mount), be
// FIFO, and stay isolated per pane — the bug this guards against is a queued
// follow-up being silently dropped when a pane remounts mid-turn.

import { describe, it, expect } from 'vitest';
import { getQueued, setQueued } from './useNativePane';

describe('mid-turn steer queue (per-pane FIFO, remount-survival)', () => {
  it('defaults to empty and persists a FIFO list', () => {
    expect(getQueued('q-a')).toEqual([]);
    setQueued('q-a', ['first', 'second']);
    // A fresh read (what a remounted NativePane's useState initializer does)
    // sees the queue intact, in order.
    expect(getQueued('q-a')).toEqual(['first', 'second']);
  });

  it('clears the entry when set empty', () => {
    setQueued('q-b', ['only']);
    expect(getQueued('q-b')).toEqual(['only']);
    setQueued('q-b', []);
    expect(getQueued('q-b')).toEqual([]);
  });

  it('isolates queues per pane', () => {
    setQueued('q-c', ['c1']);
    setQueued('q-d', ['d1', 'd2']);
    expect(getQueued('q-c')).toEqual(['c1']);
    expect(getQueued('q-d')).toEqual(['d1', 'd2']);
  });
});
