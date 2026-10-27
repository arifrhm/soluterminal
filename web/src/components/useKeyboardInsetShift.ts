import { useEffect } from 'react';
import { computeKeyboardInset, isProbablyMobileDevice } from '../lib/mobile';

/**
 * Keep the focused pane's terminal usable above the on-screen keyboard.
 *
 * The app's viewport meta uses the browser-default
 * `interactive-widget=resizes-visual`, so the soft keyboard overlays the
 * layout viewport instead of shrinking it: `.app` (100dvh) keeps its height,
 * the pane's ResizeObserver never fires, and the bottom rows — where a TUI
 * draws its input box — end up hidden behind the keyboard. The browser then
 * pans the visual viewport to chase xterm's focused helper textarea, which is
 * what makes typed text appear to scroll up into the scroll history.
 *
 * Two strategies, picked by `resizeToFit`:
 *
 *  - TRANSLATE (default, alt-screen TUIs like Claude Code): lift the host up
 *    by the keyboard inset. grid == PTY always, no SIGWINCH, no flicker. Works
 *    because Claude's input box sits mid-grid, so the lift reveals it and the
 *    clipped top is just transcript the user can scroll back to.
 *
 *  - RESIZE (`resizeToFit`, normal-buffer TUIs like Codex/Ratatui): a
 *    normal-buffer app draws its input box at the BOTTOM grid rows, so a
 *    translate would lift the input box clean OFF the top of the pane. Instead
 *    shrink the host to the area above the keyboard. The ResizeObserver/refit
 *    then sizes the PTY to the visible rows, Codex redraws its input box just
 *    above the keyboard, and scrollback works normally. This costs a SIGWINCH
 *    on keyboard open/close — acceptable for Codex, avoided for Claude.
 */
export function useKeyboardInsetShift(
  hostRef: React.RefObject<HTMLElement | null>,
  active: boolean,
  resizeToFit = false,
): void {
  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;

    const clear = () => {
      host.style.transform = '';
      host.style.willChange = '';
      host.style.maxHeight = '';
    };

    if (!active || !isProbablyMobileDevice()) {
      clear();
      return;
    }

    const vv = window.visualViewport;
    if (!vv) {
      clear();
      return;
    }

    // A keyboard eats a large slice of height; the URL bar / incidental chrome
    // eat a small one. Only treat a substantial inset as "keyboard open" so we
    // don't jiggle the terminal on URL-bar show/hide.
    const KEYBOARD_MIN_PX = 120;
    // Never shrink the host below this — a near-full-screen keyboard would
    // otherwise collapse the pane. Better to clip than to vanish.
    const MIN_HOST_PX = 80;

    let raf = 0;
    const apply = () => {
      raf = 0;
      const inset = computeKeyboardInset();
      const shift = inset >= KEYBOARD_MIN_PX ? Math.round(inset) : 0;

      if (resizeToFit) {
        // Normal-buffer: shrink the host so xterm refits to the visible area
        // and the PTY rows match what's actually on screen above the keyboard.
        host.style.transform = '';
        host.style.willChange = '';
        if (shift) {
          // Height from the host's top to the keyboard's top, in
          // visual-viewport coordinates (vv.height already excludes the
          // keyboard). rectTop is layout-viewport-relative; subtract
          // vv.offsetTop to convert.
          const rectTop = host.getBoundingClientRect().top;
          const visible = Math.round(vv.height - (rectTop - vv.offsetTop));
          host.style.maxHeight = visible >= MIN_HOST_PX ? `${visible}px` : '';
        } else {
          host.style.maxHeight = '';
        }
      } else {
        // Alt-screen: translate the host up; grid/PTY unchanged.
        host.style.maxHeight = '';
        host.style.transform = shift ? `translate3d(0, ${-shift}px, 0)` : '';
        host.style.willChange = shift ? 'transform' : '';
      }

      // We are the single source of vertical offset. Cancel the browser's own
      // pan-to-textarea so the two don't stack. Only when there's a real
      // offset to cancel: calling scrollTo while already at 0 still emits a
      // visualViewport scroll event, re-scheduling apply() into a churn loop.
      // The 0.5px floor ignores subpixel jitter.
      if (shift && (Math.abs(vv.offsetTop) > 0.5 || Math.abs(window.scrollY) > 0.5)) {
        window.scrollTo(0, 0);
      }
    };
    const schedule = () => {
      if (raf) return;
      raf = window.requestAnimationFrame(apply);
    };

    apply();
    vv.addEventListener('resize', schedule);
    vv.addEventListener('scroll', schedule);
    return () => {
      if (raf) window.cancelAnimationFrame(raf);
      vv.removeEventListener('resize', schedule);
      vv.removeEventListener('scroll', schedule);
      clear();
    };
  }, [hostRef, active, resizeToFit]);
}
