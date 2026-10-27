// Persistent DOM hosts for terminal panes.
//
// Pane components (xterm + WebSocket) render through React portals into
// these host divs instead of directly into the layout tree. The layout
// tree renders lightweight PaneSlot placeholders that adopt the host
// div on mount and park it on unmount. Because the portal target never
// changes, React never unmounts the Pane component when the slot moves
// or disappears — the xterm instance, its scrollback, and the live
// WebSocket all survive workspace switches, zoom toggles, and layout
// rearranges. Switching back is a plain DOM reattach: no daemon replay,
// no SIGWINCH storm, no refit choreography.
//
// Parked hosts live in a hidden "parking lot" element so they stay in
// the document (xterm keeps parsing incoming bytes; its renderer
// auto-pauses while the element isn't visible and runs a full refresh
// when it becomes visible again).

const hosts = new Map<string, HTMLDivElement>();
const leases = new Map<string, symbol>();
export function claimPaneHostPlacement(key: string): symbol { const lease = Symbol(key); leases.set(key,lease); return lease; }
export function ownsPaneHostPlacement(key: string, lease: symbol): boolean { return leases.get(key) === lease; }
let parkingLot: HTMLDivElement | null = null;

function ensureParkingLot(): HTMLDivElement {
  if (parkingLot && parkingLot.isConnected) return parkingLot;
  const lot = document.createElement('div');
  lot.id = 'solu-pane-parking';
  lot.style.display = 'none';
  document.body.appendChild(lot);
  parkingLot = lot;
  return lot;
}

export function acquirePaneHost(paneId: string): HTMLDivElement {
  let host = hosts.get(paneId);
  if (!host) {
    host = document.createElement('div');
    host.className = 'pane-host';
    hosts.set(paneId, host);
    // New hosts start parked so the portal has a connected target even
    // before (or without) a slot adopting it.
    ensureParkingLot().appendChild(host);
  }
  return host;
}

// When a slot parks a host that currently holds the keyboard caret, moving the
// host into the (display:none) parking lot blurs the focused element to <body>.
// In a layout REFLOW (an ARIA/MCP graft re-nesting the tree) the same pane's
// host is re-adopted by a new slot in the SAME commit, so we hand the focused
// element off here and let restoreParkBlur() put the caret back on re-adopt —
// before any background pane's focus effect can mistake the blur for "stopped
// typing" and steal it. Cleared on a microtask so a genuine park-and-stay
// (workspace switch) never restores stale focus later.
let parkBlur: { paneId: string; el: HTMLElement } | null = null;

/** Move a pane's host back to the hidden parking lot (slot unmounted,
 *  pane stays warm). No-op if the host doesn't exist. */
export function parkPaneHost(paneId: string, lease?: symbol): void {
  if (lease && leases.get(paneId) !== lease) return;
  leases.delete(paneId);
  const host = hosts.get(paneId);
  if (!host) return;
  const ae = document.activeElement as HTMLElement | null;
  if (ae && ae !== document.body && host.contains(ae)) {
    parkBlur = { paneId, el: ae };
    queueMicrotask(() => { if (parkBlur && parkBlur.paneId === paneId) parkBlur = null; });
  }
  ensureParkingLot().appendChild(host);
  // Tell the Pane it just went OFF-SCREEN (parked to the hidden lot). It reports
  // visibility:false to the daemon so a background-workspace / background-window
  // tile stops voting in the shared-PTY sizing election (desiredSizeLocked) —
  // otherwise a parked narrow tile pins an actively-viewed pane narrow. Mirror
  // of PaneSlot's 'solu:pane-reattach' (on-screen) event.
  //
  // Sent on a microtask, and only if the host is still parked: a same-commit
  // reflow (opening or closing a tile re-nests the layout) parks and re-adopts
  // every host in one go. Reporting that instant off-screen made the daemon
  // hand the PTY to another viewer's size and, on the hidden→visible
  // transition, back again: two resizes that left the screen blank.
  queueMicrotask(() => {
    if (!isPaneHostParked(paneId)) return;
    window.dispatchEvent(new CustomEvent('solu:pane-park', { detail: { paneId, sessionKey: paneId } }));
  });
}

/** Is this pane's host currently parked in the hidden lot (off-screen)? Used to
 *  seed a Pane's initial on-screen state for per-pane visibility. */
export function isPaneHostParked(paneId: string): boolean {
  const host = hosts.get(paneId);
  return !!host && host.parentElement?.id === 'solu-pane-parking';
}

/** Called by a slot right after it re-adopts a host. If this pane's host was
 *  just parked while holding the caret (a same-commit reflow) and the move
 *  blurred it to <body>, restore the caret to the original element. */
export function restoreParkBlur(paneId: string): void {
  const pend = parkBlur;
  if (!pend || pend.paneId !== paneId) return;
  parkBlur = null;
  if (pend.el.isConnected && document.activeElement === document.body) {
    try { pend.el.focus({ preventScroll: true }); } catch { /* not fatal */ }
  }
}

/** Drop a pane's host entirely (pane killed / evicted from the warm
 *  set / backend switched). Call AFTER the portal unmounted so xterm's
 *  dispose ran against a still-connected element. */
export function releasePaneHost(paneId: string): void {
  const host = hosts.get(paneId);
  if (!host) return;
  host.remove();
  hosts.delete(paneId);
  leases.delete(paneId);
}

/** Test/debug introspection. */
export function paneHostCount(): number {
  return hosts.size;
}
