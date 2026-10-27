import { useEffect } from 'react';

// Single source of truth for the RENDERED heights of the fixed mobile bottom
// bars (.mobile-bottombar, .mobile-keybar, .mobile-quick-reply). Measures each
// bar's actual height into --bottombar-h / --keybar-h / --quick-reply-h on
// <html>; CSS composes those into `.main` padding-bottom and each bar's own
// `bottom`. This replaces the hard-coded 52/56/66/64/120px constants that were
// duplicated across the stylesheet — those guessed at content-sized bars and
// were wrong on devices with large-font / accessibility scaling, notch insets,
// or a wrapped keybar row.
//
// IMPORTANT: this is ORTHOGONAL to the soft keyboard. The bars ride up with
// var(--keyboard-inset) (their own `bottom`); useKeyboardInsetShift lifts the
// pane by the keyboard inset. This hook only measures the bars' intrinsic
// height — never fold the keyboard inset in here (that double-lifts the pane;
// see the .main padding-bottom comment).
export function useBottomBarsMeasure(active: boolean): void {
  useEffect(() => {
    const root = document.documentElement;
    const setVar = (name: string, px: number) =>
      root.style.setProperty(name, `${Math.ceil(px)}px`);
    const reset = () => {
      setVar('--bottombar-h', 0);
      setVar('--keybar-h', 0);
      setVar('--quick-reply-h', 0);
    };
    if (!active) {
      reset();
      return;
    }

    const bars: Array<[string, string]> = [
      ['.mobile-bottombar', '--bottombar-h'],
      ['.mobile-keybar', '--keybar-h'],
      ['.mobile-quick-reply', '--quick-reply-h'],
    ];

    let raf = 0;
    let ro: ResizeObserver;
    const measure = () => {
      raf = 0;
      ro.disconnect();
      for (const [sel, varName] of bars) {
        const el = document.querySelector(sel) as HTMLElement | null;
        // offsetHeight counts padding + border + the bar's own safe-area
        // padding — exactly the room it occupies. 0 when it isn't mounted
        // (keybar hidden / quick-reply idle), so the reservation self-zeroes.
        setVar(varName, el ? el.offsetHeight : 0);
        if (el) ro.observe(el);
      }
    };
    const schedule = () => {
      if (raf) return;
      raf = window.requestAnimationFrame(measure);
    };

    ro = new ResizeObserver(schedule);
    measure();

    // Catch bars mounting/unmounting (keybar toggle, quick-reply show/hide —
    // each bar component returns null when hidden, so the bar elements are
    // added/removed as direct children of `.app`). Size changes on an
    // already-mounted bar (keybar wrapping a row, quick-reply textarea
    // growing, font/orientation) are handled by the ResizeObserver above, so
    // this only needs childList on the stable parent — NOT a subtree+class
    // attribute watch on document.body, which fired ~every frame on xterm's
    // constant cursor/focus/selection class churn (a forced reflow per paint
    // frame of terminal activity). rAF-debounced via schedule() so any
    // remaining DOM churn coalesces to at most one measure per frame.
    const barParent = document.querySelector('.app') ?? document.body;
    const mo = new MutationObserver(schedule);
    mo.observe(barParent, { childList: true });
    window.addEventListener('resize', schedule);
    window.visualViewport?.addEventListener('resize', schedule);

    return () => {
      if (raf) window.cancelAnimationFrame(raf);
      ro.disconnect();
      mo.disconnect();
      window.removeEventListener('resize', schedule);
      window.visualViewport?.removeEventListener('resize', schedule);
      reset();
    };
  }, [active]);
}
