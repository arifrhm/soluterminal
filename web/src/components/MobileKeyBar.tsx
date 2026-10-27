import { effectiveTerminalTarget } from '../store';
import { useEffect, useRef, useState } from 'react';
import { useStore } from '../store';
import { debugLog } from '../debug';
import { isMobileViewport } from '../lib/mobile';

// Raw byte sequences for non-printable/control keys. Ordered
// left-to-right by frequency of use when driving Claude Code from
// mobile: submit/cancel/choice keys first (you hit these constantly
// in an approval flow), then editing, then cursor/history, then
// shell control at the end. Horizontally scrollable — the row
// widens as needed, but the first ~6 should rarely require scroll.
const KEYS: Array<{ label: string; bytes: string; title?: string; group?: 'primary' | 'edit' | 'nav' | 'ctrl' }> = [
  // Primary — the Claude Code approval loop lives here.
  { label: '⏎', bytes: '\r', title: 'Enter / submit', group: 'primary' },
  { label: 'Esc', bytes: '\x1b', title: 'Escape — cancel current action', group: 'primary' },
  { label: '1', bytes: '1', title: '1 — first numbered choice (Claude approval)', group: 'primary' },
  { label: '2', bytes: '2', title: '2 — second numbered choice', group: 'primary' },
  { label: '3', bytes: '3', title: '3 — third numbered choice', group: 'primary' },
  // Shift+Tab is CSI Z — the standard xterm "back tab" sequence.
  // Claude Code uses it to cycle plan / auto-accept / normal mode.
  { label: '⇧⇥', bytes: '\x1b[Z', title: 'Shift+Tab — cycle Claude Code mode (plan / auto-accept)', group: 'primary' },
  { label: 'Tab', bytes: '\t', title: 'Tab — autocomplete / next field', group: 'primary' },

  // Editing — for fixing the prompt before you hit submit.
  { label: '⌫', bytes: '\x7f', title: 'Backspace — delete previous character', group: 'edit' },
  { label: '^U', bytes: '\x15', title: 'Ctrl+U — clear entire input line (readline kill-line)', group: 'edit' },
  { label: '^W', bytes: '\x17', title: 'Ctrl+W — delete previous word', group: 'edit' },

  // Cursor + history navigation.
  { label: '↑', bytes: '\x1b[A', title: 'Up — command history / cursor up', group: 'nav' },
  { label: '↓', bytes: '\x1b[B', title: 'Down — history / cursor down', group: 'nav' },
  { label: '←', bytes: '\x1b[D', title: 'Left — cursor left', group: 'nav' },
  { label: '→', bytes: '\x1b[C', title: 'Right — cursor right', group: 'nav' },

  // Shell / process control — rarely tapped but essential when needed.
  { label: '^C', bytes: '\x03', title: 'Ctrl+C — interrupt running command', group: 'ctrl' },
  { label: '^L', bytes: '\x0c', title: 'Ctrl+L — clear screen', group: 'ctrl' },
  { label: '^R', bytes: '\x12', title: 'Ctrl+R — reverse history search', group: 'ctrl' },
  { label: '^D', bytes: '\x04', title: 'Ctrl+D — EOF / exit shell', group: 'ctrl' },
];

function useIsNarrow(breakpoint = 800) {
  const [narrow, setNarrow] = useState(
    isMobileViewport(breakpoint),
  );
  useEffect(() => {
    const on = () => setNarrow(isMobileViewport(breakpoint));
    window.addEventListener('resize', on);
    window.visualViewport?.addEventListener('resize', on);
    return () => {
      window.removeEventListener('resize', on);
      window.visualViewport?.removeEventListener('resize', on);
    };
  }, [breakpoint]);
  return narrow;
}

interface KeyBarProps {
  // Controlled-visibility — the toggle button now lives in the unified
  // bottom bar (MobileTabBar), so open/close is driven by App state.
  visible?: boolean;
  onHide?: () => void;
}

