// Behavioural tests for useNativeFind — the native/Focus renderer's in-pane find.
// Focus of these tests is the product requirement that a fresh query lands on the
// MOST RECENT match (last in document order), plus match counting, next/prev
// navigation with wraparound, and empty/zero-match edges. The CSS Custom Highlight
// API isn't present in jsdom, so highlightsSupported() is false and painting is a
// no-op here — the range walk, count, active-index (recency), and navigation logic
// all still run and are what we assert.

import { render, act } from '@testing-library/react';
import { useRef } from 'react';
import { beforeAll, describe, expect, it } from 'vitest';
import { useNativeFind } from './useNativeFind';

// jsdom doesn't implement scrollIntoView; the hook scrolls the active match, so
// stub it to a no-op (we're not asserting scroll position, only match state).
beforeAll(() => {
  if (!(Element.prototype as unknown as { scrollIntoView?: unknown }).scrollIntoView) {
    Element.prototype.scrollIntoView = () => {};
  }
});

type FindApi = ReturnType<typeof useNativeFind>;

// The harness renders a transcript containing `text` and exposes the latest hook
// return via a shared holder so the test can drive setQuery/next/prev and read
// results after React flushes.
function makeHarness() {
  const holder: { api: FindApi | null } = { api: null };
  function Harness({ text }: { text: string }) {
    const transcriptRef = useRef<HTMLDivElement>(null);
    holder.api = useNativeFind({ paneId: 'p1', transcriptRef });
    return (
      <div className="native-transcript" ref={transcriptRef}>
        {/* Multiple children so matches span several text nodes, exercising the
            TreeWalker document-order collection. */}
        {text.split('|').map((chunk, i) => (
          <p key={i}>{chunk}</p>
        ))}
      </div>
    );
  }
  return { holder, Harness };
}

function openFind() {
  act(() => {
    window.dispatchEvent(new CustomEvent('solu:pane-find-open', { detail: { paneId: 'p1' } }));
  });
}

async function type(holder: { api: FindApi | null }, q: string) {
  await act(async () => {
    holder.api!.setQuery(q);
    await Promise.resolve();
  });
}

describe('useNativeFind recency + counting', () => {
  it('lands on the MOST RECENT (last) match on a fresh query', async () => {
    // 'alpha' appears 3× across three paragraphs; the last one is the newest.
    const { holder, Harness } = makeHarness();
    render(<Harness text="alpha one|alpha two|gamma alpha three" />);
    openFind();
    await type(holder, 'alpha');
    expect(holder.api!.results.total).toBe(3);
    // current === total means the active match is the last/newest one.
    expect(holder.api!.results.current).toBe(3);
  });

  it('next() advances (wrapping newest→oldest) and prev() reverses', async () => {
    const { holder, Harness } = makeHarness();
    render(<Harness text="x hit|y hit|z hit" />);
    openFind();
    await type(holder, 'hit');
    expect(holder.api!.results).toEqual({ current: 3, total: 3 });
    // From the newest (3/3), next wraps to the oldest (1/3).
    act(() => holder.api!.next());
    expect(holder.api!.results).toEqual({ current: 1, total: 3 });
    act(() => holder.api!.next());
    expect(holder.api!.results).toEqual({ current: 2, total: 3 });
    // prev walks back.
    act(() => holder.api!.prev());
    expect(holder.api!.results).toEqual({ current: 1, total: 3 });
    // prev from the oldest wraps to the newest.
    act(() => holder.api!.prev());
    expect(holder.api!.results).toEqual({ current: 3, total: 3 });
  });

  it('is case-insensitive and counts every occurrence, including repeats in one node', async () => {
    const { holder, Harness } = makeHarness();
    render(<Harness text="Foo foo FOO|foofoo" />);
    openFind();
    await type(holder, 'foo');
    // 3 in the first paragraph (Foo, foo, FOO) + 2 in 'foofoo' = 5.
    expect(holder.api!.results.total).toBe(5);
    expect(holder.api!.results.current).toBe(5);
  });

  it('reports zero matches for an absent query and resets on clear', async () => {
    const { holder, Harness } = makeHarness();
    render(<Harness text="alpha|beta" />);
    openFind();
    await type(holder, 'zzz');
    expect(holder.api!.results).toEqual({ current: 0, total: 0 });
    // Re-query to a present term, then clear back to empty.
    await type(holder, 'beta');
    expect(holder.api!.results).toEqual({ current: 1, total: 1 });
    await type(holder, '');
    expect(holder.api!.results).toEqual({ current: 0, total: 0 });
  });

  it('opens and drives find from a cross-pane search reveal event', async () => {
    const { holder, Harness } = makeHarness();
    render(<Harness text="needle here|and needle again" />);
    act(() => {
      window.dispatchEvent(
        new CustomEvent('solu:pane-search-reveal', { detail: { paneId: 'p1', query: 'needle' } }),
      );
    });
    // Let the open + query-driven recompute effect flush.
    await act(async () => {
      await Promise.resolve();
    });
    expect(holder.api!.open).toBe(true);
    expect(holder.api!.query).toBe('needle');
    expect(holder.api!.results.total).toBe(2);
    expect(holder.api!.results.current).toBe(2);
  });

  it('ignores find events addressed to a different pane', async () => {
    const { holder, Harness } = makeHarness();
    render(<Harness text="alpha" />);
    act(() => {
      window.dispatchEvent(new CustomEvent('solu:pane-find-open', { detail: { paneId: 'other' } }));
    });
    expect(holder.api!.open).toBe(false);
  });
});
