import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type MutableRefObject,
} from 'react';

// In-pane find for the native/Focus renderer — the counterpart to usePaneFind
// (which drives the terminal renderer via xterm's SearchAddon). There is no
// xterm here: the transcript is a DOM tree of rendered React views, so we search
// it by walking text nodes under the transcript root, build Range objects for
// each match, and paint them with the CSS Custom Highlight API (non-destructive:
// no DOM mutation, so it never fights React reconciliation). Matches that live
// inside folded groups are made visible first via NativeFindContext.active,
// which force-opens every foldable view while a query is present.
//
// Recency priority (per product requirement): a fresh query lands on the MOST
// RECENT match — the last one in document order, nearest the bottom — and scrolls
// it into view, matching the terminal renderer's findPrevious-on-type behaviour.
//
// Wiring parity with usePaneFind: both listen for `solu:pane-find-open` (Ctrl+F /
// palette) and `solu:pane-search-reveal` (cross-pane search reveal).
//
// Known boundaries (documented, intentional): matches resolve WITHIN a single
// text node — a query spanning an inline element boundary (e.g. across a <code>
// span) won't match — and Focus mode omits successful tool output from the DOM by
// design, so that text isn't searchable here (it is in the terminal renderer).

interface UseNativeFindArgs {
  paneId: string;
  // The transcript root (.native-transcript) whose text we search + highlight.
  // The active match is scrolled into view via scrollIntoView, which walks up to
  // the nearest scrollable ancestor (.native-body) on its own — no body ref needed.
  transcriptRef: MutableRefObject<HTMLElement | null>;
  // Composer textarea to return focus to on close (optional; may be unmounted).
  composerRef?: MutableRefObject<HTMLTextAreaElement | null>;
  // Cheap render-side signal for transcript growth. MutationObserver remains
  // the primary live path; this also covers test/host environments without it.
  contentVersion?: number;
}

// Highlight registry names. NOTE: the CSS Custom Highlight registry is global and
// keyed by name, and `::highlight()` rules need a static name, so all native panes
// share these two registers. We therefore enforce a single active native find at a
// time (see `solu:native-find-opened` below): opening find in one pane closes it in
// any other, so two panes never fight over the shared registers.
const HL_ALL = 'pane-find';
const HL_ACTIVE = 'pane-find-active';
// Broadcast so exactly one native find bar is open at a time (shared registers).
const NATIVE_FIND_OPENED = 'solu:native-find-opened';

function highlightsSupported(): boolean {
  return (
    typeof CSS !== 'undefined' &&
    !!(CSS as unknown as { highlights?: unknown }).highlights &&
    typeof Highlight !== 'undefined' &&
    typeof Range !== 'undefined'
  );
}

