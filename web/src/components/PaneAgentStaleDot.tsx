import type { PaneInfo } from '../types';
import { confirmDialog } from '../lib/dialog';
import { restartPaneFlow } from '../lib/paneRestart';

export function paneAgentStaleTitle(pane: Pick<PaneInfo, 'agentVersion'>): string {
  const running = pane.agentVersion ? `v${pane.agentVersion}` : 'an older build';
  return `Running ${running} · restart pane to update`;
}

// Quiet per-pane signal that this pane's agent process speaks an older agent
// protocol than the daemon, so a restart would actually change its behavior.
// Daemon-only releases never set it. Click → confirm → restart just this pane
// via the shared restart flow (which handles the busy / needs-fresh 409s).
export function PaneAgentStaleDot({ pane }: { pane: PaneInfo }) {
  if (!pane.agentStale) return null;
  const title = paneAgentStaleTitle(pane);
  return (
    <button
      type="button"
      className="pane-agent-stale-dot"
      title={title}
      aria-label={`${title}. Restart this pane.`}
      onMouseDownCapture={(e) => e.stopPropagation()}
      onDoubleClick={(e) => e.stopPropagation()}
      onClick={(e) => {
        e.stopPropagation();
        void (async () => {
          const ok = await confirmDialog({
            title: 'Restart this pane?',
            message: `${title}. Restarting resumes its conversation on the installed build.`,
            confirmLabel: 'Restart pane',
          });
          if (ok) await restartPaneFlow(pane.id);
        })();
      }}
    />
  );
}