export function MobileKeyBar({ visible = false, onHide }: KeyBarProps = {}) {
  const narrow = useIsNarrow();
  const sendToFocused = useStore((s) => s.sendToFocused);
  const focusedPaneId = useStore((s) => effectiveTerminalTarget(s));

  // Mirror visibility to a body class so the layout can reserve
  // additional bottom padding while the keybar is showing.
  useEffect(() => {
    const cls = 'keybar-visible';
    if (narrow && visible) document.body.classList.add(cls);
    else document.body.classList.remove(cls);
    return () => document.body.classList.remove(cls);
  }, [narrow, visible]);

  // Press-in-progress state. Byte dispatch is deferred to pointerup so
  // users can drag horizontally to scroll the overflow-x row without
  // each touch-start triggering the underlying button. If the press
  // moves more than DRAG_PX in either axis before release, it's
  // classified as a scroll gesture and the byte is NOT sent.
  //
  // Previously we fired on pointerdown, which meant any attempt to
  // scroll left/right through the key row also fired Enter / Esc /
  // etc. on whichever button was first touched. Real scrolling on
  // mobile is always touch-then-drag; there is no "hover and tap
  // without pressing" path.
  const pressRef = useRef<{
    bytes: string;
    pointerId: number;
    x: number;
    y: number;
    dragged: boolean;
  } | null>(null);
  const DRAG_PX = 10;

  // Per-button flash: highlight the button briefly on press, regardless
  // of whether the gesture ends up as a tap or a scroll. Gives immediate
  // "yes I saw your touch" feedback without committing to a byte send.
  // Also used for dedup-dropped taps so a silent suppression still
  // shows up visually.
  const [flashedKey, setFlashedKey] = useState<string | null>(null);
  const flashTimer = useRef<number | null>(null);
  useEffect(() => {
    return () => {
      if (flashTimer.current !== null) window.clearTimeout(flashTimer.current);
    };
  }, []);

  useEffect(() => {
    debugLog('[solu] MobileKeyBar mounted (build: keybar-v8, drag=' + DRAG_PX + 'px)');
  }, []);

  if (!narrow || !visible) return null;

  function flashButton(bytes: string) {
    if (flashTimer.current !== null) window.clearTimeout(flashTimer.current);
    setFlashedKey(bytes);
    flashTimer.current = window.setTimeout(() => {
      setFlashedKey(null);
      flashTimer.current = null;
    }, 140);
  }

  // Haptic: short 10ms pulse on Android / compatible browsers (iOS
  // Safari ignores the Vibration API — visual flash is the fallback
  // there). Gated inside a try/catch because some WebViews throw
  // instead of returning false for unsupported calls.
  function haptic() {
    try { navigator.vibrate?.(10); } catch { /* not supported */ }
  }

  function onPressStart(e: React.PointerEvent<HTMLButtonElement>, bytes: string) {
    if (!focusedPaneId) return;
    // Intentionally NOT calling preventDefault: the overflow-x-auto
    // container's native horizontal scroll depends on the browser
    // observing touch-move events. preventDefault on pointerdown can
    // suppress that on some mobile WebViews.
    pressRef.current = {
      bytes,
      pointerId: e.pointerId,
      x: e.clientX,
      y: e.clientY,
      dragged: false,
    };
    // Visual + haptic on press-start so the touch feels registered
    // even before we decide tap-vs-scroll on release.
    flashButton(bytes);
    haptic();
  }

  function onPressMove(e: React.PointerEvent<HTMLButtonElement>) {
    const p = pressRef.current;
    if (!p || p.pointerId !== e.pointerId) return;
    if (p.dragged) return;
    const dx = Math.abs(e.clientX - p.x);
    const dy = Math.abs(e.clientY - p.y);
    if (dx > DRAG_PX || dy > DRAG_PX) {
      p.dragged = true;
    }
  }

  function onPressEnd(e: React.PointerEvent<HTMLButtonElement>, bytes: string) {
    const p = pressRef.current;
    pressRef.current = null;
    if (!p || p.pointerId !== e.pointerId) return;
    if (p.bytes !== bytes) return; // pointerup on a different button → never fire
    if (p.dragged) {
      // User was scrolling. Quiet bail — flash already played on press,
      // no need for additional feedback.
      return;
    }
    debugLog('[solu] keybar tap ->', JSON.stringify(bytes));
    sendToFocused(bytes);
  }

  function onPressCancel(e: React.PointerEvent<HTMLButtonElement>) {
    // Browser took ownership of the pointer mid-press (usually to drive
    // native scroll). The press never commits as a tap.
    const p = pressRef.current;
    if (!p || p.pointerId !== e.pointerId) return;
    pressRef.current = null;
  }

  return (
    <div className="mobile-keybar" role="toolbar" aria-label="On-screen keys">
      {KEYS.map((k) => (
        <button
          key={k.label}
          type="button"
          title={k.title || k.label}
          // Tap-vs-scroll discrimination: onPressStart only records
          // intent; actual byte dispatch happens in onPressEnd if the
          // press didn't drag past DRAG_PX. onPressCancel handles the
          // browser-takes-over-for-scroll case. See the block above.
          onPointerDown={(e) => onPressStart(e, k.bytes)}
          onPointerMove={onPressMove}
          onPointerUp={(e) => onPressEnd(e, k.bytes)}
          onPointerCancel={onPressCancel}
          onPointerLeave={onPressCancel}
          onClick={(e) => {
            // Synthesized from touch — we've already dispatched via
            // pointerup (or intentionally not, if the gesture was a
            // scroll). Prevent any default and stop propagation so
            // the synthesized click doesn't double-fire.
            e.preventDefault();
            e.stopPropagation();
          }}
          disabled={!focusedPaneId}
          // The group class lets CSS tint primary keys (Enter, 1/2/3,
          // Esc, Shift+Tab) differently from secondary ones so the
          // eye lands on the Claude-approval row even without reading.
          // `flashed` is the per-tap feedback class (see tap()).
          className={
            [
              k.group ? `kb-${k.group}` : '',
              flashedKey === k.bytes ? 'flashed' : '',
            ]
              .filter(Boolean)
              .join(' ') || undefined
          }
        >{k.label}</button>
      ))}
      <button
        className="mobile-keybar-hide"
        onClick={onHide}
        aria-label="Hide on-screen keys"
        title="Hide"
      >▾</button>
    </div>
  );
}
