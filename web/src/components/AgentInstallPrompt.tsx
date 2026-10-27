import { useEffect, useId, useRef } from 'react';
import { useFocusTrap } from './useFocusTrap';
import { useClaudeInstall, ClaudeInstallConfirmCard } from '../claudeBootstrap';
import { useCodexInstall } from '../codexBootstrap';
import { AGENT_NAMES, useAgentInstallPrompt } from '../agentStart';

// Opened by a Start button (agentStart.ts) when its agent isn't installed.
// It reuses the checklist's install flow as is: Install fetches the exact
// command, the same "Run … installer?" card shows it, and only that card's
// button opens the installer pane.

export function agentMissingCopy(name: string): string {
  return `${name} isn’t installed yet.`;
}

export function AgentInstallPrompt() {
  const agent = useAgentInstallPrompt((s) => s.agent);
  const close = useAgentInstallPrompt((s) => s.close);
  const open = agent !== null;
  const cardRef = useRef<HTMLDivElement>(null);
  const headingRef = useRef<HTMLHeadingElement>(null);
  const titleId = useId();
  useFocusTrap(cardRef, open);
  useEffect(() => { if (open) headingRef.current?.focus(); }, [open]);

  const claudeFlow = useClaudeInstall({ onSpawn: close });
  const codexFlow = useCodexInstall({ onSpawn: close });

  if (!agent) return null;
  const name = AGENT_NAMES[agent];
  const flow = agent === 'claude' ? claudeFlow : codexFlow;
  const dismiss = () => {
    flow.cancelInstall();
    close();
  };

  return (
    <div className="dialog-backdrop" onMouseDown={dismiss}>
      <div
        ref={cardRef}
        className="dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        data-agent-install={agent}
        onKeyDown={(e) => {
          e.stopPropagation();
          if (e.key === 'Escape') { e.preventDefault(); dismiss(); }
        }}
        onMouseDown={(e) => e.stopPropagation()}
        style={{ width: 440, maxWidth: '92vw' }}
      >
        <h2 id={titleId} ref={headingRef} tabIndex={-1} style={{ margin: 0, fontSize: 16 }}>
          {agentMissingCopy(name)}
        </h2>
        <div style={{ marginTop: 6, fontSize: 12, color: 'var(--fg-dim)', lineHeight: 1.5 }}>
          Install it first. The installer runs in a new pane, and you can start {name} when it’s done.
        </div>
        {flow.pendingInstall ? (
          <ClaudeInstallConfirmCard
            agentName={name}
            spec={flow.pendingInstall}
            onConfirm={flow.confirmInstall}
            onCancel={dismiss}
          />
        ) : (
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 14 }}>
            <button type="button" onClick={dismiss} style={{ fontSize: 12, padding: '4px 12px' }}>
              Cancel
            </button>
            <button
              type="button"
              className="primary"
              onClick={flow.startInstall}
              disabled={flow.installing}
              style={{ fontSize: 12, padding: '4px 14px' }}
            >
              {flow.installing ? 'Preparing…' : 'Install'}
            </button>
          </div>
        )}
      </div>
    </div>
  );
}
