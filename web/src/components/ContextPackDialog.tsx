import { useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { DirPicker } from './DirPicker';
import { useFocusTrap } from './useFocusTrap';

interface ContextPackDialogProps {
  title: string;
  value: string;
  onSave: (dir: string) => Promise<void>;
  onClose: () => void;
}

export function ContextPackDialog({ title, value, onSave, onClose }: ContextPackDialogProps) {
  const [dir, setDir] = useState(value);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const cardRef = useRef<HTMLFormElement>(null);
  useFocusTrap(cardRef, true);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !event.defaultPrevented) onClose();
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [onClose]);

  const save = async (next: string) => {
    setSaving(true);
    setError(null);
    try {
      await onSave(next.trim());
      onClose();
    } catch (err) {
      setError((err as Error).message || 'Could not update the context pack.');
    } finally {
      setSaving(false);
    }
  };

  return createPortal(
    <div className="dialog-backdrop" role="presentation" onMouseDown={onClose}>
      <form
        ref={cardRef}
        className="dialog context-pack-dialog"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onSubmit={(event) => { event.preventDefault(); void save(dir); }}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="dialog-header">
          <h2 className="dialog-title">{title}</h2>
        </div>
        <div>
          <label htmlFor="context-pack-dir">Context pack folder</label>
          <DirPicker
            id="context-pack-dir"
            value={dir}
            onChange={setDir}
            placeholder="~/projects/context"
            autoFocus
          />
          <div className="dialog-help">
            A folder with CLAUDE.md or AGENTS.md. Only the path is stored; the agent reads the files itself.
          </div>
          <div className="dialog-help">
            Tip: if ~/.claude/CLAUDE.md or ~/.codex/AGENTS.md already contains a manual pointer to this folder,
            you can remove it — the pack is attached automatically now.
          </div>
        </div>
        {error && <div className="dialog-error">{error}</div>}
        <div className="dialog-actions">
          <button type="button" onClick={onClose} disabled={saving}>Cancel</button>
          <button type="button" onClick={() => { void save(''); }} disabled={saving || !value}>Detach</button>
          <button type="submit" className="primary" disabled={saving || !dir.trim()}>
            {saving ? 'Saving…' : 'Save'}
          </button>
        </div>
      </form>
    </div>,
    document.body,
  );
}
