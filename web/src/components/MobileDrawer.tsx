import { useEffect, useRef, useState } from 'react';
import { Sidebar } from './Sidebar';

interface Props {
  open: boolean;
  onOpen: () => void;
  onClose: () => void;
  // Forwarded to Sidebar so its footer "Press ? for shortcuts" button
  // can open the help overlay — which is the only keyboardless path on
  // mobile, since "?" is a character users need in the terminal.
  onOpenHelp?: () => void;
}

// Width of the drawer as a fraction of the screen. 85% leaves just
// enough of the underlying UI peeking to signal "this is an overlay".
const DRAWER_WIDTH_VW = 85;
const DRAWER_MAX_WIDTH_PX = 340;

// How wide the left-edge swipe-detection strip is. Kept narrow so it
// doesn't interfere with interactions inside the terminal (which sits
// under the app). Also less likely to clash with iOS Safari's system
// back-swipe, which is recognised ONLY when the touch starts in the
// very first few pixels of the viewport AND the gesture is shallow.
// 24px was a tight thumb target in landscape and on phones with system
// nav bars eating the bottom edge. 40px gives a realistic hit zone
// without meaningfully encroaching on the grid's left column (where a
// user is very unlikely to be intentionally touching in the first
// ~40px — that column is terminal padding plus xterm scrollbar).
const EDGE_SWIPE_ZONE_PX = 40;

// Minimum horizontal distance (px) before we commit to opening/closing.
// Below this threshold the gesture is treated as a cancelled tap/scroll.
const COMMIT_THRESHOLD_PX = 60;

// Angle/velocity sanity: if vertical movement exceeds horizontal by this
// ratio, treat as a scroll, not a swipe.
const VERT_CANCEL_RATIO = 1.2;

function getDrawerWidthPx(el: HTMLDivElement | null): number {
  const measured = el?.getBoundingClientRect().width || 0;
  if (measured > 0) return measured;
  return Math.max(1, Math.min((window.innerWidth * DRAWER_WIDTH_VW) / 100, DRAWER_MAX_WIDTH_PX));
}

