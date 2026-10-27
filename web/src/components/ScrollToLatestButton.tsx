// One "jump to latest" affordance for every scrollable transcript surface in
// the web app — the terminal reader chip, the native/Focus pane, and the Aria
// chat. Single markup + single CSS class (.scroll-latest-chip) so the pill
// looks and behaves identically everywhere: bottom-centered, fade-in on appear,
// INSTANT scroll on click (never an animation the user has to watch), and an
// optional "N new" count with a relative-time suffix.

// Distance (px) from the bottom of a DOM scroll container within which the user
// still counts as "stuck to bottom" (auto-follow stays on). Shared by the
// native pane and the Aria panel so both unstick at the same sensitivity. The
// terminal pane has its own line-based heuristic (xterm baseY) and doesn't use
// this px slop.
export const STICK_BOTTOM_SLOP = 48;

export function relativeTimeLabel(deltaMs: number): string {
  if (deltaMs < 2000) return 'just now';
  const s = Math.floor(deltaMs / 1000);
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

interface ScrollToLatestButtonProps {
  visible: boolean;
  onClick: () => void;
  // New items that arrived below the fold while scrolled up. 0 ⇒ generic label.
  count?: number;
  // Wall-clock ms of the last output, for a "Ns ago" suffix (only with count>0).
  lastOutputMs?: number;
  title?: string;
}

export function ScrollToLatestButton({
  visible,
  onClick,
  count = 0,
  lastOutputMs = 0,
  title,
}: ScrollToLatestButtonProps) {
  if (!visible) return null;
  const shown = count >= 999 ? '999+' : count;
  return (
    <button
      type="button"
      className="scroll-latest-chip"
      onClick={onClick}
      aria-label="Scroll to latest"
      title={title ?? 'Return to latest'}
    >
      <span className="scroll-latest-arrow" aria-hidden>↓</span>
      <span className="scroll-latest-label">
        {count > 0 ? `${shown} new` : 'Jump to latest'}
      </span>
      {lastOutputMs > 0 && count > 0 && (
        <span className="scroll-latest-time">
          {relativeTimeLabel(Date.now() - lastOutputMs)}
        </span>
      )}
    </button>
  );
}
