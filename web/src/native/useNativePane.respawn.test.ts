// Tests for BUG 2 — respawn detection + full cache reset (Terminal↔Native mode
// switch). A mode switch kills + respawns the pane as a NEW agent process for
// the SAME pane id; the daemon's per-pane ring resets to seq 1,2,3…. The
// surviving client cache must be fully wiped (and re-armed for the since=0 seed)
// so those low seqs fold instead of being deduped against the dead session.
//
// We test the module-private fold cache via the __useNativePaneTest surface
// (rendering the hook pulls the whole store/cloud graph into React and OOM'd a
// prior agent under jsdom), plus the pure pid-detection + reset helpers.

import { beforeEach, describe, expect, it } from 'vitest';
import {
  notePidAndDetectRespawn,
  noteEpochAndDetectReset,
  resetCacheForRespawn,
  __useNativePaneTest as T,
} from './useNativePane';
import type { NativeEvent } from './nativeEvents';

function ev(paneId: string, seq: number, partial: Partial<NativeEvent> = {}): NativeEvent {
  return {
    seq,
    paneId,
    type: 'text',
    fromReplay: false,
    text: `e${seq}`,
    ...partial,
  } as NativeEvent;
}

beforeEach(() => {
  T.resetAll();
});

describe('notePidAndDetectRespawn', () => {
  it('does NOT flag the initial pid observation (first mount)', () => {
    expect(notePidAndDetectRespawn('p1', 1234)).toBe(false);
  });

  it('does NOT flag a repeated same-pid observation (warm-pane remount)', () => {
    notePidAndDetectRespawn('p1', 1234);
    expect(notePidAndDetectRespawn('p1', 1234)).toBe(false);
    expect(notePidAndDetectRespawn('p1', 1234)).toBe(false);
  });

  it('flags a real pid CHANGE as a respawn', () => {
    notePidAndDetectRespawn('p1', 1234);
    expect(notePidAndDetectRespawn('p1', 5678)).toBe(true);
  });

  it('does NOT flag transitions through an unknown (0/undefined) pid', () => {
    notePidAndDetectRespawn('p1', 1234);
    // A poll that hasn't populated pid yet must not look like a respawn…
    expect(notePidAndDetectRespawn('p1', undefined)).toBe(false);
    expect(notePidAndDetectRespawn('p1', 0)).toBe(false);
    // …and the SAME pid returning after the blank is still not a respawn.
    expect(notePidAndDetectRespawn('p1', 1234)).toBe(false);
  });

  it('records the latest valid pid across a blank so a later DIFFERENT pid fires', () => {
    notePidAndDetectRespawn('p1', 1234);
    notePidAndDetectRespawn('p1', undefined); // blank poll — ignored, pid kept
    expect(notePidAndDetectRespawn('p1', 9999)).toBe(true);
  });

  it('isolates detection per pane id', () => {
    notePidAndDetectRespawn('p1', 1234);
    // A different pane's FIRST pid is never a respawn even if it differs from p1.
    expect(notePidAndDetectRespawn('p2', 5678)).toBe(false);
  });
});

