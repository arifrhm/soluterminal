import { useEffect, useRef, useState } from 'react';

interface Props {
  // Returns the scrollable element to track. Called lazily (inside
  // effects) because on mount the xterm-viewport DOM doesn't exist
  // yet — xterm creates it during term.open().
  getViewport: () => HTMLElement | null | undefined;
  // When true, the pane is still settling after mount (replay →
  // Claude redraw). We skip rendering during this window so the
  // thumb doesn't flash around while xterm's scrollHeight is
  // oscillating.
  hidden?: boolean;
}

// Haptic tap — Android honours navigator.vibrate, iOS ignores it.
// Wrap so it's cheap to call from anywhere.
function haptic(ms = 10) {
  try {
    if (typeof navigator !== 'undefined' && typeof navigator.vibrate === 'function') {
      navigator.vibrate(ms);
    }
  } catch { /* ignore */ }
}

// Always-visible custom scrollbar for mobile. Native mobile browser
// scrollbars auto-fade and can't be styled reliably, so we draw our
// own synced to the underlying element's scrollTop.
//
// Intentionally nicer than the platform default:
//   - Drag the thumb to jump anywhere (native mobile bars don't
//     support thumb drag — they're purely indicators).
//   - Double-tap the thumb = jump to bottom (mobile muscle memory
//     for "show me latest output").
//   - Position bubble while dragging shows a rough % indicator.
//   - Fat invisible hit area around the thumb so your thumb
//     doesn't need pixel accuracy.
//   - Thumb pulses when new content arrives while you're scrolled
//     away from the bottom — signals "unread below".
//   - Brief haptic on drag start on Android.
export function CustomScrollbar({ getViewport, hidden }: Props) {
  const [state, setState] = useState({
    scrollTop: 0,
    scrollHeight: 0,
    clientHeight: 0,
  });
  const [dragging, setDragging] = useState(false);
  // Sticky "has overflow" flag with hysteresis. Immediate on
  // becoming scrollable (scrollHeight > clientHeight), delayed
  // clear when scrollHeight dips back — so Claude's alt-screen
  // redraw (which transiently makes scrollHeight == clientHeight
  // for a frame or two) doesn't flash the thumb between active
  // and inactive states.
  const [stickyOverflow, setStickyOverflow] = useState(false);
  const hostRef = useRef<HTMLDivElement | null>(null);
  const lastTapRef = useRef(0);
  const rafRef = useRef<number | null>(null);
  const clearOverflowTimerRef = useRef<number | null>(null);

  // Attach listeners once the viewport is available. xterm creates
  // the .xterm-viewport DOM element asynchronously, so poll briefly.
  useEffect(() => {
    let cleanup: (() => void) | null = null;
    let attempts = 0;
    // Track the pending retry timer so it can be cancelled on
    // effect teardown. Without this, pane switches leak pending
    // tryAttach timers — each one attaches a fresh set of scroll
    // listeners on the current viewport, causing progressive
    // flashing as multiple listener sets race each other.
    let retryTimer: number | null = null;
    const tryAttach = () => {
      retryTimer = null;
      const vp = getViewport();
      if (!vp) {
        if (attempts++ > 20) return;
        retryTimer = window.setTimeout(tryAttach, 100);
        return;
      }
      // rAF-throttle + dedup. Claude's alt-screen redraws can fire
      // scroll/observer events multiple times per frame — without
      // coalescing this re-renders React rapidly and the thumb
      // flashes. One setState per animation frame, skipped
      // entirely when values haven't changed.
      const flush = () => {
        rafRef.current = null;
        const st = vp.scrollTop;
        const sh = vp.scrollHeight;
        const ch = vp.clientHeight;
        setState((prev) => {
          if (prev.scrollTop === st && prev.scrollHeight === sh && prev.clientHeight === ch) {
            return prev;
          }
          return { scrollTop: st, scrollHeight: sh, clientHeight: ch };
        });
        // Sticky overflow with hysteresis:
        //   - Becomes scrollable? Flip immediately.
        //   - Stops being scrollable? Wait 500ms of stable
        //     no-overflow before flipping back. Claude's alt-
        //     screen redraws create brief scrollHeight dips; this
        //     absorbs them so the thumb doesn't flash.
        if (sh > ch) {
          if (clearOverflowTimerRef.current != null) {
            window.clearTimeout(clearOverflowTimerRef.current);
            clearOverflowTimerRef.current = null;
          }
          setStickyOverflow((prev) => (prev ? prev : true));
        } else {
          // Schedule a delayed clear only if not already scheduled.
          if (clearOverflowTimerRef.current == null) {
            clearOverflowTimerRef.current = window.setTimeout(() => {
              clearOverflowTimerRef.current = null;
              const vpNow = getViewport();
              if (vpNow && vpNow.scrollHeight <= vpNow.clientHeight) {
                setStickyOverflow(false);
              }
            }, 500);
          }
        }
      };
      const update = () => {
        if (rafRef.current != null) return;
        rafRef.current = requestAnimationFrame(flush);
      };
      update();
      vp.addEventListener('scroll', update, { passive: true });
      const ro = new ResizeObserver(update);
      ro.observe(vp);
      // Previously we polled every 400ms to catch content growth from
      // PTY output (xterm doesn't emit scroll events when appending to
      // the buffer tail). Removed: on mobile the poll interleaves with
      // native scroll frames and reads vp.scrollHeight on an unrelated
      // timer, causing layout thrashing intermittent enough to surface
      // as "scroll is sometimes smooth, sometimes jerky" — exactly the
      // pattern Benjamin reported. The tradeoff is the thumb's height
      // may lag real scrollback growth until the next scroll or
      // ResizeObserver tick. Acceptable: the user only notices the
      // thumb when interacting with it, and a scroll event fires then.
      cleanup = () => {
        vp.removeEventListener('scroll', update);
        ro.disconnect();
        if (rafRef.current != null) {
          cancelAnimationFrame(rafRef.current);
          rafRef.current = null;
        }
        if (clearOverflowTimerRef.current != null) {
          window.clearTimeout(clearOverflowTimerRef.current);
          clearOverflowTimerRef.current = null;
        }
      };
    };
    tryAttach();
    return () => {
      if (retryTimer != null) {
        window.clearTimeout(retryTimer);
        retryTimer = null;
      }
      if (cleanup) cleanup();
    };
  }, [getViewport]);

  const { scrollTop, scrollHeight, clientHeight } = state;
  if (hidden) return null;
  // Intentionally NOT returning null when scrollHeight <=
  // clientHeight. Claude's alt-screen redraw transiently satisfies
  // that condition on every repaint, which made the scrollbar
  // unmount+remount — the "flash" the user reported. Instead we
  // keep the bar visible and render the thumb as a full-track
  // placeholder when there's nothing to scroll.
  if (!clientHeight) return null;

  // Use stickyOverflow (hysteresis'd) rather than the instantaneous
  // scrollHeight > clientHeight check, so the thumb doesn't flash
  // between active/inactive during Claude's alt-screen redraws.
  const hasOverflow = stickyOverflow;
  const trackHeight = clientHeight;
  // When computing thumb dimensions, use the actual scrollHeight
  // if it's still > clientHeight (may be barely so after a dip),
  // otherwise use the previous "scrollable" geometry by falling
  // back to a sensible default (48px thumb at top).
  const thumbHeight = scrollHeight > clientHeight
    ? Math.max(48, (clientHeight / scrollHeight) * trackHeight)
    : hasOverflow ? 48 : trackHeight;
  const maxThumbTop = Math.max(0, trackHeight - thumbHeight);
  const scrollProgress = scrollHeight > clientHeight
    ? scrollTop / (scrollHeight - clientHeight)
    : 0;
  const thumbTop = scrollProgress * maxThumbTop;
  const pct = Math.round(scrollProgress * 100);

  const scrollTo = (target: number) => {
    const vp = getViewport();
    if (!vp) return;
    vp.scrollTop = Math.max(0, Math.min(vp.scrollHeight - vp.clientHeight, target));
  };

  const onThumbPointerDown = (e: React.PointerEvent) => {
    const vp = getViewport();
    if (!vp) return;
    // Double-tap thumb = jump to bottom (common mobile pattern for
    // "give me latest"). Track last pointerdown timestamp.
    const now = Date.now();
    if (now - lastTapRef.current < 350) {
      scrollTo(vp.scrollHeight);
      lastTapRef.current = 0;
      haptic(25);
      return;
    }
    lastTapRef.current = now;

    e.preventDefault();
    e.stopPropagation();
    setDragging(true);
    haptic(8);
    const startY = e.clientY;
    const startScrollTop = vp.scrollTop;
    const ratio = (scrollHeight - clientHeight) / (maxThumbTop || 1);
    try { (e.target as Element).setPointerCapture?.(e.pointerId); } catch { /* ignore */ }
    const onMove = (ev: PointerEvent) => {
      vp.scrollTop = startScrollTop + (ev.clientY - startY) * ratio;
    };
    const onUp = () => {
      setDragging(false);
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      window.removeEventListener('pointercancel', onUp);
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
    window.addEventListener('pointercancel', onUp);
  };

  // Tap anywhere on the track = snap the thumb to that position
  // AND start dragging. Means you can't "miss" the thumb — tap the
  // bar anywhere and you're already grabbing it. Much more
  // forgiving than "find the small thumb first, then drag".
  const onTrackPointerDown = (e: React.PointerEvent) => {
    if (e.target !== hostRef.current) return;
    const vp = getViewport();
    if (!vp || !hostRef.current) return;
    e.preventDefault();
    const r = hostRef.current.getBoundingClientRect();
    // Snap the thumb center to the tap position.
    const snapThumbTop = Math.max(0, Math.min(maxThumbTop, (e.clientY - r.top) - thumbHeight / 2));
    const snapProgress = maxThumbTop > 0 ? snapThumbTop / maxThumbTop : 0;
    vp.scrollTop = snapProgress * (scrollHeight - clientHeight);

    // Then begin a drag from here, so one continuous gesture does
    // tap-and-drag without the user lifting their finger.
    setDragging(true);
    haptic(8);
    const startY = e.clientY;
    const startScrollTop = vp.scrollTop;
    const ratio = (scrollHeight - clientHeight) / (maxThumbTop || 1);
    const onMove = (ev: PointerEvent) => {
      vp.scrollTop = startScrollTop + (ev.clientY - startY) * ratio;
    };
    const onUp = () => {
      setDragging(false);
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      window.removeEventListener('pointercancel', onUp);
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
    window.addEventListener('pointercancel', onUp);
  };

  return (
    <div
      ref={hostRef}
      className="custom-scrollbar"
      onPointerDown={onTrackPointerDown}
    >
      {/* Fat invisible hit area wraps the visible thumb so fat-
          finger taps still land on it. The visible thumb is styled
          in CSS; this wrapper has no visible paint.
          Position via `transform: translateY` rather than `top` so
          the thumb moves on the compositor and doesn't force a
          layout recalc on every scroll frame — matters on mobile
          where a 120Hz native scroll re-runs this component per
          frame. `height` still updates via layout but only when
          scrollback grows, not on every scroll tick. */}
      <div
        className={`custom-scrollbar-thumb-hit ${dragging ? 'dragging' : ''}`}
        style={{
          top: 0,
          transform: `translateY(${thumbTop - 8}px)`,
          height: thumbHeight + 16,
        }}
        onPointerDown={hasOverflow ? onThumbPointerDown : undefined}
      >
        <div className={`custom-scrollbar-thumb ${!hasOverflow ? 'inactive' : ''}`} />
      </div>
      {dragging && (
        <div
          className="custom-scrollbar-bubble"
          style={{ top: thumbTop + thumbHeight / 2 }}
          aria-hidden
        >
          {pct}%
        </div>
      )}
    </div>
  );
}
