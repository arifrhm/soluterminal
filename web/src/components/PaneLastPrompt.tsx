import type { Dispatch, MouseEvent, SetStateAction } from 'react';
import { useStore } from '../store';

interface PaneLastPromptProps {
  prompts: string[]; // newest first
  expanded: boolean;
  setExpanded: Dispatch<SetStateAction<boolean>>;
}

// VISIBLE_ROWS is how many history rows the expanded scroll box shows
// before scrolling — kept in sync with the max-height in styles.css so
// the "scroll for N more" hint counts what's off-screen.
const VISIBLE_ROWS = 3;

export function PaneLastPrompt({ prompts, expanded, setExpanded }: PaneLastPromptProps) {
  // User-toggleable (Preferences → "Prompt history"). Gated HERE, not at the
  // mount sites, so Term and Focus panes can never disagree.
  const show = useStore((s) => s.showPromptHistory);
  const toast = useStore((s) => s.toast);
  if (!show || !prompts.length) return null;

  const hidden = prompts.length - VISIBLE_ROWS;
  const toggle = () => setExpanded((v) => !v);

  // Per-row copy — the strip is informational, but "grab that prompt again"
  // is a real need (re-ask in another pane, share it). stopPropagation so
  // the copy click doesn't also expand/collapse the strip.
  const copyPrompt = async (e: MouseEvent, p: string) => {
    e.stopPropagation();
    try {
      await navigator.clipboard.writeText(p);
      toast('Prompt copied', { kind: 'success', icon: '⧉' });
    } catch {
      toast('Copy failed — clipboard unavailable', { kind: 'error', icon: '⚠' });
    }
  };

  // One prompt row — ellipsized text + a hover-revealed copy button. The row
  // (not just the text) toggles expand/collapse to browse history.
  const entry = (p: string, idx: number, extra: string) => (
    <span key={idx} className={`pane-last-prompt-row${extra}`}>
      <span className="pane-last-prompt-text" title={p}>
        {p}
      </span>
      <button
        type="button"
        className="pane-last-prompt-copy"
        title="Copy this prompt"
        aria-label="Copy this prompt"
        onClick={(e) => void copyPrompt(e, p)}
      >⧉</button>
    </span>
  );

  const body = expanded ? (
    <div className="pane-last-prompt-body">
      <div className="pane-last-prompt-list">
        {prompts.map((p, idx) => entry(p, idx, idx > 0 ? ' older' : ''))}
      </div>
      {hidden > 0 && (
        <div className="pane-last-prompt-more" aria-hidden>
          ↓ scroll for {hidden} more
        </div>
      )}
    </div>
  ) : (
    entry(prompts[0], 0, '')
  );

  return (
    <div
      className={`pane-last-prompt ${expanded ? 'expanded' : ''}`}
      title={expanded
        ? 'Click to collapse'
        : `Click to expand · ${prompts.length} prompt${prompts.length > 1 ? 's' : ''} in history`}
      onClick={toggle}
      role="button"
      aria-expanded={expanded}
    >
      <span className="pane-last-prompt-icon" aria-hidden>
        {expanded ? '⌄' : '›'}
      </span>
      {body}
    </div>
  );
}
