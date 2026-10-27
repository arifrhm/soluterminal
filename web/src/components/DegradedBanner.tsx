import { useMemo, useState } from 'react';
import { api } from '../api';
import { useStore } from '../store';

// Surfaces when one or more panes are "degraded": their running `claude`
// inherited a nested child-session env (the daemon was launched from inside a
// claude session) and therefore never writes a transcript — so the
// conversation / Activity view is permanently empty until the pane is
// restarted. The flag is computed live by the daemon (degraded on the
// /api/panes DTO); this banner only renders when at least one pane reports it,
// so unaffected users never see it.
//
// "Fix in place" respawns Claude clean in the SAME panes (loses the unsaved
// chat — which was never recorded anyway — but keeps positions and clears the
// warning). "Open clean copies" spawns clean siblings and leaves the originals
// (non-destructive; the warning persists until the user closes the old panes).
export function DegradedBanner() {
  const panes = useStore((s) => s.panes);
  const refreshPanes = useStore((s) => s.refreshPanes);
  const [dismissed, setDismissed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [confirming, setConfirming] = useState(false);

  const n = useMemo(
    () => Object.values(panes).filter((p) => p.degraded).length,
    [panes],
  );

  // Always render the wrapper so it keeps occupying its own grid row in the
  // .app layout (grid-template-rows: auto 36px 1fr). Returning null here would
  // drop the row and shift the Topbar/pane-grid into the wrong rows.
  const show = n > 0 && !dismissed;

  const run = async (mode: 'inplace' | 'sibling') => {
    setBusy(true);
    try {
      await api.migrateCleanAll({ dryRun: false, mode, confirmAll: mode === 'inplace' });
      // The migration runs staggered in the background server-side; refresh a
      // couple of times so the cleared (inplace) / added (sibling) panes show.
      setTimeout(() => void refreshPanes(), 1500);
      setTimeout(() => void refreshPanes(), 5000);
    } catch {
      // Leave the banner up so the user can retry.
    } finally {
      setBusy(false);
      setConfirming(false);
    }
  };

  const plural = n > 1 ? 's' : '';

  return (
    <div className="degraded-banner-row">
      {show && (
    <div className="degraded-banner" role="alert">
      <span className="degraded-banner-dot" aria-hidden />
      <span className="degraded-banner-text">
        <strong>
          {n} session{plural} not recording activity
        </strong>
        <span className="degraded-banner-sub">
          {' · Claude started nested and isn’t saving a transcript'}
        </span>
      </span>
      {confirming ? (
        <span className="degraded-banner-actions">
          <span className="degraded-banner-warn">
            Restarts Claude in {n} pane{plural}; unsaved chat (never recorded) is lost.
          </span>
          <button
            type="button"
            className="degraded-banner-btn danger"
            disabled={busy}
            onClick={() => void run('inplace')}
          >
            {busy ? 'Fixing…' : 'Confirm'}
          </button>
          <button
            type="button"
            className="degraded-banner-btn"
            disabled={busy}
            onClick={() => setConfirming(false)}
          >
            Cancel
          </button>
        </span>
      ) : (
        <span className="degraded-banner-actions">
          <button
            type="button"
            className="degraded-banner-btn primary"
            disabled={busy}
            onClick={() => setConfirming(true)}
            title="Restart Claude clean in the same panes"
          >
            Fix in place
          </button>
          <button
            type="button"
            className="degraded-banner-btn"
            disabled={busy}
            onClick={() => void run('sibling')}
            title="Open clean copies beside them, keep the originals"
          >
            {busy ? 'Working…' : 'Open clean copies'}
          </button>
          <button
            type="button"
            className="degraded-banner-btn ghost"
            title="Dismiss"
            onClick={() => setDismissed(true)}
          >
            ×
          </button>
        </span>
      )}
    </div>
      )}
    </div>
  );
}
