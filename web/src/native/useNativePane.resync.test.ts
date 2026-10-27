// Tests for BUG 1 — periodic idle-resync. A half-open SSE (or a non-blocking
// fanout drop) can swallow a turn's TAIL with no later event to trip
// gap-detection and no reconnect to trip onopen, freezing the spinner forever
// though the daemon HAS the reply. The hook arms a steady interval that calls
// backfill(getCache(paneId).lastSeq) while the stream is OPEN to self-heal it.
//
// The timer lives inside the SSE effect (which pulls the store/cloud graph into
// a React render — OOM'd a prior agent under jsdom), so we test the pure
// predicate that gates each tick, sharing the exact source of truth the effect
// uses. jsdom doesn't define EventSource, so we shim the readyState constants
// the predicate reads.

import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest';
import { __useNativePaneTest, shouldIdleResync } from './useNativePane';
import type { NativeEvent } from './nativeEvents';

const ORIGINAL_WS = (globalThis as { WebSocket?: unknown }).WebSocket;

beforeAll(() => {
  // Minimal shim: the predicate only reads WebSocket.OPEN (native panes moved
  // from SSE→WS). jsdom doesn't define WebSocket, so shim the readyState const.
  (globalThis as { WebSocket?: unknown }).WebSocket = {
    CONNECTING: 0,
    OPEN: 1,
    CLOSING: 2,
    CLOSED: 3,
  };
});

afterAll(() => {
  (globalThis as { WebSocket?: unknown }).WebSocket = ORIGINAL_WS;
});

beforeEach(() => {
  __useNativePaneTest.resetAll();
});

describe('shouldIdleResync (idle-resync tick gate)', () => {
  it('fires while live and OPEN', () => {
    expect(shouldIdleResync(false, 1 /* OPEN */)).toBe(true);
  });

  it('does NOT fire after teardown even when OPEN', () => {
    // `closed` is set in the effect cleanup; a tick that races teardown must no-op.
    expect(shouldIdleResync(true, 1 /* OPEN */)).toBe(false);
  });

  it('does NOT fire while CONNECTING (onopen will resync on connect)', () => {
    expect(shouldIdleResync(false, 0 /* CONNECTING */)).toBe(false);
  });

  it('does NOT fire while CLOSED (onclose drives the reconnect)', () => {
    expect(shouldIdleResync(false, 3 /* WebSocket.CLOSED */)).toBe(false);
  });
});

describe('session_init freshness', () => {
  const init = (seq: number, model: string, fromReplay = false): NativeEvent => ({
    seq,
    paneId: 'pane-model',
    type: 'session_init',
    fromReplay,
    model,
  });

  it('lets a newer live init replace an older live init', () => {
    const cache = __useNativePaneTest.getCache('pane-model');
    __useNativePaneTest.foldEvent(cache, init(1, 'claude-sonnet-4-6'));
    __useNativePaneTest.foldEvent(cache, init(2, 'claude-opus-4-8'));
    expect(cache.sessionInit?.model).toBe('claude-opus-4-8');
  });

  it('never lets a replayed init downgrade a live init', () => {
    const cache = __useNativePaneTest.getCache('pane-model');
    __useNativePaneTest.foldEvent(cache, init(2, 'claude-opus-4-8'));
    __useNativePaneTest.foldEvent(cache, init(3, 'claude-sonnet-4-6', true));
    expect(cache.sessionInit?.model).toBe('claude-opus-4-8');
  });
});
