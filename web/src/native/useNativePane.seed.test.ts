// Regression for the "native pane loses content after a desktop app update"
// bug. The app-update self-heal soft-reload races the daemon's auto-update
// binary swap. The reattached daemon can momentarily answer the one-shot since=0
// transcript seed with an EMPTY-but-successful 200 (seedFromTranscript returns
// ([],nil) while the on-disk transcript is briefly unresolvable and the rebuilt
// in-memory ring is still empty). The old code latched `seeded=true` on ANY
// non-throwing response, permanently consuming the seed — and since the agent's
// byte-replay then advances lastSeq>0, every later resync pulls since=lastSeq and
// never re-fetches the since=0 transcript, so the pane stayed blank/partial until
// a manual hard refresh. The fix: applySeedResult latches only on a non-empty
// response, leaving `seeded` false so es.onopen / the idle-resync re-attempt the
// seed until it lands content.
//
// The latch + fold logic is extracted into the pure applySeedResult so it's
// testable without rendering the hook (which pulls the store/cloud graph into
// React and OOM'd a prior agent under jsdom).

import { beforeEach, describe, expect, it } from 'vitest';
import { __useNativePaneTest as T } from './useNativePane';
import type { NativeEvent } from './nativeEvents';

function seedEv(paneId: string, seq: number, text: string): NativeEvent {
  return { seq, paneId, type: 'text', fromReplay: true, text } as NativeEvent;
}

beforeEach(() => {
  T.resetAll();
});

describe('applySeedResult — empty seed must not consume the one-shot seed', () => {
  it('does NOT latch `seeded` on an empty response (daemon mid-swap window)', () => {
    const c = T.getCache('p1');
    expect(c.seeded).toBe(false);

    const changed = T.applySeedResult(c, []);

    expect(changed).toBe(false); // nothing folded → caller won't publish
    expect(c.seeded).toBe(false); // CRUCIAL: seed stays un-consumed → retried
    expect(c.events.length).toBe(0);
  });

  it('a later non-empty seed still lands after an empty one (self-heal, no refresh)', () => {
    const c = T.getCache('p2');

    // First attempt races the swap window → empty → seed NOT consumed.
    T.applySeedResult(c, []);
    expect(c.seeded).toBe(false);

    // The idle-resync re-attempts once the daemon transcript resolves → content.
    const changed = T.applySeedResult(c, [
      seedEv('p2', 1, 'hello'),
      seedEv('p2', 2, 'world'),
    ]);

    expect(changed).toBe(true);
    expect(c.seeded).toBe(true); // now latched — re-attempts stop
    expect(c.events.map((e) => e.text)).toEqual(['hello', 'world']);
  });

  it('latches on the first non-empty response (normal healthy path)', () => {
    const c = T.getCache('p3');
    const changed = T.applySeedResult(c, [seedEv('p3', 1, 'only')]);
    expect(changed).toBe(true);
    expect(c.seeded).toBe(true);
    expect(c.events.length).toBe(1);
  });
});
