import { useEffect, useState } from 'react';
import { api } from '../api';
import type { BrowsedSession } from '../types';
import { formatAgo } from '../lib/turnTime';

// "Resume a past session" list for the New Pane dialog: recent Claude Code /
// Codex conversations started in the chosen folder, with their title and first
// prompt, one click to resume. Complements the `claude --resume` chip (which
// opens Claude's own picker inside the terminal): this one is searchable at a
// glance, covers Codex too, and can include headless / Focus runs that
// Claude's picker hides.

interface Props {
  cwd: string;
  disabled?: boolean;
  onResume: (s: BrowsedSession) => void;
}

const shownByDefault = 5;

export function PastSessions({ cwd, disabled, onResume }: Props) {
  const [sessions, setSessions] = useState<BrowsedSession[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [automated, setAutomated] = useState(false);
  const [expanded, setExpanded] = useState(false);

  useEffect(() => {
    const dir = cwd.trim();
    setExpanded(false);
    if (!dir || !(dir.startsWith('/') || dir.startsWith('~') || /^[A-Za-z]:[\\/]/.test(dir))) {
      setSessions(null);
      return;
    }
    let cancelled = false;
    setLoading(true);
    const h = window.setTimeout(() => {
      api.recentSessions(dir, automated)
        .then((r) => { if (!cancelled) setSessions(r.sessions ?? []); })
        .catch(() => { if (!cancelled) setSessions(null); })
        .finally(() => { if (!cancelled) setLoading(false); });
    }, 300);
    return () => { cancelled = true; window.clearTimeout(h); };
  }, [cwd, automated]);

  if (!sessions && !loading) return null;
  const list = sessions ?? [];
  const shown = expanded ? list : list.slice(0, shownByDefault);

  return (
    <div className="past-sessions">
      <div className="past-sessions-head">
        <label>Resume a past session</label>
        <label className="past-sessions-auto" title="Also list headless runs (workflow stages, Focus, codex exec)">
          <input type="checkbox" checked={automated} onChange={(e) => setAutomated(e.target.checked)} />
          include automated runs
        </label>
      </div>
      {loading && list.length === 0 && <div className="dialog-help">Looking for sessions in this folder…</div>}
      {!loading && list.length === 0 && <div className="dialog-help">No Claude or Codex sessions started in this folder yet.</div>}
      {shown.length > 0 && (
        <div className="past-sessions-list" role="list">
          {shown.map((s) => (
            <button
              type="button"
              role="listitem"
              key={`${s.agent}:${s.id}`}
              className="past-session-row"
              disabled={disabled}
              title={`${s.firstPrompt ?? ''}\n\nResume ${s.agent === 'codex' ? 'Codex thread' : 'Claude session'} ${s.id}`}
              onClick={() => onResume(s)}
            >
              <span className={`past-session-agent ${s.agent}`}>{s.agent === 'codex' ? 'Codex' : 'Claude'}</span>
              <span className="past-session-text">
                <span className="past-session-title">{s.title || s.firstPrompt || s.id}</span>
                {s.title && s.firstPrompt && <span className="past-session-prompt">{s.firstPrompt}</span>}
              </span>
              <span className="past-session-when">{s.automated ? 'auto · ' : ''}{formatAgo(s.updatedMs)}</span>
            </button>
          ))}
        </div>
      )}
      {list.length > shownByDefault && (
        <button type="button" className="dialog-chip past-sessions-more" onClick={() => setExpanded((v) => !v)}>
          {expanded ? 'Show fewer' : `Show ${list.length - shownByDefault} more`}
        </button>
      )}
    </div>
  );
}

export function pastSessionTitle(s: BrowsedSession): string {
  const base = s.title || s.firstPrompt || (s.agent === 'codex' ? 'codex' : 'claude');
  return base.length > 40 ? base.slice(0, 39) + '…' : base;
}
