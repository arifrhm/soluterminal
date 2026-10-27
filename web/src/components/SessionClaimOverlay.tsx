import { currentRoutingScope } from '../api';
import { terminalSessionKey, effectiveVisiblePaneIds, useStore } from '../store';

// Single session-level claim chip. Renders once over the grid when ANY
// pane reports owned=false, so the user sees one unified "take over"
// control rather than one-per-pane. Deliberately NOT a blocking modal:
// watching another device drive is a legitimate long-lived state, and
// each locked pane already blocks its own pointer/keys via
// .pane-state-input-locked — a full-screen scrim added nothing but got
// in the way of reading the streams. One tap fires claim on every pane
// this browser has connected.
export function SessionClaimOverlay() {
  const claimAllPanes = useStore((s) => s.claimAllPanes);
  // Only consider panes in the CURRENT layout. With warm-pane keep-alive,
  // parked panes from other workspaces keep their paneOwnership entries
  // alive — a phone taking over a backgrounded pane would otherwise throw
  // the "Session in use on another device" modal over the workspace the
  // user is actually looking at, where nothing changed hands. Boolean
  // selector keeps the subscription referentially stable.
  const anyUnowned = useStore((s) => {
    for (const id of effectiveVisiblePaneIds(s)) {
      if (s.paneOwnership[terminalSessionKey(currentRoutingScope(),id)] === false) return true;
    }
    return false;
  });
  // Truthful chip copy: if at least one locked pane is genuinely held by a
  // peer device, keep the "another device" wording; when every locked pane
  // is merely UNOWNED (daemon restarted / controller disconnected — the
  // owner:"none" provenance), saying "input is on another device" is false
  // and sends the user hunting for a phantom phone. Boolean selector keeps
  // the subscription referentially stable.
  const anyHeldByPeer = useStore((s) => {
    for (const id of effectiveVisiblePaneIds(s)) {
      if (s.paneOwnership[terminalSessionKey(currentRoutingScope(),id)] === false && s.paneOwnerState[terminalSessionKey(currentRoutingScope(),id)] !== 'none') return true;
    }
    return false;
  });
  if (!anyUnowned) return null;
  return (
    <div className="claim-chip" role="status" aria-live="polite">
      <span className="claim-chip-dot" aria-hidden="true" />
      <span className="claim-chip-text">
        {anyHeldByPeer
          ? 'Typing is on another device. You can watch here.'
          : 'Typing is paused on this screen.'}
      </span>
      <button
        type="button"
        className="claim-chip-btn"
        title="Send your keystrokes from this screen"
        onClick={() => claimAllPanes()}
      >Type here</button>
    </div>
  );
}

