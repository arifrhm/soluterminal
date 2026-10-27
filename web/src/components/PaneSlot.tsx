import { currentRoutingScope } from '../api';
import { sessionKey, invalidateSessionPlacement } from '../lib/terminalViewSessions';
import { useStore } from '../store';
import { useLayoutEffect, useRef } from 'react';
import { acquirePaneHost, parkPaneHost, restoreParkBlur, claimPaneHostPlacement, ownsPaneHostPlacement } from '../lib/paneHosts';

// PaneSlot is the layout tree's placeholder for a warm pane: it adopts
// the pane's persistent host div on mount and parks it on unmount. The
// actual Pane component is rendered by WarmPaneLayer through a portal
// into that host, so moving a slot (workspace switch, zoom, drag
// rearrange) is a DOM move — the terminal never remounts.
export function PaneSlot({ paneId, sessionKey: explicitKey, presentationActive = true }: { paneId: string; sessionKey?: string; presentationActive?: boolean }) {
  const view = useStore(s => s.mainPresentation.kind);
  const key = explicitKey ?? sessionKey(currentRoutingScope(),paneId);
  const active = presentationActive && (explicitKey !== undefined || view === 'workspace');
  const ref = useRef<HTMLDivElement>(null);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || !active) return;
    const lease = claimPaneHostPlacement(key);
    invalidateSessionPlacement(key);
    const host = acquirePaneHost(key);
    // Was the host sitting in the hidden parking lot (i.e. this pane was
    // warm but off-screen — e.g. its workspace wasn't loaded)? Only then
    // does it need a reattach repaint. A host that was already in another
    // visible slot (drag-rearrange, zoom) was never hidden, so firing a
    // refresh there would be wasted work. See Pane's 'solu:pane-reattach'
    // handler — xterm's auto-refresh-on-visible isn't reliable for the
    // display:none -> visible move, leaving stale cells + a stuck scroll.
    const wasParked = host.parentElement?.id === 'solu-pane-parking';
    el.appendChild(host);
    // If this host was just parked (same-commit reflow) while holding the caret,
    // the park blurred it to <body>; put the caret back so a background graft's
    // focus effect can't read the blur as "stopped typing" and steal it.
    restoreParkBlur(key);
    if (wasParked) {
      window.dispatchEvent(new CustomEvent('solu:pane-reattach', { detail: { paneId, sessionKey:key } }));
    }
    return () => {
      // Back to the hidden parking lot — NOT released. WarmPaneLayer
      // owns the host's lifetime; the slot only owns its placement.
      if (ownsPaneHostPlacement(key,lease)) {
        invalidateSessionPlacement(key);
        parkPaneHost(key,lease);
      }
    };
  }, [key, active]);

  return <div className="pane-slot" ref={ref} />;
}
