import { useState } from 'react';
import { useStore } from '../store';
import { collectPaneIds } from '../lib/layout';
import type { PaneInfo } from '../types';

type RecoveryKind = 'folder' | 'duplicate' | 'gone' | 'other';

export function recoveryKind(error: string | undefined): { kind: RecoveryKind; ownerId?: string } {
  const msg = error ?? '';
  if (/working directory (does not exist|is not a directory)/.test(msg)) return { kind: 'folder' };
  const dup = /conversation is already running in pane (\S+)/.exec(msg);
  if (dup) return { kind: 'duplicate', ownerId: dup[1] };
  if (/no longer resumable/.test(msg)) return { kind: 'gone' };
  return { kind: 'other' };
}

// A lost folder is usually a deleted checkout or a temporary folder cleared on
// restart, often named after its project (/tmp/<repo>-<topic>). Prefer a folder
// this workspace's working panes use whose name appears in the lost path, then
// the most used one. Claude and Codex both resume by id from any folder
// (verified with Claude Code 2.1.310, interactive and headless). Returns a
// string so the store selector stays referentially stable.
export function suggestedFolder(s: Pick<ReturnType<typeof useStore.getState>, 'layout' | 'panes'>, self: string, lost: string): string {
  const counts = new Map<string, number>();
  for (const id of s.layout ? collectPaneIds(s.layout) : []) {
    const p = s.panes[id];
    if (!p || id === self || p.recoveryError || !p.cwd) continue;
    counts.set(p.cwd, (counts.get(p.cwd) ?? 0) + 1);
  }
  const named = (cwd: string) => {
    const base = cwd.replace(/[\\/]+$/, '').split(/[\\/]/).pop() ?? '';
    return base.length >= 3 && lost.toLowerCase().includes(base.toLowerCase());
  };
  let best = '~';
  let score = 0;
  for (const [cwd, c] of counts) {
    const v = c + (named(cwd) ? 1000 : 0);
    if (v > score) { best = cwd; score = v; }
  }
  return best;
}

