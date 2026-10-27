import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { useDismiss } from './useDismiss';

// Shared "click a chrome button, get a dropdown panel" behavior. Used by
// AlertsMenu, VoiceConfigMenu, VoiceHistoryButton — historically in the
// top bar (hence the name), but those buttons now live in the sidebar
// footer (bottom-left of the app).
//
// Two problems the current implementation solves:
//
//   1. Clipping by ancestor overflow. The sidebar has scrollable
//      content and `overflow: hidden` at a few levels; a descendant
//      `position: absolute` panel gets cropped whenever it spills past
//      those bounds. Fix: render through a React portal to document.body
//      and position the panel with `position: fixed` coordinates
//      computed from the trigger's bounding rect — ancestors become
//      irrelevant to the panel's layout.
//
//   2. Viewport clipping. A trigger near the bottom-left of the screen
//      wants its panel to open UP and anchor to the LEFT, not DOWN and
//      RIGHT. The placement pass measures trigger + panel + viewport
//      and flips to whichever side has room. Clamped 4px inside the
//      viewport edges so the panel never sits flush against the edge.
//
// Placement runs in useLayoutEffect before paint; a ResizeObserver on
// the trigger + window resize listener keep it correct through sidebar
// drag-resizes and rotations.

type VPlace = 'below' | 'above';
type HPlace = 'left' | 'right';

// Inset from the viewport edges. Keeps the panel from sitting flush
// against the screen edge (looks clipped) and out of iOS safe-area
// notches in practice.
const VIEWPORT_INSET = 4;

export function TopbarPopover({
  trigger,
  children,
  align = 'right',
  minWidth,
  onOpenChange,
}: {
  trigger: (opts: { open: boolean; toggle: () => void }) => ReactNode;
  /** A function child receives `close` so a row inside the panel can dismiss
   *  it after acting (e.g. opening another surface). Plain nodes still work. */
  children: ReactNode | ((api: { close: () => void }) => ReactNode);
  align?: HPlace;
  minWidth?: number;
  /** Fired whenever the panel opens or closes — e.g. to mark notifications
   *  seen when the alerts bell opens. */
  onOpenChange?: (open: boolean) => void;
}) {
  const [open, setOpen] = useState(false);

  useEffect(() => {
    onOpenChange?.(open);
    // onOpenChange identity isn't part of the trigger — only react to open.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null);
  const rootRef = useRef<HTMLDivElement | null>(null);
  const panelRef = useRef<HTMLDivElement | null>(null);

  // Full history of the click-not-mousedown rule now lives in useDismiss.ts.
  useDismiss(() => setOpen(false), {
    enabled: open,
    inside: [rootRef, panelRef],
    capture: false,
    target: 'document',
  });

  // Reset measured position when closed so the next open gets a fresh
  // measurement rather than flashing the previous placement.
  useEffect(() => {
    if (open) return;
    setPos(null);
  }, [open]);

  // Measure + flip + clamp on open, resize, and sidebar drag. Ancestors
  // don't matter for the math — we work in viewport coordinates.
  useLayoutEffect(() => {
    if (!open) return;
    const measure = () => {
      const root = rootRef.current;
      const panel = panelRef.current;
      if (!root) return;

      const trig = root.getBoundingClientRect();
      // On the first measurement the panel hasn't rendered yet, so we
      // estimate. minWidth is a reasonable proxy for actual width at
      // this stage (caller-supplied or a default). Height gets refined
      // on the second pass once the panel has laid out.
      const panelH = panel ? panel.getBoundingClientRect().height : 180;
      const panelW = panel ? panel.getBoundingClientRect().width : Math.max(minWidth ?? 0, 220);
      const vw = window.innerWidth;
      const vh = window.innerHeight;
      const gap = 6;

      // Vertical placement. Prefer below; flip if below clips AND above
      // has more room. Clamp both ways — if neither side has enough
      // room (short viewport, phone in landscape), we just pin to the
      // side with more space and let overflow happen rather than
      // pushing fully off-screen.
      const roomBelow = vh - trig.bottom - gap;
      const roomAbove = trig.top - gap;
      const nextV: VPlace = panelH > roomBelow && roomAbove >= panelH
        ? 'above'
        : 'below';
      let top: number;
      if (nextV === 'above') top = trig.top - gap - panelH;
      else top = trig.bottom + gap;
      top = Math.max(VIEWPORT_INSET, Math.min(top, vh - panelH - VIEWPORT_INSET));

      // Horizontal anchor. `align="right"` anchors the panel's right
      // edge to the trigger's right edge (panel grows leftward);
      // `align="left"` anchors the left edges (panel grows rightward).
      // Flip whichever would push the panel off-viewport, then clamp.
      let left: number;
      if (align === 'right') {
        left = trig.right - panelW;
        if (left < VIEWPORT_INSET) left = trig.left; // fall back to left-anchor
      } else {
        left = trig.left;
        if (left + panelW > vw - VIEWPORT_INSET) left = trig.right - panelW;
      }
      left = Math.max(VIEWPORT_INSET, Math.min(left, vw - panelW - VIEWPORT_INSET));

      setPos({ top, left });
    };

    measure();
    // Remeasure on resize (rotation, window resize) and when the
    // trigger itself moves (sidebar drag-resize). ResizeObserver on
    // rootRef covers the "trigger moved without a window resize" case.
    const ro = new ResizeObserver(measure);
    if (rootRef.current) ro.observe(rootRef.current);
    window.addEventListener('resize', measure);
    return () => {
      ro.disconnect();
      window.removeEventListener('resize', measure);
    };
  }, [open, align, minWidth]);

  const panelStyle: React.CSSProperties = {
    position: 'fixed',
    top: pos?.top ?? -9999,
    left: pos?.left ?? -9999,
    // Hide the panel offscreen during the first measurement pass so it
    // never flashes at the wrong coordinates. opacity keeps layout
    // measurable (unlike display:none) so the second pass can read real
    // dimensions.
    opacity: pos ? 1 : 0,
    zIndex: 'var(--z-top)',
    ...(minWidth ? { minWidth } : null),
  };

  // Portal to body so ancestors' `overflow: hidden` / stacking-context
  // rules can't clip us. The root still holds the trigger + receives
  // click-outside detection via its contains() check above.
  const portalTarget = typeof document !== 'undefined' ? document.body : null;

  return (
    <div className="topbar-popover-root" ref={rootRef}>
      {trigger({ open, toggle: () => setOpen((v) => !v) })}
      {open && portalTarget && createPortal(
        <div
          ref={panelRef}
          className="topbar-popover-panel"
          style={panelStyle}
        >
          {typeof children === 'function' ? children({ close: () => setOpen(false) }) : children}
        </div>,
        portalTarget,
      )}
    </div>
  );
}
