// Regression tests for the long-standing "first native message after a term→
// native switch" bug: the user's first prompt either VANISHED (only Claude's
// reply showed) or rendered BELOW the reply.
//
// Root cause (all client-side, in foldEvent): on a switch the transcript seed
// pre-loads EVERY prior prompt's text into c.userTexts. When the user's first
// native prompt REUSED any prior text ("hi", "yes", "continue", "ok" — very
// common), its live `--replay-user-messages` echo matched c.userTexts and was
// DROPPED as a seed duplicate — even though the user had genuinely just typed
// it. The optimistic pending row was already reconciled away, so the prompt was
// gone. The fix: a live echo that reconciles a PENDING optimistic row (only a
// real local send creates one) is never dropped, and a SEED never consumes a
// pending row.
//
// We drive the module-private fold cache + pending list via __useNativePaneTest
// (rendering the hook pulls the whole store/cloud graph into React and OOM'd a
// prior agent under jsdom).

import { beforeEach, describe, expect, it } from 'vitest';
import { __useNativePaneTest as T } from './useNativePane';
import type { NativeEvent } from './nativeEvents';

function userEv(paneId: string, seq: number, text: string, fromReplay = false): NativeEvent {
  return { seq, paneId, type: 'user_text', fromReplay, text } as NativeEvent;
}
function deltaEv(paneId: string, seq: number, mid: string, text: string): NativeEvent {
  return { seq, paneId, type: 'text_delta', messageId: mid, fromReplay: false, text } as NativeEvent;
}
function textEv(paneId: string, seq: number, mid: string, text: string): NativeEvent {
  return { seq, paneId, type: 'text', messageId: mid, fromReplay: false, text } as NativeEvent;
}
function replayText(paneId: string, seq: number, text: string): NativeEvent {
  return { seq, paneId, type: 'text', fromReplay: true, text } as NativeEvent;
}
function turnEndEv(paneId: string, seq: number): NativeEvent {
  return { seq, paneId, type: 'turn_end', fromReplay: false } as NativeEvent;
}
const SEED = 2 ** 52; // disjoint seed seq block (daemon's seedSeqBase)

beforeEach(() => {
  T.resetAll();
});

describe('first native message after term→native switch', () => {
  it('renders a just-typed prompt whose text matches prior history (no vanish)', () => {
    const pane = 'pane-a';
    const c = T.getCache(pane);

    // Switch seeds the prior conversation: a previous "hi" turn (FromReplay).
    expect(T.foldEvent(c, userEv(pane, 1 << 52, 'hi', true))).toBe(true);
    expect(c.userTexts.has('hi')).toBe(true);

    // The user types "hi" again → sendOptimistic pushes a pending row.
    T.getPending(pane).push({ localMsgId: 'm1', text: 'hi' });

    // Its live replay echo arrives. It MUST fold (render) and reconcile the
    // pending row — NOT be dropped as a seed duplicate (the old bug).
    expect(T.foldEvent(c, userEv(pane, 4, 'hi', false))).toBe(true);
    expect(T.getPending(pane)).toHaveLength(0); // pending reconciled
    // Two "hi" turns now exist: the seeded history one + the new live one.
    const hello = c.events.filter((e) => e.type === 'user_text' && e.text === 'hi');
    expect(hello).toHaveLength(2);
    // The new turn is the LIVE one (low seq), folded after the seed.
    expect(hello.some((e) => !e.fromReplay && e.seq === 4)).toBe(true);
  });

  it('a transcript SEED never consumes a pending optimistic row', () => {
    const pane = 'pane-b';
    const c = T.getCache(pane);

    // The user typed "continue" (pending row exists) BEFORE the seed resolves.
    T.getPending(pane).push({ localMsgId: 'm1', text: 'continue' });

    // The seed (FromReplay) folds a prior "continue" turn. It must NOT reconcile
    // (consume) the pending row — that race would orphan the just-typed prompt.
    expect(T.foldEvent(c, userEv(pane, 1 << 52, 'continue', true))).toBe(true);
    expect(T.getPending(pane)).toHaveLength(1); // pending survives the seed

    // The live echo then reconciles it and renders.
    expect(T.foldEvent(c, userEv(pane, 5, 'continue', false))).toBe(true);
    expect(T.getPending(pane)).toHaveLength(0);
    expect(c.events.some((e) => !e.fromReplay && e.type === 'user_text' && e.text === 'continue')).toBe(
      true,
    );
  });

  it('still drops an AUTOMATIC resume re-echo (no pending row → seed dedup intact)', () => {
    const pane = 'pane-c';
    const c = T.getCache(pane);

    // Resume seeds a prior "yes" turn; the user typed NOTHING this time.
    expect(T.foldEvent(c, userEv(pane, 1 << 52, 'yes', true))).toBe(true);

    // The daemon's --replay-user-messages re-echoes the last prompt LIVE. With no
    // pending row, this is a pure history re-delivery and must be dropped once.
    expect(T.foldEvent(c, userEv(pane, 3, 'yes', false))).toBe(false);
    expect(c.events.filter((e) => e.type === 'user_text' && e.text === 'yes')).toHaveLength(1);

    // The dedup entry is consumed, so a LATER genuinely-repeated live "yes"
    // (with a pending row) still renders.
    T.getPending(pane).push({ localMsgId: 'm2', text: 'yes' });
    expect(T.foldEvent(c, userEv(pane, 9, 'yes', false))).toBe(true);
    expect(T.getPending(pane)).toHaveLength(0);
  });

  it('two genuinely-repeated live prompts both render (no seed involved)', () => {
    const pane = 'pane-d';
    const c = T.getCache(pane);

    T.getPending(pane).push({ localMsgId: 'm1', text: 'go' });
    T.getPending(pane).push({ localMsgId: 'm2', text: 'go' });

    expect(T.foldEvent(c, userEv(pane, 2, 'go', false))).toBe(true);
    expect(T.foldEvent(c, userEv(pane, 7, 'go', false))).toBe(true);
    expect(c.events.filter((e) => e.type === 'user_text' && e.text === 'go')).toHaveLength(2);
    expect(T.getPending(pane)).toHaveLength(0);
  });
});

