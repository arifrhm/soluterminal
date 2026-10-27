import { useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import type { PaneInfo } from '../types';
import { useStore } from '../store';
import { api } from '../api';
import { formatAgo } from '../lib/turnTime';
import { useFocusTrap } from './useFocusTrap';

// Name, @handle and "working on" in one place. The handle is how you and the
// agents address the pane ("ask @api-fix …"); numbers move when panes close,
// handles don't. The working-on line is the pane's current status, which its
// agent may also keep up to date.

/** The handle the daemon will store, or '' — mirrors normalizePaneHandle. */
export function normalizeHandle(raw: string): string {
  return raw.trim().replace(/^@/, '').toLowerCase();
}

export function handleProblem(raw: string): string | null {
  const h = normalizeHandle(raw);
  if (!h) return null;
  if (h.length > 24 || !/^[a-z0-9][a-z0-9-]*$/.test(h) || !/[a-z]/.test(h)) {
    return 'Use 1–24 lowercase letters, digits or dashes, with at least one letter.';
  }
  return null;
}

/** "set by #2 Claude, 5m ago" — who wrote the working-on line, when. */
export function workingOnProvenance(pane: PaneInfo): string {
  const id = pane.identity;
  if (!id?.workingOn) return '';
  const who = id.workingOnBy === 'user' ? 'you' : id.workingOnBy || 'an agent';
  return id.workingOnAt ? `set by ${who}, ${formatAgo(id.workingOnAt)}` : `set by ${who}`;
}

export function PaneIdentityDialog({ pane, onClose }: { pane: PaneInfo; onClose: () => void }) {
  const renamePane = useStore((s) => s.renamePane);
  const setPaneIdentity = useStore((s) => s.setPaneIdentity);
  const [title, setTitle] = useState(pane.title);
  const [handle, setHandle] = useState(pane.identity?.handle ?? '');
  const [workingOn, setWorkingOn] = useState(pane.identity?.workingOn ?? '');
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [suggesting, setSuggesting] = useState(false);
  // A suggestion that arrives after the dialog closed is dropped.
  const open = useRef(true);
  // Set on mount as well: development mode mounts effects twice.
  useEffect(() => { open.current = true; return () => { open.current = false; }; }, []);
  const suggest = async () => {
    setSuggesting(true);
    setError(null);
    try {
      const { name } = await api.suggestPaneName(pane.id);
      if (open.current) setTitle(name);
    } catch (err) {
      if (open.current) setError((err as Error).message || 'No suggestion.');
    } finally {
      if (open.current) setSuggesting(false);
    }
  };
  const cardRef = useRef<HTMLFormElement>(null);
  useFocusTrap(cardRef, true);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !event.defaultPrevented) onClose();
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [onClose]);

  const problem = handleProblem(handle);
  const save = async () => {
    if (problem) return;
    setSaving(true);
    setError(null);
    try {
      const change: { handle?: string; workingOn?: string } = {};
      const nextHandle = normalizeHandle(handle);
      if (nextHandle !== (pane.identity?.handle ?? '')) change.handle = nextHandle;
      if (workingOn.trim() !== (pane.identity?.workingOn ?? '')) change.workingOn = workingOn.trim();
      // The handle first: a taken one is the likeliest refusal, and then
      // nothing else has changed yet.
      if (Object.keys(change).length) await setPaneIdentity(pane.id, change);
      if (title.trim() !== pane.title) await renamePane(pane.id, title);
      onClose();
    } catch (err) {
      setError((err as Error).message || 'Could not save.');
    } finally {
      setSaving(false);
    }
  };

  const provenance = workingOnProvenance(pane);
  return createPortal(
    <div className="dialog-backdrop" role="presentation" onMouseDown={onClose}>
      <form
        ref={cardRef}
        className="dialog pane-identity-dialog"
        role="dialog"
        aria-modal="true"
        aria-label="Name and handle"
        onSubmit={(event) => { event.preventDefault(); void save(); }}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="dialog-header">
          <h2 className="dialog-title">Name and handle</h2>
        </div>
        <label htmlFor="pane-identity-title">Name</label>
        <div className="pane-identity-name-field">
          <input id="pane-identity-title" value={title} onChange={(e) => setTitle(e.target.value)} maxLength={80} autoFocus disabled={suggesting} />
          <button
            type="button"
            onClick={() => { void suggest(); }}
            disabled={suggesting || saving}
            title="Your own Claude Code (Haiku) reads this pane's Claude or Codex conversation and suggests a name; nothing changes until you save"
          >
            {suggesting ? 'Thinking…' : 'Suggest'}
          </button>
        </div>
        <label htmlFor="pane-identity-handle">Handle</label>
        <div className="pane-identity-handle-field">
          <span aria-hidden="true">@</span>
          <input
            id="pane-identity-handle"
            value={handle}
            onChange={(e) => setHandle(e.target.value)}
            placeholder="api-fix"
            maxLength={25}
            spellCheck={false}
            autoCapitalize="off"
            aria-invalid={!!problem}
            aria-describedby="pane-identity-handle-help"
          />
        </div>
        <div id="pane-identity-handle-help" className={problem ? 'dialog-error' : 'dialog-help'}>
          {problem ?? 'Unique among your panes. You and the agents can say @handle instead of a pane number, which changes when panes close.'}
        </div>
        <label htmlFor="pane-identity-working-on">Working on</label>
        <input
          id="pane-identity-working-on"
          value={workingOn}
          onChange={(e) => setWorkingOn(e.target.value)}
          placeholder="Fixing the login redirect"
          maxLength={140}
        />
        <div className="dialog-help">
          One line, shown on the pane and to other agents. The pane's agent can update it too.
          {provenance && ` Currently ${provenance}.`}
        </div>
        {error && <div className="dialog-error" role="alert">{error}</div>}
        <div className="dialog-actions">
          <button type="button" onClick={onClose} disabled={saving}>Cancel</button>
          <button type="submit" className="primary" disabled={saving || suggesting || !!problem}>
            {saving ? 'Saving…' : 'Save'}
          </button>
        </div>
      </form>
    </div>,
    document.body,
  );
}
