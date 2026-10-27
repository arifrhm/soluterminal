import { useCallback, useRef } from 'react';
import type { MouseEvent as ReactMouseEvent } from 'react';

// Hover-to-select for keyboard-driven lists (the command palette).
//
// When rows re-render under a pointer that is sitting still — the list filters
// as you type — the browser re-runs hover for the new row under the cursor:
// mouseenter/mouseover fire, and Chromium can also send a mousemove at the
// same position. If hover moved the selection on those, Enter would run that
// row instead of the best match you were typing towards.
//
// `track` must see every mousemove over the surface (attach it to a wrapper
// so it runs after the row's handler in the bubble phase). `moved` answers
// "did the pointer really move since the last event?" for a row's
// onMouseMove. The first event after opening never counts: it can be the
// browser reporting a pointer that was already resting there.
export function usePointerMoved() {
  const last = useRef<{ x: number; y: number } | null>(null);

  const track = useCallback((e: ReactMouseEvent) => {
    last.current = { x: e.screenX, y: e.screenY };
  }, []);

  const moved = useCallback((e: ReactMouseEvent) => {
    const prev = last.current;
    return !!prev && (prev.x !== e.screenX || prev.y !== e.screenY);
  }, []);

  const reset = useCallback(() => {
    last.current = null;
  }, []);

  return { track, moved, reset };
}