// noteEpochAndDetectReset: the daemon's per-process epoch changes on a restart/
// swap, which rebuilds a native pane's ring from seq 1 even though the surviving
// agent keeps its pid. Same shape as the pid detector; drives the SAME reset so a
// mid-turn reattach rebuilds cleanly instead of jumbling the re-seq'd replay.
describe('noteEpochAndDetectReset (daemon swap, pid unchanged)', () => {
  it('does NOT flag the initial epoch observation (fresh mount / first poll)', () => {
    expect(noteEpochAndDetectReset('p1', 111)).toBe(false);
  });

  it('does NOT flag a repeated same-epoch observation (steady daemon)', () => {
    noteEpochAndDetectReset('p1', 111);
    expect(noteEpochAndDetectReset('p1', 111)).toBe(false);
    expect(noteEpochAndDetectReset('p1', 111)).toBe(false);
  });

  it('flags a real epoch CHANGE as a reset (daemon restart/swap)', () => {
    noteEpochAndDetectReset('p1', 111);
    expect(noteEpochAndDetectReset('p1', 222)).toBe(true);
  });

  it('does NOT flag transitions through an unknown (0/undefined) epoch (older daemon)', () => {
    noteEpochAndDetectReset('p1', 111);
    // A poll from an epoch-less older daemon must not look like a reset…
    expect(noteEpochAndDetectReset('p1', undefined)).toBe(false);
    expect(noteEpochAndDetectReset('p1', 0)).toBe(false);
    // …and the SAME epoch returning after the blank is still not a reset.
    expect(noteEpochAndDetectReset('p1', 111)).toBe(false);
  });

  it('is independent of pid detection (a daemon swap keeps the pid)', () => {
    // Same pid throughout (agent survives), but the epoch advances → reset fires.
    expect(notePidAndDetectRespawn('p1', 1234)).toBe(false);
    expect(noteEpochAndDetectReset('p1', 111)).toBe(false);
    expect(notePidAndDetectRespawn('p1', 1234)).toBe(false); // pid unchanged
    expect(noteEpochAndDetectReset('p1', 222)).toBe(true); // epoch advanced → reset
  });

  it('isolates detection per pane id', () => {
    noteEpochAndDetectReset('p1', 111);
    expect(noteEpochAndDetectReset('p2', 222)).toBe(false); // p2's first epoch
  });

  // The transitional swap: a pre-epoch daemon (≤v0.0.629) never sent an epoch, so
  // the client built a live session with NO recorded epoch. When it swaps to an
  // epoch daemon the value goes undefined→defined — a plain "first observation"
  // that must STILL reset, because the ring was rebuilt from seq 1. hadContent
  // (cache.lastSeq>0) distinguishes it from a genuine fresh mount.
  it('flags the FIRST epoch as a reset when a live session predates it (rollout swap)', () => {
    // First epoch ever, but the cache already holds content → epoch-less→epoch swap.
    expect(noteEpochAndDetectReset('p1', 111, /* hadContent */ true)).toBe(true);
  });

  it('does NOT flag the first epoch on a genuinely fresh mount (no content)', () => {
    expect(noteEpochAndDetectReset('p1', 111, /* hadContent */ false)).toBe(false);
  });

  it('hadContent is irrelevant once an epoch is known (only the change matters)', () => {
    noteEpochAndDetectReset('p1', 111, false); // record baseline
    expect(noteEpochAndDetectReset('p1', 111, true)).toBe(false); // unchanged → no reset
    expect(noteEpochAndDetectReset('p1', 222, false)).toBe(true); // changed → reset
  });
});

describe('respawn fully resets the fold cache so the fresh ring loads', () => {
  it('folds fresh seq-1 events after a respawn instead of deduping them', () => {
    const pane = 'pane-x';
    const c = T.getCache(pane);

    // Dead session: ring seqs 1..4 fold normally.
    for (let s = 1; s <= 4; s++) expect(T.foldEvent(c, ev(pane, s))).toBe(true);
    expect(c.events.length).toBe(4);
    expect(c.lastSeq).toBe(4);
    expect(c.seen.has(1)).toBe(true);

    // Sanity: WITHOUT a reset, the respawned ring's seq 1..4 would be deduped
    // as "already seen" — this is exactly the freeze/blank bug.
    expect(T.foldEvent(c, ev(pane, 1, { text: 'new1' }))).toBe(false);
    expect(c.events.length).toBe(4);

    // Simulate the respawn: pid changes, so we wipe + re-arm the cache.
    expect(notePidAndDetectRespawn(pane, 111)).toBe(false); // initial
    expect(notePidAndDetectRespawn(pane, 222)).toBe(true); // respawn detected
    const fresh = resetCacheForRespawn(pane);

    // The new cache must be empty and RE-ARMED for the since=0 seed.
    expect(fresh.events.length).toBe(0);
    expect(fresh.seen.size).toBe(0);
    expect(fresh.lastSeq).toBe(0);
    expect(fresh.seeded).toBe(false);
    // getCache must now return the fresh cache (the map entry was swapped).
    expect(T.getCache(pane)).toBe(fresh);

    // The respawned ring's fresh seq 1..4 now fold cleanly (not deduped).
    for (let s = 1; s <= 4; s++) {
      expect(T.foldEvent(fresh, ev(pane, s, { text: `r${s}` }))).toBe(true);
    }
    expect(fresh.events.length).toBe(4);
    expect(fresh.lastSeq).toBe(4);
    expect(fresh.events.map((e) => e.text)).toEqual(['r1', 'r2', 'r3', 'r4']);
  });

  it('a respawn reset does NOT preserve seeded (distinct from resetCache)', () => {
    const pane = 'pane-y';
    const c = T.getCache(pane);
    c.seeded = true; // pretend the dead session had already seeded
    const fresh = resetCacheForRespawn(pane);
    // Unlike the truncated-history resetCache (which keeps seeded), a respawn
    // must re-run the since=0 transcript seed for the brand-new ring.
    expect(fresh.seeded).toBe(false);
  });
});

