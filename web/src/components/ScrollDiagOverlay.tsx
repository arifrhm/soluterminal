import { useEffect, useState } from 'react';
import {
  getScrollDiagEvents,
  isScrollDiagVisible,
  subscribeScrollDiag,
  toggleScrollDiag,
  type ScrollDiagEvent,
} from '../scrollDiag';
import { isSoluDebugEnabled } from '../debug';

// Floating diagnostic overlay for the Pane.tsx scroll-preservation logic.
// Toggled with Alt+Shift+D or the "diag" button in the status bar.
// Renders the ring buffer of logged events as a short timeline, with a
// Copy button so the trace can be pasted back without retyping.
export function ScrollDiagOverlay() {
  const [, force] = useState(0);
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'err'>('idle');

  useEffect(() => {
    const bump = () => force((n) => (n + 1) % 1_000_000);
    const unsub = subscribeScrollDiag(bump);
    const onKey = (e: KeyboardEvent) => {
      // Dev builds, or support sessions with localStorage solu.debug=1 only:
      // in a normal install Alt+Shift+D belongs to the terminal.
      if (!isSoluDebugEnabled()) return;
      if (e.altKey && e.shiftKey && (e.key === 'D' || e.key === 'd' || e.code === 'KeyD')) {
        e.preventDefault();
        e.stopPropagation();
        toggleScrollDiag();
      }
    };
    // CAPTURE phase on document beats any focused element's own handler
    // (xterm's textarea in particular stops propagation on most keys).
    document.addEventListener('keydown', onKey, true);
    return () => {
      unsub();
      document.removeEventListener('keydown', onKey, true);
    };
  }, []);

  if (!isScrollDiagVisible()) return null;
  const events = getScrollDiagEvents();
  const firstT = events[0]?.t ?? 0;

  const formatEvent = (ev: ScrollDiagEvent): string => {
    const dt = (ev.t - firstT).toFixed(0);
    const parts: string[] = [];
    if (ev.ydisp !== undefined) parts.push(`yd=${ev.ydisp}`);
    if (ev.length !== undefined) parts.push(`len=${ev.length}`);
    if (ev.rows !== undefined) parts.push(`r=${ev.rows}`);
    if (ev.cols !== undefined) parts.push(`c=${ev.cols}`);
    if (ev.scrollTop !== undefined) parts.push(`sT=${Math.round(ev.scrollTop)}`);
    if (ev.scrollHeight !== undefined) parts.push(`sH=${ev.scrollHeight}`);
    if (ev.clientHeight !== undefined) parts.push(`cH=${ev.clientHeight}`);
    if (ev.saved) parts.push(`saved{yd=${ev.saved.viewportY} sT=${Math.round(ev.saved.scrollTop)} atBot=${ev.saved.wasAtBottom ? 1 : 0}}`);
    if (ev.note) parts.push(ev.note);
    return `+${dt.padStart(5)}ms ${ev.label} ${ev.paneId.slice(0, 8)} ${parts.join(' ')}`;
  };

  const copy = async () => {
    const lines = events.map(formatEvent).join('\n');
    try {
      await navigator.clipboard.writeText(lines);
      setCopyState('copied');
      setTimeout(() => setCopyState('idle'), 1200);
    } catch {
      // navigator.clipboard may fail in non-HTTPS / non-focused contexts.
      // Fall back to a hidden textarea + document.execCommand('copy').
      try {
        const ta = document.createElement('textarea');
        ta.value = lines;
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        document.execCommand('copy');
        document.body.removeChild(ta);
        setCopyState('copied');
        setTimeout(() => setCopyState('idle'), 1200);
      } catch {
        setCopyState('err');
        setTimeout(() => setCopyState('idle'), 1500);
      }
    }
  };

  const btn: React.CSSProperties = {
    background: 'transparent',
    border: '1px solid #3a4a60',
    color: '#d0e0ff',
    fontFamily: 'inherit',
    fontSize: 10,
    padding: '1px 6px',
    borderRadius: 3,
    cursor: 'pointer',
  };

  return (
    <div
      style={{
        position: 'fixed',
        top: 44,
        right: 8,
        zIndex: 9999,
        width: 400,
        maxHeight: '70vh',
        overflowY: 'auto',
        background: 'rgba(10, 14, 20, 0.94)',
        border: '1px solid #3a4a60',
        borderRadius: 4,
        color: '#d0e0ff',
        fontFamily: 'SF Mono, Menlo, monospace',
        fontSize: 10,
        padding: 6,
        lineHeight: 1.35,
        userSelect: 'text',         // allow drag-select for manual copy
        WebkitUserSelect: 'text',
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 4 }}>
        <strong style={{ flex: 1 }}>scroll diag (Alt+Shift+D)</strong>
        <span style={{ color: '#8aa0c0', fontSize: 9 }}>{events.length} events</span>
        <button type="button" style={btn} onClick={copy} title="Copy all events to clipboard">
          {copyState === 'copied' ? '✓ copied' : copyState === 'err' ? '✗ err' : 'copy'}
        </button>
        <button type="button" style={btn} onClick={toggleScrollDiag} title="Close overlay">×</button>
      </div>
      {events.length === 0 && (
        <div style={{ color: '#8aa0c0' }}>No events yet. Drag a resize handle.</div>
      )}
      {events.map((ev, i) => {
        const dt = (ev.t - firstT).toFixed(0);
        const parts: string[] = [];
        if (ev.ydisp !== undefined) parts.push(`yd=${ev.ydisp}`);
        if (ev.length !== undefined) parts.push(`len=${ev.length}`);
        if (ev.rows !== undefined) parts.push(`r=${ev.rows}`);
        if (ev.cols !== undefined) parts.push(`c=${ev.cols}`);
        if (ev.scrollTop !== undefined) parts.push(`sT=${Math.round(ev.scrollTop)}`);
        if (ev.scrollHeight !== undefined) parts.push(`sH=${ev.scrollHeight}`);
        if (ev.clientHeight !== undefined) parts.push(`cH=${ev.clientHeight}`);
        if (ev.saved) parts.push(`saved{yd=${ev.saved.viewportY} sT=${Math.round(ev.saved.scrollTop)} atBot=${ev.saved.wasAtBottom ? 1 : 0}}`);
        if (ev.note) parts.push(ev.note);
        return (
          <div key={i} style={{ whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
            <span style={{ color: '#8aa0c0' }}>+{dt.padStart(5)}ms </span>
            <span style={{ color: '#a0f0c0' }}>{ev.label}</span>
            <span style={{ color: '#6a7a90' }}> {ev.paneId.slice(0, 8)}</span>{' '}
            <span>{parts.join(' ')}</span>
          </div>
        );
      })}
    </div>
  );
}