export function useNativeFind({ paneId, transcriptRef, composerRef, contentVersion }: UseNativeFindArgs) {
  const [open, setOpen] = useState(false);
  const [query, setQueryState] = useState('');
  const [results, setResults] = useState<{ current: number; total: number }>({ current: 0, total: 0 });
  const inputRef = useRef<HTMLInputElement | null>(null);

  // Match ranges for the current query, in document order (oldest→newest,
  // top→bottom). activeRef indexes into it; -1 when there are no matches.
  const rangesRef = useRef<Range[]>([]);
  const activeRef = useRef(-1);
  // Previous query, so the content-driven re-walk can tell a genuine query change
  // (→ re-land on the newest match) apart from a streaming tick (→ keep position).
  const prevQueryRef = useRef('');
  // Mirror of `open` for use inside window-event listeners without stale closures.
  const openRef = useRef(false);
  useEffect(() => {
    openRef.current = open;
  }, [open]);

  const clearHighlights = useCallback(() => {
    if (!highlightsSupported()) return;
    CSS.highlights.delete(HL_ALL);
    CSS.highlights.delete(HL_ACTIVE);
  }, []);

  // Push rangesRef into the two highlight registers (all matches + the active one).
  const paint = useCallback(() => {
    if (!highlightsSupported()) return;
    const ranges = rangesRef.current;
    if (ranges.length === 0) {
      CSS.highlights.delete(HL_ALL);
      CSS.highlights.delete(HL_ACTIVE);
      return;
    }
    const all = new Highlight();
    for (const r of ranges) all.add(r);
    CSS.highlights.set(HL_ALL, all);
    const idx = activeRef.current;
    if (idx >= 0 && idx < ranges.length) {
      const act = new Highlight();
      act.add(ranges[idx]);
      // Higher priority so the active-match style wins where it overlaps the
      // all-matches highlight, regardless of registry insertion order.
      (act as unknown as { priority: number }).priority = 1;
      CSS.highlights.set(HL_ACTIVE, act);
    } else {
      CSS.highlights.delete(HL_ACTIVE);
    }
  }, []);

  const scrollActiveIntoView = useCallback(() => {
    const idx = activeRef.current;
    const ranges = rangesRef.current;
    if (idx < 0 || idx >= ranges.length) return;
    const node: Node = ranges[idx].startContainer;
    const el: Element | null =
      node.nodeType === Node.TEXT_NODE ? node.parentElement : (node as Element);
    el?.scrollIntoView({ block: 'center', behavior: 'smooth' });
  }, []);

  // Walk the transcript's text nodes and (re)build the match ranges. `landNewest`
  // resets the active match to the most recent one and scrolls to it — used on a
  // fresh/changed query; a streaming re-walk keeps the user's current position.
  // The needle is trimmed to match `active` and the daemon's cross-pane search
  // (both TrimSpace), so a whitespace-only query is a no-op everywhere.
  const recompute = useCallback(
    (q: string, landNewest: boolean) => {
      const root = transcriptRef.current;
      const needle = q.trim().toLowerCase();
      if (!root || !needle) {
        rangesRef.current = [];
        activeRef.current = -1;
        clearHighlights();
        setResults({ current: 0, total: 0 });
        return;
      }
      const ranges: Range[] = [];
      const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
        acceptNode(node) {
          const t = node.nodeValue;
          if (!t || !t.trim()) return NodeFilter.FILTER_REJECT;
          return NodeFilter.FILTER_ACCEPT;
        },
      });
      let n: Node | null;
      while ((n = walker.nextNode())) {
        const text = n.nodeValue as string;
        const lower = text.toLowerCase();
        let from = 0;
        for (;;) {
          const i = lower.indexOf(needle, from);
          if (i === -1) break;
          const r = document.createRange();
          r.setStart(n, i);
          r.setEnd(n, i + needle.length);
          ranges.push(r);
          from = i + needle.length;
        }
      }
      rangesRef.current = ranges;
      if (ranges.length === 0) {
        activeRef.current = -1;
      } else if (landNewest || activeRef.current < 0 || activeRef.current >= ranges.length) {
        // Recency priority: newest match = last in document order.
        activeRef.current = ranges.length - 1;
      }
      paint();
      setResults({
        current: activeRef.current >= 0 ? activeRef.current + 1 : 0,
        total: ranges.length,
      });
      if (landNewest) scrollActiveIntoView();
    },
    [transcriptRef, clearHighlights, paint, scrollActiveIntoView],
  );

  // Re-walk on a query change or on open. useEffect (not layout) runs after the
  // force-open render commits, so folded matches are in the DOM by the time we
  // walk. `landNewest` only when the query text actually changed.
  useEffect(() => {
    if (!open) return;
    const landNewest = prevQueryRef.current !== query;
    prevQueryRef.current = query;
    recompute(query, landNewest);
  }, [open, query, recompute]);

  useEffect(() => {
    if (!open || !query.trim()) return;
    recompute(query, false);
  }, [contentVersion, open, query, recompute]);

  // While find is active, re-walk whenever the transcript DOM changes — streaming
  // deltas, a late tool_result filling an earlier row, a group expanding — so the
  // highlights and count never go stale. rAF-coalesced to at most one walk/frame.
  // `landNewest=false` keeps the user's current match; new matches append at the
  // tail and don't shift existing indices.
  useEffect(() => {
    const root = transcriptRef.current;
    if (!open || !query.trim() || !root || typeof MutationObserver === 'undefined') return;
    let raf = 0;
    const obs = new MutationObserver(() => {
      if (raf) return;
      raf = requestAnimationFrame(() => {
        raf = 0;
        recompute(query, false);
      });
    });
    obs.observe(root, { childList: true, characterData: true, subtree: true });
    return () => {
      obs.disconnect();
      if (raf) cancelAnimationFrame(raf);
    };
  }, [open, query, transcriptRef, recompute]);

  const setQuery = useCallback((value: string) => setQueryState(value), []);

  const step = useCallback(
    (delta: number) => {
      const ranges = rangesRef.current;
      if (ranges.length === 0) return;
      activeRef.current = (activeRef.current + delta + ranges.length) % ranges.length;
      paint();
      scrollActiveIntoView();
      setResults({ current: activeRef.current + 1, total: ranges.length });
    },
    [paint, scrollActiveIntoView],
  );

  const next = useCallback(() => step(1), [step]);
  const prev = useCallback(() => step(-1), [step]);

  // refocus=false when we're auto-closing because another pane took over find —
  // that pane is grabbing focus, so we must not yank it back to our composer.
  const close = useCallback(
    (refocus = true) => {
      setOpen(false);
      setQueryState('');
      prevQueryRef.current = '';
      rangesRef.current = [];
      activeRef.current = -1;
      clearHighlights();
      if (refocus) composerRef?.current?.focus();
    },
    [clearHighlights, composerRef],
  );

  // Open/reveal listeners — same events the terminal renderer consumes, so Ctrl+F
  // and cross-pane search reveal work identically in Focus mode. On open we
  // broadcast NATIVE_FIND_OPENED so any other native pane's find bar closes,
  // keeping the shared highlight registers owned by exactly one pane.
  useEffect(() => {
    const announceOpen = () => {
      window.dispatchEvent(new CustomEvent(NATIVE_FIND_OPENED, { detail: { paneId } }));
    };
    const focusInput = () => {
      requestAnimationFrame(() => {
        const el = inputRef.current;
        if (!el) return;
        el.focus();
        el.select();
      });
    };
    const onFindOpen = (ev: Event) => {
      const detail = (ev as CustomEvent).detail as { paneId?: string } | undefined;
      if (!detail || detail.paneId !== paneId) return;
      setOpen(true);
      announceOpen();
      focusInput();
    };
    const onSearchReveal = (ev: Event) => {
      const detail = (ev as CustomEvent).detail as { paneId?: string; query?: string } | undefined;
      if (!detail || detail.paneId !== paneId || !detail.query) return;
      setOpen(true);
      setQueryState(detail.query);
      announceOpen();
      focusInput();
    };
    // Another native pane opened find → close ours (only if we're actually open,
    // else we'd delete the new owner's highlight registers).
    const onOtherOpened = (ev: Event) => {
      const detail = (ev as CustomEvent).detail as { paneId?: string } | undefined;
      if (!detail || detail.paneId === paneId) return;
      if (openRef.current) close(false);
    };
    window.addEventListener('solu:pane-find-open', onFindOpen as EventListener);
    window.addEventListener('solu:pane-search-reveal', onSearchReveal as EventListener);
    window.addEventListener(NATIVE_FIND_OPENED, onOtherOpened as EventListener);
    return () => {
      window.removeEventListener('solu:pane-find-open', onFindOpen as EventListener);
      window.removeEventListener('solu:pane-search-reveal', onSearchReveal as EventListener);
      window.removeEventListener(NATIVE_FIND_OPENED, onOtherOpened as EventListener);
    };
  }, [paneId, close]);

  // Clear the shared highlight registers if this pane unmounts while find is open
  // (e.g. workspace switch, pane close) so stale ranges don't linger. Guarded on
  // our own ranges so we never wipe another pane's paint.
  useEffect(() => {
    return () => {
      if (rangesRef.current.length > 0) clearHighlights();
    };
  }, [clearHighlights]);

  return {
    open,
    query,
    setQuery,
    results,
    next,
    prev,
    close,
    inputRef,
    // Derived: transcript force-opens folded views only once a query is present.
    active: open && query.trim().length > 0,
    // Unused by the bar but handy for callers that want to know if the platform
    // can paint highlights (count + scroll still work without it).
    highlightsSupported: highlightsSupported(),
  };
}
