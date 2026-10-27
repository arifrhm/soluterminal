import React, {
  cloneElement,
  isValidElement,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from 'react';
import { createPortal } from 'react-dom';

// Styled tooltip replacing native `title=`. Why not title=: it is unstyled,
// shows after a UA-controlled ~1s with no way to tune it, never appears for
// keyboard focus, and reads inconsistently across WebView2/browsers. This
// one shows on hover AND focus, is theme-styled, viewport-clamped, and wires
// aria-describedby while visible so screen readers get the same text.
//
// Usage: <Tooltip label="Maximise pane"><button …/></Tooltip>
// The child must be a single element; handlers are merged (the child's own
// onMouseEnter etc. still run). No wrapper DOM is added — the child is cloned
// — so flex/grid rows keep their exact layout.
//
// Adoption is incremental: new/high-traffic surfaces use <Tooltip>, the long
// tail of title= migrates as files get touched.

const SHOW_DELAY_MS = 450;
const VIEWPORT_PAD = 8;
const ANCHOR_GAP = 6;

interface TooltipProps {
  label: string;
  // Preferred side; flips automatically when there's no room.
  side?: 'top' | 'bottom';
  children: React.ReactElement;
}

export function Tooltip({ label, side = 'bottom', children }: TooltipProps) {
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null);
  const showTimer = useRef<number | undefined>(undefined);
  const tipRef = useRef<HTMLDivElement | null>(null);
  const tipId = useId();

  const cancelShow = () => {
    if (showTimer.current !== undefined) {
      clearTimeout(showTimer.current);
      showTimer.current = undefined;
    }
  };
  const hide = () => {
    cancelShow();
    setAnchor(null);
    setPos(null);
  };

  // Position once the (hidden) tip has a size. Fixed coords, clamped to the
  // viewport, flipping to the other side when the preferred one lacks room.
  useLayoutEffect(() => {
    if (!anchor || !tipRef.current) return;
    const a = anchor.getBoundingClientRect();
    const t = tipRef.current.getBoundingClientRect();
    let top =
      side === 'bottom' ? a.bottom + ANCHOR_GAP : a.top - t.height - ANCHOR_GAP;
    if (top + t.height > window.innerHeight - VIEWPORT_PAD) {
      top = a.top - t.height - ANCHOR_GAP;
    }
    if (top < VIEWPORT_PAD) top = a.bottom + ANCHOR_GAP;
    let left = a.left + a.width / 2 - t.width / 2;
    left = Math.min(
      Math.max(left, VIEWPORT_PAD),
      window.innerWidth - t.width - VIEWPORT_PAD,
    );
    setPos({ left, top });
  }, [anchor, side, label]);

  // Anything that moves the anchor invalidates the fixed position — just hide
  // (cheaper and less glitchy than live-tracking a scrolling anchor).
  useEffect(() => {
    if (!anchor) return;
    const onScroll = () => hide();
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') hide();
    };
    window.addEventListener('scroll', onScroll, true);
    window.addEventListener('resize', onScroll);
    window.addEventListener('keydown', onKeyDown, true);
    return () => {
      window.removeEventListener('scroll', onScroll, true);
      window.removeEventListener('resize', onScroll);
      window.removeEventListener('keydown', onKeyDown, true);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [anchor]);

  useEffect(() => cancelShow, []);

  // After the hooks so the hook order stays stable when label flips between
  // empty and set (e.g. a computed tooltip that is sometimes blank).
  if (!label || !isValidElement(children)) return children;
  const childProps = children.props as Record<string, ((e: never) => void) | undefined>;
  const schedule = (el: HTMLElement, delay: number) => {
    cancelShow();
    showTimer.current = window.setTimeout(() => {
      showTimer.current = undefined;
      setAnchor(el);
    }, delay);
  };

  const clone = cloneElement(children as React.ReactElement<Record<string, unknown>>, {
    'aria-describedby': anchor && pos ? tipId : undefined,
    onMouseEnter: (e: React.MouseEvent<HTMLElement>) => {
      (childProps.onMouseEnter as ((e: React.MouseEvent<HTMLElement>) => void) | undefined)?.(e);
      schedule(e.currentTarget, SHOW_DELAY_MS);
    },
    onMouseLeave: (e: React.MouseEvent<HTMLElement>) => {
      (childProps.onMouseLeave as ((e: React.MouseEvent<HTMLElement>) => void) | undefined)?.(e);
      hide();
    },
    // Focus shows immediately: a keyboard user already "asked" by tabbing here.
    onFocus: (e: React.FocusEvent<HTMLElement>) => {
      (childProps.onFocus as ((e: React.FocusEvent<HTMLElement>) => void) | undefined)?.(e);
      schedule(e.currentTarget, 0);
    },
    onBlur: (e: React.FocusEvent<HTMLElement>) => {
      (childProps.onBlur as ((e: React.FocusEvent<HTMLElement>) => void) | undefined)?.(e);
      hide();
    },
    // Clicking usually changes state the label described — drop the tip.
    onMouseDown: (e: React.MouseEvent<HTMLElement>) => {
      (childProps.onMouseDown as ((e: React.MouseEvent<HTMLElement>) => void) | undefined)?.(e);
      hide();
    },
  });

  return (
    <>
      {clone}
      {anchor &&
        createPortal(
          <div
            ref={tipRef}
            id={tipId}
            role="tooltip"
            className="ui-tooltip"
            // Render hidden at 0,0 for the measure pass, then place.
            style={
              pos
                ? { left: pos.left, top: pos.top }
                : { left: 0, top: 0, visibility: 'hidden' }
            }
          >
            {label}
          </div>,
          document.body,
        )}
    </>
  );
}