export function PaneRecovery({ pane }: { pane: PaneInfo }) {
  const resumePane = useStore((s) => s.resumePane);
  const closePane = useStore((s) => s.closePaneInLayout);
  const revealPane = useStore((s) => s.revealPane);
  const { kind, ownerId } = recoveryKind(pane.recoveryError);
  const suggestion = useStore((s) => suggestedFolder(s, pane.id, pane.cwd));
  const ownerTitle = useStore((s) => (ownerId ? s.panes[ownerId]?.title ?? '' : ''));
  // The duplicate error is cached from the last attempt. 'closed' only when this
  // window can see the owner has stopped (the daemon would then allow this copy);
  // an owner it can't see (another backend) is treated as still open.
  const owner = useStore((s): 'open' | 'closed' | 'unknown' => {
    const o = ownerId ? s.panes[ownerId] : undefined;
    if (!o) return 'unknown';
    return o.status === 'running' && !o.recoveryError ? 'open' : 'closed';
  });
  const ownerOpen = owner === 'open';
  const [confirmCloseAll, setConfirmCloseAll] = useState(false);
  const stuckIds = useStore((s) => (s.layout ? collectPaneIds(s.layout).filter((id) => s.panes[id]?.recoveryError) : []).join(' '));
  const [retrying, setRetrying] = useState(false);
  // Until the user types, the folder follows the failure: a retry that turns
  // a generic failure into a missing-folder one switches to the suggestion.
  const [edited, setCwd] = useState<string | null>(null);
  const cwd = edited ?? (kind === 'folder' ? suggestion : pane.cwd);
  const chooseConversation = pane.identityBlocked ||
    ((pane.agent === 'codex' || pane.agent === 'claude') && !pane.codexThreadId && !pane.claudeSessionId);
  const stuck = stuckIds ? stuckIds.split(' ') : [];

  const retry = async () => {
    setRetrying(true);
    try { await resumePane(pane.id, pane.cols, pane.rows, cwd); }
    catch { /* Store displays the current recovery error. */ }
    finally { setRetrying(false); }
  };

  const heading = kind === 'folder' ? "This pane's folder is gone"
    : kind === 'duplicate' ? 'This conversation is already open'
    : kind === 'gone' ? "This conversation can't be reopened"
    : "This pane couldn't be restored";

  return (
    <div className="pane">
      <div className="pane-header">
        <span className="pane-title">{pane.title || 'Saved pane'}</span>
        <div className="pane-actions">
          <button className="iconbtn" aria-label="Close saved pane" onClick={() => closePane(pane.id)}>×</button>
        </div>
      </div>
      <div className="pane-recovery">
        <strong className="pane-recovery-title">{heading}</strong>
        {kind === 'folder' && (
          <p>
            <code>{pane.cwd}</code> no longer exists. It may have been deleted, or it was a temporary
            folder that was cleared when the computer restarted. The conversation is saved, so you can
            reopen it in another folder. Files that were only in the old folder can't be brought back.
          </p>
        )}
        {kind === 'duplicate' && (
          <p>
            {ownerOpen
              ? <>It's running in another pane{ownerTitle ? <> (<strong>{ownerTitle}</strong>)</> : null}, so this copy can't open too.</>
              : owner === 'closed'
                ? 'Another pane had it open. That pane has since closed, so you can reopen the conversation here.'
                : "Another pane has it open, so this copy can't open too."}
          </p>
        )}
        {kind === 'gone' && <p>Its saved history is no longer on this computer, so there's nothing to reopen.</p>}
        <p role="alert" className="pane-recovery-detail">{pane.recoveryError}</p>
        {(kind === 'folder' || kind === 'other') && (
          <label className="pane-recovery-field">
            {kind === 'folder' ? 'Reopen in folder' : 'Working directory'}
            <input value={cwd} onChange={(e) => setCwd(e.target.value)} disabled={retrying} spellCheck={false} />
          </label>
        )}
        {kind === 'folder' && edited === null && (
          <p className="pane-recovery-detail">Suggested from the other panes in this workspace. Check it's the right project before reopening.</p>
        )}
        <div className="pane-recovery-actions">
          {kind === 'duplicate' ? (
            <>
              {ownerOpen && ownerId && <button className="primary" onClick={() => void revealPane(ownerId)}>Go to that pane</button>}
              {owner === 'closed' && <button className="primary" disabled={retrying} onClick={retry}>{retrying ? 'Restoring…' : 'Reopen here'}</button>}
              <button className={owner === 'unknown' ? 'primary' : 'secondary'} onClick={() => closePane(pane.id)}>Close this copy</button>
              {owner === 'unknown' && <button className="secondary" disabled={retrying} onClick={retry}>{retrying ? 'Restoring…' : 'Try again'}</button>}
            </>
          ) : kind === 'gone' ? (
            <>
              <button className="primary" onClick={() => closePane(pane.id)}>Close pane</button>
              <button className="secondary" disabled={retrying} onClick={retry}>{retrying ? 'Restoring…' : 'Try again'}</button>
            </>
          ) : (
            <button className="primary" disabled={retrying || !cwd.trim()} onClick={retry}>
              {retrying ? 'Restoring…' : chooseConversation ? 'Choose original conversation' : kind === 'folder' ? 'Reopen here' : 'Retry restore'}
            </button>
          )}
          {stuck.length > 1 && (confirmCloseAll ? (
            <>
              <button className="danger" onClick={() => stuck.forEach((id) => closePane(id))}>Close {stuck.length} panes</button>
              <button className="secondary" onClick={() => setConfirmCloseAll(false)}>Keep them</button>
            </>
          ) : (
            <button className="secondary" onClick={() => setConfirmCloseAll(true)}>
              Close all {stuck.length} panes that need recovery
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}
