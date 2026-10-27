import { useLayoutEffect, useState, type RefObject } from 'react';

// Keep cursor-anchored context menus fully inside the viewport. A menu opened
// near the bottom edge flips to open UPWARD from the cursor (its bottom edge
// at the click point); one near the right edge opens leftward. Falls back to
// clamping when even the flipped position would poke past the top/left.
//
// Usage: render the menu at the returned coords with position:fixed and pass
// the same ref used for outside-click detection. Measurement happens in a
// layout effect (post-mount, pre-paint), so the correction never flashes.
const EDGE_MARGIN = 8;

export function useClampedMenuPosition(
  ref: RefObject<HTMLElement | null>,
  pos: { x: number; y: number } | null,
): { left: number; top: number } {
  const [adjusted, setAdjusted] = useState({ left: pos?.x ?? 0, top: pos?.y ?? 0 });

  useLayoutEffect(() => {
    if (!pos) return;
    let { x: left, y: top } = pos;
    const el = ref.current;
    if (el) {
      // Measure at the viewport origin: a fixed element positioned near the
      // right edge shrink-to-fits against the viewport, so measuring it in
      // place under-reports its natural width and the flip comes up short.
      const prev = { left: el.style.left, top: el.style.top };
      el.style.left = '0px';
      el.style.top = '0px';
      const r = el.getBoundingClientRect();
      el.style.left = prev.left;
      el.style.top = prev.top;
      if (top + r.height > window.innerHeight - EDGE_MARGIN) {
        top = Math.max(EDGE_MARGIN, pos.y - r.height);
      }
      if (left + r.width > window.innerWidth - EDGE_MARGIN) {
        left = Math.max(EDGE_MARGIN, pos.x - r.width);
      }
    }
    setAdjusted({ left, top });
  }, [ref, pos?.x, pos?.y]);

  // While the layout effect hasn't run for a fresh open yet, render at the
  // raw cursor position — the correction lands before the browser paints.
  return pos ? adjusted : { left: 0, top: 0 };
}
