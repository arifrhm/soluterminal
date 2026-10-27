import { useCallback, useEffect, useRef, useState, type MutableRefObject } from 'react';
import type { SearchAddon } from '@xterm/addon-search';
import type { Terminal } from '@xterm/xterm';
import { FIND_DECORATIONS } from './PaneFindBar';

interface SearchResultsEvent {
  resultIndex?: number;
  resultCount?: number;
}

interface UsePaneFindArgs {
  paneId: string;
  searchAddonRef: MutableRefObject<SearchAddon | null>;
  termRef: MutableRefObject<Terminal | null>;
  actionGuard?: () => boolean;
  captureGuard?: () => () => boolean;
}

const FIND_OPTIONS = {
  caseSensitive: false,
  regex: false,
  decorations: FIND_DECORATIONS,
} as const;

export function usePaneFind({ paneId, searchAddonRef, termRef, actionGuard, captureGuard }: UsePaneFindArgs) {
  const guards=useRef({actionGuard,captureGuard});guards.current={actionGuard,captureGuard};
  const visible=()=>guards.current.actionGuard?.() ?? true;
  const capture=()=>guards.current.captureGuard?.() ?? visible;
  const [open, setOpen] = useState(false);
  const [query, setQueryState] = useState('');
  const [results, setResults] = useState<{ current: number; total: number }>({ current: 0, total: 0 });
  const inputRef = useRef<HTMLInputElement | null>(null);

  const setQuery = useCallback((value: string) => {
    if(!visible())return;
    setQueryState(value);
    // Recency priority: land on the MOST RECENT match first, matching the
    // native/Focus renderer (useNativeFind). Clearing any prior selection and
    // calling findPrevious makes the addon search backward from the bottom of the
    // buffer, so it lands on the LAST occurrence — and it does so on every
    // keystroke, so a growing query never marches the cursor up toward older hits.
    // An empty query clears decorations; the addon's onDidChangeResults still
    // fires with a 0-count result, so the match-count UI updates automatically.
    termRef.current?.clearSelection();
    searchAddonRef.current?.findPrevious(value, FIND_OPTIONS);
  }, [searchAddonRef, termRef]);

  const next = useCallback(() => {
    if(!visible())return;
    searchAddonRef.current?.findNext(query, FIND_OPTIONS);
  }, [query, searchAddonRef]);

  const prev = useCallback(() => {
    if(!visible())return;
    searchAddonRef.current?.findPrevious(query, FIND_OPTIONS);
  }, [query, searchAddonRef]);

  const close = useCallback(() => {
    if(!visible())return;
    setOpen(false);
    // Clear highlights on close. Empty findNext is the addon's official
    // reset path, then return focus to the terminal for typing.
    searchAddonRef.current?.findNext('', { decorations: FIND_DECORATIONS });
    termRef.current?.focus();
  }, [searchAddonRef, termRef]);

  const handleResults = useCallback((result: SearchResultsEvent) => {
    if(!visible())return;
    const total = Math.max(0, result.resultCount ?? 0);
    const current = total > 0 ? Math.max(1, (result.resultIndex ?? 0) + 1) : 0;
    setResults({ current, total });
  }, []);

  useEffect(() => {
    let revealTimer: number | null = null;

    // Cross-pane search -> reveal: the palette dispatches this event after
    // revealPane settles. We wait briefly so WebSocket replay has a chance
    // to land in xterm before searching its buffer.
    const onSearchReveal = (ev: Event) => {
      const detail = (ev as CustomEvent).detail as { paneId?: string; query?: string } | undefined;
      if (!detail || detail.paneId !== paneId || !detail.query || !visible()) return;
      const valid=capture();
      if (revealTimer !== null) window.clearTimeout(revealTimer);
      revealTimer = window.setTimeout(() => {
        if(!valid())return;
        // Recency priority (parity with setQuery): land on the latest match.
        searchAddonRef.current?.findPrevious(detail.query!, FIND_OPTIONS);
      }, 60);
    };

    const onFindOpen = (ev: Event) => {
      const detail = (ev as CustomEvent).detail as { paneId?: string } | undefined;
      if (!detail || detail.paneId !== paneId || !visible()) return;
      const valid=capture();
      setOpen(true);
      // Focus + select-all on next frame so the PaneFindBar input exists.
      requestAnimationFrame(() => {
        if(!valid())return;
        const el = inputRef.current;
        if (!el) return;
        el.focus();
        el.select();
      });
    };

    window.addEventListener('solu:pane-search-reveal', onSearchReveal as EventListener);
    window.addEventListener('solu:pane-find-open', onFindOpen as EventListener);
    return () => {
      if (revealTimer !== null) window.clearTimeout(revealTimer);
      window.removeEventListener('solu:pane-search-reveal', onSearchReveal as EventListener);
      window.removeEventListener('solu:pane-find-open', onFindOpen as EventListener);
    };
  }, [paneId, searchAddonRef]);

  return {
    open,
    inputRef,
    query,
    setQuery,
    results,
    next,
    prev,
    close,
    handleResults,
  };
}