// Ordering: Claude's --replay-user-messages echo is NOT guaranteed to precede the
// reply on stdout (observed live: reply deltas at seq 3-9, the "hi there" echo at
// seq 10), and the daemon seqs in arrival order — so a late echo appended at the
// tail renders BELOW the answer. A live user_text must anchor at the START of the
// turn it triggered. The seed/transcript is already correctly ordered, so this
// only reshapes the live, in-flight turn.
describe('live user_text anchors above its reply (late CLI echo)', () => {
  it('echo arriving AFTER the reply still renders above it (the real bug)', () => {
    const pane = 'pane-late';
    const c = T.getCache(pane);

    // Reply streams first — the CLI emitted deltas before echoing the user msg.
    expect(T.foldEvent(c, deltaEv(pane, 3, 'm1', 'Hey '))).toBe(true);
    expect(T.foldEvent(c, deltaEv(pane, 4, 'm1', 'there'))).toBe(true);
    // The user's message echo lands late (seq 10) and reconciles the pending row.
    T.getPending(pane).push({ localMsgId: 'x', text: 'hi there' });
    expect(T.foldEvent(c, userEv(pane, 10, 'hi there', false))).toBe(true);
    // Authoritative full text supersedes the streaming block (same messageId).
    expect(T.foldEvent(c, textEv(pane, 11, 'm1', 'Hey there'))).toBe(true);

    // Rendered order: the user's message FIRST, then the single reply block.
    const rows = c.events
      .filter((e) => e.type === 'user_text' || e.type === 'text')
      .map((e) => `${e.type}:${e.text}`);
    expect(rows).toEqual(['user_text:hi there', 'text:Hey there']);
    // Coalescing survived the splice: one block, superseded in place (no dup).
    expect(c.events.filter((e) => e.type === 'text')).toHaveLength(1);
    expect(T.getPending(pane)).toHaveLength(0);
  });

  it('echo arriving BEFORE the reply keeps normal order (append path)', () => {
    const pane = 'pane-early';
    const c = T.getCache(pane);

    T.getPending(pane).push({ localMsgId: 'x', text: 'write a poem' });
    expect(T.foldEvent(c, userEv(pane, 4, 'write a poem', false))).toBe(true);
    expect(T.foldEvent(c, deltaEv(pane, 7, 'm1', 'Roses'))).toBe(true);
    expect(T.foldEvent(c, textEv(pane, 9, 'm1', 'Roses are red'))).toBe(true);

    const rows = c.events
      .filter((e) => e.type === 'user_text' || e.type === 'text')
      .map((e) => e.text);
    expect(rows).toEqual(['write a poem', 'Roses are red']);
  });

  it('never reorders across a turn boundary', () => {
    const pane = 'pane-turns';
    const c = T.getCache(pane);

    // Turn 1: user1 → reply1 → turn_end.
    T.getPending(pane).push({ localMsgId: 'a', text: 'first' });
    T.foldEvent(c, userEv(pane, 2, 'first', false));
    T.foldEvent(c, textEv(pane, 3, 'm1', 'reply one'));
    T.foldEvent(c, turnEndEv(pane, 4));
    // Turn 2: reply2 streams, THEN the user2 echo lands late.
    T.foldEvent(c, deltaEv(pane, 5, 'm2', 'reply '));
    T.getPending(pane).push({ localMsgId: 'b', text: 'second' });
    T.foldEvent(c, userEv(pane, 8, 'second', false));
    T.foldEvent(c, textEv(pane, 9, 'm2', 'reply two'));

    const rows = c.events
      .filter((e) => e.type === 'user_text' || e.type === 'text')
      .map((e) => e.text);
    // 'second' anchors to ITS turn — never jumps ahead of turn 1's reply.
    expect(rows).toEqual(['first', 'reply one', 'second', 'reply two']);
  });

  it('never reorders a live user_text into seeded history', () => {
    const pane = 'pane-seed-order';
    const c = T.getCache(pane);

    // Seeded prior turn (FromReplay) — already correctly ordered.
    T.foldEvent(c, userEv(pane, SEED, 'old q', true));
    T.foldEvent(c, replayText(pane, SEED + 1, 'old answer'));
    // Live in-flight turn: reply streams, then the late user echo.
    T.foldEvent(c, deltaEv(pane, 3, 'm1', 'new '));
    T.getPending(pane).push({ localMsgId: 'z', text: 'new q' });
    T.foldEvent(c, userEv(pane, 6, 'new q', false));
    T.foldEvent(c, textEv(pane, 7, 'm1', 'new answer'));

    const rows = c.events.map((e) => e.text);
    // The live user stays inside the live turn, after the whole seed block.
    expect(rows).toEqual(['old q', 'old answer', 'new q', 'new answer']);
  });
});
