import { useStore } from '../store';

// Full-screen overlay shown only after a self-update swap is accepted.
// A checked/prepared request that receives a 409 hold never mounts it.
// The store verifies every SSE hint against fresh status and shares the same
// bounded recovery controller with accepted HTTP/status admission signals, so
// one lost or replayed event can neither mount nor strand this surface.
export function UpdateOverlay() {
  const status = useStore((s) => s.updateStatus);
  if (!status?.applying || status.workflowDeferred) return null;
  const target = status.latest || '';
  return (
    <div className="update-overlay" role="dialog" aria-live="polite" aria-busy="true">
      <div className="update-overlay-card">
        <div className="update-overlay-spinner" aria-hidden>⟳</div>
        <div className="update-overlay-title">
          Updating{target ? ` to ${target}` : ''}…
        </div>
        <div className="update-overlay-detail">
          The daemon activates automatically at the next bounded safe boundary.
          Active model calls are allowed to finish and are never force-killed. Open
          shells stay attached; a pane whose agent needs a restart shows a small
          dot in its header after this window reconnects.
        </div>
      </div>
    </div>
  );
}
