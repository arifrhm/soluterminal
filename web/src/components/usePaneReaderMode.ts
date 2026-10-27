import { useCallback, useRef, useState, type MutableRefObject } from 'react';
import type { Terminal } from '@xterm/xterm';
import { logScrollDiag } from '../scrollDiag';

interface UsePaneReaderModeArgs {
  paneId: string;
  termRef: MutableRefObject<Terminal | null>;
  actionGuard?: () => boolean;
}

// Keyboard keys that xterm turns into a scrollback scroll. A keydown on one of
// these is explicit "the reader is moving the viewport" intent, exactly like a
// wheel notch or a touch drag.
const SCROLL_KEYS = new Set(['ArrowUp', 'ArrowDown', 'PageUp', 'PageDown', 'Home', 'End']);

// xterm exposes its buffer service on `term._core` (underscore-prefixed in the
// 5.5 build this app pins; bare-name fallback kept for a de-privatizing
// upgrade). `isUserScrolling` is the latch xterm gates output auto-follow on:
// once set, new output no longer moves the viewport. We deliberately read and
// clear it here rather than waiting for xterm, because a browser scroll-anchor
// adjustment can set it without any user input.
function xtermBufferService(term: Terminal): { isUserScrolling?: boolean } | undefined {
  const core = (term as unknown as {
    _core?: {
      _bufferService?: { isUserScrolling?: boolean };
      bufferService?: { isUserScrolling?: boolean };
    };
  })._core;
  return core?._bufferService ?? core?.bufferService;
}

