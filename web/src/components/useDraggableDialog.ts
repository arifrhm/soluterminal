import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { CSSProperties, PointerEvent as ReactPointerEvent } from 'react';

const EDGE_MARGIN = 8;

// Drag-to-move behavior for a dialog card that mounts inside a flex-centered
// .dialog-backdrop. On open the card's centered position is captured and
// re-applied as fixed left/top, which does two things:
//  1. the header becomes a drag handle that moves the window 1:1 with the
//     cursor, and
//  2. the native resize grip tracks the cursor 1:1 — a flex-centered card
//     re-centers on every frame, so half of each resize delta goes to the
//     opposite edge and the grip drifts away from the pointer.
// The card is kept inside the viewport when the window resizes or the card
// grows (tab switch, native resize past the edge).
export function useDraggableDialog(open: boolean) {
  const cardRef = useRef<HTMLDivElement | null>(null);
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null);
  const drag = useRef<{ id: number; offX: number; offY: number } | null>(null);

  const clamp = useCallback((p: { left: number; top: number }) => {
    const el = cardRef.current;
    const w = el?.offsetWidth ?? 0;
    const h = el?.offsetHeight ?? 0;
    const maxLeft = Math.max(EDGE_MARGIN, window.innerWidth - w - EDGE_MARGIN);
    const maxTop = Math.max(EDGE_MARGIN, window.innerHeight - h - EDGE_MARGIN);
    return {
      left: Math.min(Math.max(p.left, EDGE_MARGIN), maxLeft),
      top: Math.min(Math.max(p.top, EDGE_MARGIN), maxTop),
    };
  }, []);

  // Pin the card where flex centering put it, before first paint (no jump).
  useLayoutEffect(() => {
    if (!open) {
      setPos(null);
      return;
    }
    const el = cardRef.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    setPos({ left: Math.round(r.left), top: Math.round(r.top) });
  }, [open]);

  // Re-clamp when the viewport shrinks or the card grows past an edge.
  useEffect(() => {
    if (!open) return;
    const reclamp = () => setPos((p) => (p ? clamp(p) : p));
    window.addEventListener('resize', reclamp);
    let ro: ResizeObserver | undefined;
    if (typeof ResizeObserver !== 'undefined' && cardRef.current) {
      ro = new ResizeObserver(reclamp);
      ro.observe(cardRef.current);
    }
    return () => {
      window.removeEventListener('resize', reclamp);
      ro?.disconnect();
    };
  }, [open, clamp]);

  const onPointerDown = useCallback((e: ReactPointerEvent<HTMLElement>) => {
    if (e.button !== 0) return;
    // Interactive children (the × button) keep their click; only chrome drags.
    if ((e.target as HTMLElement).closest('button, input, select, textarea, a')) return;
    const el = cardRef.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    drag.current = { id: e.pointerId, offX: e.clientX - r.left, offY: e.clientY - r.top };
    e.currentTarget.setPointerCapture?.(e.pointerId);
    e.preventDefault();
  }, []);

  const onPointerMove = useCallback(
    (e: ReactPointerEvent<HTMLElement>) => {
      const d = drag.current;
      if (!d || e.pointerId !== d.id) return;
      setPos(clamp({ left: e.clientX - d.offX, top: e.clientY - d.offY }));
    },
    [clamp],
  );

  const endDrag = useCallback((e: ReactPointerEvent<HTMLElement>) => {
    if (drag.current?.id === e.pointerId) drag.current = null;
  }, []);

  const cardStyle: CSSProperties | undefined = pos
    ? { position: 'fixed', left: pos.left, top: pos.top, margin: 0 }
    : undefined;

  return {
    cardRef,
    cardStyle,
    dragHandleProps: {
      onPointerDown,
      onPointerMove,
      onPointerUp: endDrag,
      onPointerCancel: endDrag,
    },
  };
}
