// Unit tests for the diffLines memo cache + cost cap (the FIX #3 perf work).
//
// The cache must be TRANSPARENT: identical inputs return a deep-equal result to a
// fresh diff (the existing diffPreview.test.ts already pins the LCS output; here we
// guard that the cache doesn't change it, and that the same input twice is equal).
// The size cap must bound a pathological edit: past the threshold diffLines falls
// back to a coarse del-then-add diff (correct shape, no LCS context).

import { describe, it, expect } from 'vitest';
import { diffLines } from './NativeEventViews';

describe('diffLines cache (transparent memo)', () => {
  it('returns a deep-equal result on the same input twice', () => {
    const before = 'keep\nold\ntail';
    const after = 'keep\nnew\ntail';
    const first = diffLines(before, after);
    const second = diffLines(before, after);
    // The cache may return the SAME ref the second time (a hit), but the contract
    // we care about is value-equality vs a fresh diff — identical inputs, identical
    // output. Compare against a freshly-derived expectation too.
    expect(second).toEqual(first);
    expect(first).toEqual([
      { kind: 'ctx', text: 'keep' },
      { kind: 'del', text: 'old' },
      { kind: 'add', text: 'new' },
      { kind: 'ctx', text: 'tail' },
    ]);
  });

  it('keeps distinct inputs distinct (no key collision)', () => {
    const a = diffLines('a\nb', 'a\nc');
    const b = diffLines('a', 'b\nc'); // a different (before, after) pair
    expect(a).not.toEqual(b);
    // The first still computes correctly (shared 'a' context, b→c divergence).
    expect(a).toEqual([
      { kind: 'ctx', text: 'a' },
      { kind: 'del', text: 'b' },
      { kind: 'add', text: 'c' },
    ]);
  });

  it('does not collide when a space straddles the before/after boundary', () => {
    // The regression guard for the cache key. ('a','b c') and ('a b','c') BOTH
    // join to 'a b c' under a naive `before + " " + after` key — a real collision
    // that would make the second call return the FIRST call's cached diff. Call the
    // colliding pair in order: the first caches, the second must still return ITS
    // OWN diff. (Single-line strings with spaces are common in real code edits.)
    const first = diffLines('a', 'b c');
    const second = diffLines('a b', 'c');
    expect(first).toEqual([
      { kind: 'del', text: 'a' },
      { kind: 'add', text: 'b c' },
    ]);
    expect(second).toEqual([
      { kind: 'del', text: 'a b' },
      { kind: 'add', text: 'c' },
    ]);
  });
});

describe('diffLines cost cap (pathological size fallback)', () => {
  it('falls back to del-then-add for a huge edit (over the n+m line cap)', () => {
    // 5000 + 5000 = 10000 lines > the 8000 line cap → no LCS, coarse diff. Use two
    // line sets that SHARE all content; the LCS path would emit every line as ctx,
    // the fallback emits every before line as del then every after line as add.
    const n = 5000;
    const before = Array.from({ length: n }, (_, i) => `line ${i}`).join('\n');
    const after = Array.from({ length: n }, (_, i) => `line ${i}`).join('\n'); // identical content
    const out = diffLines(before, after);
    // Fallback signature: NO context lines at all (the LCS path would have made
    // every line ctx since the content is identical).
    expect(out.some((l) => l.kind === 'ctx')).toBe(false);
    expect(out.filter((l) => l.kind === 'del')).toHaveLength(n);
    expect(out.filter((l) => l.kind === 'add')).toHaveLength(n);
    // Order: all dels first, then all adds.
    expect(out[0]).toEqual({ kind: 'del', text: 'line 0' });
    expect(out[n]).toEqual({ kind: 'add', text: 'line 0' });
  });

  it('falls back via the n·m CELL guard even when under the line cap', () => {
    // 2100 × 2100: n+m = 4200 (UNDER the 8000 line cap) but n·m = 4.41M (> the 4M
    // cell cap). This isolates the cell guard — the case a realistic large square
    // edit hits. Identical content, so any ctx line proves the LCS ran; the
    // fallback emits zero ctx.
    const n = 2100;
    const before = Array.from({ length: n }, (_, i) => `row ${i}`).join('\n');
    const after = Array.from({ length: n }, (_, i) => `row ${i}`).join('\n');
    const out = diffLines(before, after);
    expect(out.some((l) => l.kind === 'ctx')).toBe(false);
    expect(out.filter((l) => l.kind === 'del')).toHaveLength(n);
    expect(out.filter((l) => l.kind === 'add')).toHaveLength(n);
  });

  it('still uses the LCS (context detection) for a normal-sized edit', () => {
    const out = diffLines('keep\nold', 'keep\nnew');
    // Below the cap → LCS path → the shared 'keep' line is context, not del+add.
    expect(out).toContainEqual({ kind: 'ctx', text: 'keep' });
  });
});
