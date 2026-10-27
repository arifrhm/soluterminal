import type { MutableRefObject } from 'react';

// Shared search-decoration palette for both the cross-pane-search
// reveal (CommandPalette -> findNext) and the in-pane find overlay.
export const FIND_DECORATIONS = {
  matchBackground: '#4e3b12',
  matchBorder: '#ffb454',
  matchOverviewRuler: '#ffb454',
  activeMatchBackground: '#7a5a1a',
  activeMatchBorder: '#ffd080',
  activeMatchColorOverviewRuler: '#ffd080',
} as const;

interface PaneFindBarProps {
  inputRef: MutableRefObject<HTMLInputElement | null>;
  query: string;
  setQuery: (v: string) => void;
  results: { current: number; total: number };
  onNext: () => void;
  onPrev: () => void;
  onClose: () => void;
}

// In-pane find overlay. Keyboard-first:
// Enter -> next match, Shift+Enter -> previous, Esc -> close.
export function PaneFindBar({
  inputRef,
  query,
  setQuery,
  results,
  onNext,
  onPrev,
  onClose,
}: PaneFindBarProps) {
  return (
    <div className="pane-find-bar" role="search">
      <input
        ref={inputRef}
        type="text"
        className="pane-find-input"
        placeholder="Find in pane"
        value={query}
        autoComplete="off"
        spellCheck={false}
        onChange={(e) => setQuery(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Escape') { e.preventDefault(); onClose(); return; }
          if (e.key === 'Enter') {
            e.preventDefault();
            if (e.shiftKey) onPrev(); else onNext();
            return;
          }
        }}
      />
      {query && (
        <span className="pane-find-count" aria-live="polite">
          {results.total === 0 ? 'no matches' : `${results.current}/${results.total}`}
        </span>
      )}
      <button
        type="button"
        className="pane-find-btn"
        onClick={onPrev}
        title="Previous match (Shift+Enter)"
        aria-label="Previous match"
        disabled={!query || results.total === 0}
      >↑</button>
      <button
        type="button"
        className="pane-find-btn"
        onClick={onNext}
        title="Next match (Enter)"
        aria-label="Next match"
        disabled={!query || results.total === 0}
      >↓</button>
      <button
        type="button"
        className="pane-find-btn pane-find-close"
        onClick={onClose}
        title="Close (Esc)"
        aria-label="Close find"
      >×</button>
    </div>
  );
}