// The daemon now announces its nativeEpoch as a named `epoch` SSE frame on every
// (re)connect, BEFORE the ring snapshot. The wire handler in useNativePane reads
// it and runs the SAME reset the [paneId,pid,epoch] effect does — but without
// waiting for the 3s poll, so a silent EventSource auto-reconnect after a daemon
// auto-update swap resets BEFORE the fresh seq-1 ring folds. This is the fix for
// "Focus loses the latest few messages after an update until a Term toggle." We
// test the handler's DECISION sequence (parse → noteEpochAndDetectReset(hadContent
// = lastSeq>0) → resetCacheForRespawn) at the helper level; rendering the hook to
// drive a real EventSource OOMs jsdom (see the file header note).
describe('wire epoch frame drives the same reset as a respawn', () => {
  it('a CHANGED epoch wipes the stale seeded cache so the fresh ring folds', () => {
    const pane = 'pane-w';
    const c = T.getCache(pane);
    // Mount: the pre-swap epoch is recorded on the fresh cache (lastSeq=0) before
    // any content — by the [paneId,pid,epoch] effect or the first `epoch` frame.
    expect(noteEpochAndDetectReset(pane, 100, c.lastSeq > 0)).toBe(false);
    // Now the session builds content: seeded + seqs 1..3 folded.
    c.seeded = true;
    for (let s = 1; s <= 3; s++) T.foldEvent(c, ev(pane, s));
    expect(c.lastSeq).toBe(3);

    // Daemon swap: the reconnect's `epoch` frame carries a NEW epoch. The handler
    // reads hadContent = lastSeq>0 (true) → reset fires on the change.
    expect(noteEpochAndDetectReset(pane, 200, T.getCache(pane).lastSeq > 0)).toBe(true);
    const fresh = resetCacheForRespawn(pane);
    expect(fresh.seeded).toBe(false);
    // Without the reset these re-seq'd 1..3 would be deduped (the missing-tail bug);
    // after it they fold cleanly.
    for (let s = 1; s <= 3; s++) {
      expect(T.foldEvent(fresh, ev(pane, s, { text: `n${s}` }))).toBe(true);
    }
    expect(fresh.events.map((e) => e.text)).toEqual(['n1', 'n2', 'n3']);
  });

  it('a SAME-epoch reconnect (transient SSE drop) does NOT reset the cache', () => {
    const pane = 'pane-w2';
    const c = T.getCache(pane);
    expect(noteEpochAndDetectReset(pane, 100, c.lastSeq > 0)).toBe(false); // mount records 100
    c.seeded = true;
    T.foldEvent(c, ev(pane, 1));
    // A transient drop reconnects to the SAME daemon → same epoch → cache preserved.
    expect(noteEpochAndDetectReset(pane, 100, T.getCache(pane).lastSeq > 0)).toBe(false);
    expect(T.getCache(pane).events.length).toBe(1); // untouched
  });
});
