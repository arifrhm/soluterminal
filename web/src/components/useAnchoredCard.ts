import { useLayoutEffect, useState, type RefObject } from 'react';

/** Viewport position for a card opened from a small header button. The card
 *  renders in a portal because panes clip their overflow and a multi-up pane
 *  is narrower than the card; it right-aligns to the button and stays on
 *  screen while the window resizes or scrolls. */
export function useAnchoredCard(open: boolean, anchor: RefObject<HTMLElement | null>, width = 320) {
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null);
  useLayoutEffect(() => {
    if (!open || !anchor.current) return;
    const place = () => {
      const r = anchor.current!.getBoundingClientRect();
      const w = Math.min(width, window.innerWidth - 16);
      const left = Math.max(8, Math.min(r.right - w, window.innerWidth - w - 8));
      setPos({ top: r.bottom + 4, left });
    };
    place();
    window.addEventListener('resize', place);
    window.addEventListener('scroll', place, true);
    return () => {
      window.removeEventListener('resize', place);
      window.removeEventListener('scroll', place, true);
    };
  }, [open, anchor, width]);
  return pos;
}
