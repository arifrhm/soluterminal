import type { PaneInfo } from '../types';
import { effectivePaneAgent } from '../lib/agentKind';
import { paneAgentRssMB } from '../lib/agentMemory';
import { confirmDialog } from '../lib/dialog';

function memoryLabel(mb: number) {
  return mb >= 1024 ? `${(mb / 1024).toFixed(1)} GB` : `${mb} MB`;
}

export function AgentMemoryChip({ pane, onReclaim }: {
  pane: PaneInfo;
  onReclaim: () => void;
}) {
  const mb = paneAgentRssMB(pane);
  if (!(mb > 0)) return null;

  const label = memoryLabel(mb);
  const heavy = mb >= 500;
  const conversationKind = effectivePaneAgent(pane) === 'codex' ? 'thread' : 'session';
  return (
    <button
      className={`iconbtn pane-mem-chip${heavy ? ' heavy' : ''}`}
      title={`Agent memory ${label}${heavy ? ' — heavy.' : '.'} Click to restart this agent and reclaim it (your conversation is kept).`}
      aria-label={`Agent memory ${label}. Click to restart and reclaim.`}
      onClick={async (e) => {
        e.stopPropagation();
        if (
          await confirmDialog({
            message: `Restart this agent to reclaim ${label} of memory?\n\nYour conversation is kept — it resumes the same ${conversationKind}. The agent will be briefly unavailable while it restarts.`,
            confirmLabel: 'Restart & reclaim',
          })
        ) {
          onReclaim();
        }
      }}
      onDoubleClick={(e) => e.stopPropagation()}
    >
      {label}
    </button>
  );
}