export function usePaneReaderMode({ paneId, termRef, actionGuard }: UsePaneReaderModeArgs) {
  const guardRef=useRef(actionGuard);guardRef.current=actionGuard;
  const visible=()=>guardRef.current?.() ?? true;
  const [scrolledAway, setScrolledAway] = useState(false);
  const [newLinesCount, setNewLinesCount] = useState(0);
  const [lastOutputMs, setLastOutputMs] = useState(0);
  const scrolledAwayRef = useRef(false);
  // Whether the reader wants the viewport glued to the live tail. Only a real
  // user scroll-away (wheel / touch / scroll key / scrollbar drag) flips this
  // false. A viewport move caused by content reflow — browser scroll anchoring,
  // or xterm coalescing its own scrollTop writes — does NOT, so the pane
  // recovers instead of silently stranding the reader in history.
  const followTailRef = useRef(true);
  const lastOutputCommitRef = useRef(0);

  const jumpToLive = useCallback(() => {
    if(!visible())return;
    const t = termRef.current;
    if (!t) return;
    t.scrollToBottom();
    t.focus();
    // onScroll fires on programmatic scrolls too, but flip state
    // synchronously so the chip unmounts on the same frame as the click.
    scrolledAwayRef.current = false;
    followTailRef.current = true;
    setScrolledAway(false);
    setNewLinesCount(0);
  }, [termRef]);

  const attachReaderMode = useCallback((term: Terminal) => {
    let active = true;

    const isAtBottom = () => {
      const buf = term.buffer.active;
      return buf.viewportY >= buf.baseY;
    };

    // Explicit user-intent ledger. `markIntent(true)` holds intent for the
    // duration of a press/drag (scrollbar thumb, touch scroll); the timed form
    // covers a wheel notch or scroll key whose DOM scroll event lands a frame
    // later. A viewport move with no live intent is treated as a reflow
    // artifact and undone.
    let intentUntil = 0;
    let pointerDown = false;
    const markIntent = (hold = false) => {
      if(!visible())return;
      intentUntil = performance.now() + 400;
      if (hold) pointerDown = true;
    };
    const hasIntent = () => pointerDown || performance.now() <= intentUntil;

    // Snap the terminal back to the tail AND clear xterm's user-scroll latch so
    // its own auto-follow resumes. Called only when we believe no user asked to
    // leave the bottom.
    const reassertFollow = (source: string) => {
      if(!visible())return;
      try { term.scrollToBottom(); } catch { /* best-effort */ }
      const bufferService = xtermBufferService(term);
      if (bufferService) bufferService.isUserScrolling = false;
      logScrollDiag({
        t: performance.now(),
        paneId,
        label: 'reader-refollow',
        ydisp: term.buffer.active.viewportY,
        length: term.buffer.active.length,
        rows: term.rows,
        note: source,
      });
    };

    const syncScrollAway = (source: string) => {
      if(!visible())return;
      const away = !isAtBottom();
      if (away && followTailRef.current && !hasIntent()) {
        // Not a user scroll: content reflow (scroll anchoring) or a coalesced
        // xterm scrollTop write drifted the viewport off the tail. Recover
        // instead of latching the reader away from live output.
        reassertFollow(`${source}:drift`);
        return;
      }
      if (!away) followTailRef.current = true;
      else if (hasIntent()) followTailRef.current = false;
      const nextAway = away && !followTailRef.current;
      if (nextAway === scrolledAwayRef.current) return;
      scrolledAwayRef.current = nextAway;
      setScrolledAway(nextAway);
      if (!nextAway) setNewLinesCount(0);
      logScrollDiag({
        t: performance.now(),
        paneId,
        label: `reader-${nextAway ? 'away' : 'back'}`,
        ydisp: term.buffer.active.viewportY,
        length: term.buffer.active.length,
        rows: term.rows,
        note: source,
      });
    };

    // --- user-intent listeners ----------------------------------------------
    // A plain click into the transcript is NOT a scroll gesture. Only a wheel
    // notch, a touch drag, a scroll key, or a press on the scrollbar (which
    // targets the viewport itself — text selection targets the screen layer)
    // count. Treating every pointerdown as intent meant a click while output
    // streamed could latch the reader away from the tail permanently.
    const el = term.element;
    let holdTimer: number | null = null;
    const clearHold = () => {
      pointerDown = false;
      if (holdTimer !== null) { window.clearTimeout(holdTimer); holdTimer = null; }
    };
    const onWheel = () => markIntent();
    const onTouchMove = () => markIntent();
    const onTouchEnd = () => clearHold();
    const onKeyDown = (e: KeyboardEvent) => { if (SCROLL_KEYS.has(e.key)) markIntent(); };
    el?.addEventListener('wheel', onWheel, { passive: true });
    el?.addEventListener('touchmove', onTouchMove, { passive: true });
    el?.addEventListener('touchend', onTouchEnd, { passive: true });
    el?.addEventListener('touchcancel', onTouchEnd, { passive: true });
    el?.addEventListener('keydown', onKeyDown, true);
    // pointerup/mouseup land on the document when a drag leaves the element.
    document.addEventListener('pointerup', clearHold, true);
    document.addEventListener('mouseup', clearHold, true);
    document.addEventListener('pointercancel', clearHold, true);
    const onWindowBlur = () => clearHold();
    window.addEventListener('blur', onWindowBlur);

    const scrollDisp = term.onScroll(() => syncScrollAway('xterm.onScroll'));
    let removeViewportListeners: (() => void) | null = null;
    requestAnimationFrame(() => {
      if (!active) return;
      const vp = term.element?.querySelector('.xterm-viewport') as HTMLElement | null;
      if (!vp) return;
      const onVpScroll = () => syncScrollAway('dom.scroll');
      const onVpPointerDown = () => {
        if(!visible())return;
        // A press on the viewport is the scrollbar (or its gutter). Hold intent
        // for the drag, with a hard timeout so a lost pointerup can't wedge the
        // reader away from the tail forever.
        markIntent(true);
        if (holdTimer !== null) window.clearTimeout(holdTimer);
        holdTimer = window.setTimeout(clearHold, 4000);
      };
      vp.addEventListener('scroll', onVpScroll, { passive: true });
      vp.addEventListener('pointerdown', onVpPointerDown, true);
      removeViewportListeners = () => {
        vp.removeEventListener('scroll', onVpScroll);
        vp.removeEventListener('pointerdown', onVpPointerDown, true);
      };
    });

    const lineFeedDisp = term.onLineFeed(() => {
      if(!visible())return;
      if (!scrolledAwayRef.current) {
        // Following: recover any silent drift before recording the new line.
        if (!isAtBottom() && !hasIntent()) reassertFollow('linefeed-drift');
        return;
      }
      setNewLinesCount((c) => (c >= 999 ? 999 : c + 1));
    });

    const writeParsedDisp = term.onWriteParsed(() => {
      if(!visible())return;
      if (!scrolledAwayRef.current) {
        // A write can grow above the fold without a line feed (wrapped text,
        // an image, a re-measured row). Keep the tail pinned if we're following.
        if (!isAtBottom() && !hasIntent()) reassertFollow('writeparsed-drift');
        return;
      }
      const now = Date.now();
      if (now - lastOutputCommitRef.current < 500) return;
      lastOutputCommitRef.current = now;
      setLastOutputMs(now);
    });

    // Final safety net: xterm repaints on its own schedule during output, so a
    // drift that slipped past line-feed/write hooks is corrected before the
    // reader can perceive it. No-op while already at the tail.
    const renderDisp = term.onRender(() => {
      if(!visible())return;
      if (scrolledAwayRef.current) return;
      if (!isAtBottom() && !hasIntent()) reassertFollow('render-drift');
    });

    return () => {
      active = false;
      scrollDisp.dispose();
      lineFeedDisp.dispose();
      writeParsedDisp.dispose();
      renderDisp.dispose();
      removeViewportListeners?.();
      clearHold();
      el?.removeEventListener('wheel', onWheel);
      el?.removeEventListener('touchmove', onTouchMove);
      el?.removeEventListener('touchend', onTouchEnd);
      el?.removeEventListener('touchcancel', onTouchEnd);
      el?.removeEventListener('keydown', onKeyDown, true);
      document.removeEventListener('pointerup', clearHold, true);
      document.removeEventListener('mouseup', clearHold, true);
      document.removeEventListener('pointercancel', clearHold, true);
      window.removeEventListener('blur', onWindowBlur);
    };
  }, [paneId]);

  return {
    scrolledAway,
    newLinesCount,
    lastOutputMs,
    jumpToLive,
    attachReaderMode,
    // Stable latch exposed so the Pane's own viewport-sync writer can avoid
    // translating a reflow artifact into a scrollLines() call while we're
    // following the tail.
    followTailRef,
  };
}