export function MobileDrawer({ open, onOpen, onClose, onOpenHelp }: Props) {
  const drawerRef = useRef<HTMLDivElement | null>(null);
  // Live drag offset in px. Positive = drawer is being pulled to the right
  // (opening). Used to translate the drawer during the gesture so it feels
  // "attached" to the finger instead of snapping open on release.
  const [dragOffset, setDragOffset] = useState<number | null>(null);

  // Esc closes. Shared handler so it fires whether focus is in body, in
  // xterm, or anywhere else.
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        onClose();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [open, onClose]);

  // Edge-swipe to OPEN. Attached to window so it works regardless of which
  // element is under the finger — but we filter the start position to the
  // narrow edge zone so normal terminal interactions aren't hijacked.
  //
  // IMPORTANT: the handlers MUST NOT cause React re-renders or re-register
  // mid-gesture for touches that turn out to be vertical scrolls. Prior
  // implementation fired setDragOffset(0) on every touchstart within the
  // 40px edge zone — triggering an App re-render AND (because dragOffset
  // was in this effect's deps) a full teardown/reattach of all window touch
  // listeners while the user was still mid-gesture. Cue: left-side scroll
  // felt jittery, sometimes dropped the gesture entirely. The rule now:
  //   - touchstart stores geometry in closure locals only, no setState.
  //   - setDragOffset fires only once we've committed to a horizontal drag
  //     (dx > 0 after the vertical-cancel check has cleared).
  //   - setDragOffset(null) on end/cancel is skipped if we never set a
  //     non-null value, so pure-vertical touches leave React state
  //     untouched.
  //   - dragOffset is NOT in the effect's deps — current value is read via
  //     closure-local `currentDragOffset`, so the effect doesn't re-run
  //     every time the user mid-slides the drawer open.
  useEffect(() => {
    if (open) return; // while open, the drawer's own handlers take over
    let startX = 0;
    let startY = 0;
    let tracking = false;
    let currentDragOffset = 0;
    let showing = false;

    const onStart = (e: TouchEvent) => {
      const t = e.touches[0];
      if (!t) return;
      if (t.clientX > EDGE_SWIPE_ZONE_PX) return;
      startX = t.clientX;
      startY = t.clientY;
      tracking = true;
      currentDragOffset = 0;
      showing = false;
    };
    const onMove = (e: TouchEvent) => {
      if (!tracking) return;
      const t = e.touches[0];
      if (!t) return;
      const dx = t.clientX - startX;
      const dy = Math.abs(t.clientY - startY);
      // Gesture is vertical → user is scrolling, abandon.
      if (dy > Math.abs(dx) * VERT_CANCEL_RATIO && dy > 10) {
        tracking = false;
        if (showing) {
          showing = false;
          setDragOffset(null);
        }
        return;
      }
      if (dx > 0) {
        currentDragOffset = Math.min(dx, getDrawerWidthPx(drawerRef.current));
        showing = true;
        setDragOffset(currentDragOffset);
      }
    };
    const onEnd = () => {
      if (!tracking) return;
      tracking = false;
      const committed = currentDragOffset > COMMIT_THRESHOLD_PX;
      if (showing) {
        showing = false;
        setDragOffset(null);
      }
      if (committed) onOpen();
    };
    // ALL passive — we deliberately don't preventDefault here. A passive
    // touchmove lets the browser fast-path scrolling, which is the
    // dominant perf cost on low-end phones. The cost is we can't block
    // Android's edge-swipe-back — mitigated via CSS
    // `overscroll-behavior-x: contain` on body.
    window.addEventListener('touchstart', onStart, { passive: true });
    window.addEventListener('touchmove', onMove, { passive: true });
    window.addEventListener('touchend', onEnd);
    window.addEventListener('touchcancel', onEnd);
    return () => {
      window.removeEventListener('touchstart', onStart);
      window.removeEventListener('touchmove', onMove);
      window.removeEventListener('touchend', onEnd);
      window.removeEventListener('touchcancel', onEnd);
    };
  }, [open, onOpen]);

  // Swipe-left to CLOSE while open. Attached to the drawer element so it
  // only activates for touches inside the drawer (scrim has its own tap
  // handler).
  useEffect(() => {
    if (!open) return;
    const el = drawerRef.current;
    if (!el) return;
    let startX = 0;
    let startY = 0;
    let tracking = false;
    let currentDragOffset = 0;
    let showing = false;

    const onStart = (e: TouchEvent) => {
      const t = e.touches[0];
      if (!t) return;
      startX = t.clientX;
      startY = t.clientY;
      tracking = true;
      currentDragOffset = 0;
      showing = false;
    };
    const onMove = (e: TouchEvent) => {
      if (!tracking) return;
      const t = e.touches[0];
      if (!t) return;
      const dx = t.clientX - startX;
      const dy = Math.abs(t.clientY - startY);
      if (dy > Math.abs(dx) * VERT_CANCEL_RATIO && dy > 10) {
        // Vertical motion — user is scrolling the drawer, not dismissing.
        tracking = false;
        if (showing) {
          showing = false;
          setDragOffset(null);
        }
        return;
      }
      if (dx < 0) {
        // Negative dx (left swipe) shrinks the drawer towards closed.
        currentDragOffset = dx;
        showing = true;
        setDragOffset(dx);
      }
    };
    const onEnd = () => {
      if (!tracking) return;
      tracking = false;
      const committed = currentDragOffset < -COMMIT_THRESHOLD_PX;
      if (showing) {
        showing = false;
        setDragOffset(null);
      }
      if (committed) onClose();
    };
    el.addEventListener('touchstart', onStart, { passive: true });
    el.addEventListener('touchmove', onMove, { passive: true });
    el.addEventListener('touchend', onEnd);
    el.addEventListener('touchcancel', onEnd);
    return () => {
      el.removeEventListener('touchstart', onStart);
      el.removeEventListener('touchmove', onMove);
      el.removeEventListener('touchend', onEnd);
      el.removeEventListener('touchcancel', onEnd);
    };
  }, [open, onClose]);

  // Compute the drawer's live transform. When "open" and no drag, it's
  // fully on-screen (0). When closed and no drag, it's off-screen
  // (-100%). During a drag we interpolate between those.
  let translate: string;
  if (dragOffset !== null) {
    if (open) {
      // Closing drag — dragOffset is negative.
      translate = `translateX(${dragOffset}px)`;
    } else {
      // Opening drag — shift from the actual drawer width by the drag amount.
      // CSS caps the drawer at 340px, so viewport math alone can over-translate.
      const drawerPx = getDrawerWidthPx(drawerRef.current);
      translate = `translateX(${dragOffset - drawerPx}px)`;
    }
  } else {
    translate = open ? 'translateX(0)' : 'translateX(-100%)';
  }

  const scrimOpacity =
    dragOffset !== null
      ? Math.max(
          0,
          Math.min(
            1,
            (open ? 1 : 0) +
              dragOffset / getDrawerWidthPx(drawerRef.current),
          ),
        )
      : open
        ? 1
        : 0;

  return (
    <>
      <div
        className="mobile-drawer-scrim"
        style={{
          opacity: scrimOpacity,
          pointerEvents: open ? 'auto' : 'none',
        }}
        onClick={onClose}
        aria-hidden="true"
      />
      <aside
        ref={drawerRef}
        className={`mobile-drawer ${open ? 'open' : ''}`}
        style={{
          transform: translate,
          // Snap (transition) back when the user releases without committing,
          // but NOT during an active drag — there we want 1:1 finger tracking.
          transition: dragOffset === null ? 'transform 0.2s ease' : 'none',
        }}
        aria-hidden={!open}
      >
        <Sidebar
          onToggleCollapse={onClose}
          onOpenHelp={onOpenHelp ? () => { onClose(); onOpenHelp(); } : undefined}
        />
      </aside>
    </>
  );
}
