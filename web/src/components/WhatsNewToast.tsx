import { useEffect, useState } from 'react';
import { useStore } from '../store';
import { userFacingNotes } from '../lib/releaseNotes';
import { useOperatorFeatures } from '../entitlements';

const STORAGE_KEY = 'solu.seenUpdateNotes';
const AUTO_DISMISS_MS = 12000;

function readSeen(): string | null {
  try { return localStorage.getItem(STORAGE_KEY); } catch { return null; }
}

function writeSeen(version: string) {
  try { localStorage.setItem(STORAGE_KEY, version); } catch { /* ignore */ }
}

export function WhatsNewToast() {
  const current = useStore((s) => s.updateStatus?.current);
  const notes = useStore((s) => s.updateStatus?.notes);
  const setVersionPopoverOpen = useStore((s) => s.setVersionPopoverOpen);
  const team = useOperatorFeatures();
  const [visible, setVisible] = useState(false);
  const [version, setVersion] = useState<string | null>(null);
  const [headline, setHeadline] = useState<string>('');

  useEffect(() => {
    if (!current || current === 'dev') return;
    const seen = readSeen();
    if (seen === null) {
      writeSeen(current);
      return;
    }
    if (seen === current) return;
    // Curated notes for users when the release has them; never raw commit
    // subjects on a public install.
    setVersion(current);
    setHeadline(userFacingNotes(current, notes, team)[0]?.text ?? '');
    setVisible(true);
  }, [current, notes, team]);

  useEffect(() => {
    if (!visible) return;
    const t = window.setTimeout(() => dismiss(), AUTO_DISMISS_MS);
    return () => window.clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [visible]);

  function dismiss() {
    if (version) writeSeen(version);
    setVisible(false);
  }

  function openAll() {
    if (version) writeSeen(version);
    setVisible(false);
    setVersionPopoverOpen(true);
  }

  if (!visible || !version) return null;

  return (
    <div
      className="whats-new-toast"
      role="status"
      aria-live="polite"
    >
      <div className="whats-new-header">
        <span className="whats-new-title">Updated to v{version}</span>
        <button
          type="button"
          className="whats-new-close"
          onClick={dismiss}
          aria-label="Dismiss"
          title="Dismiss"
        >×</button>
      </div>
      {headline && <div className="whats-new-body">{headline}</div>}
      <button
        type="button"
        className="whats-new-link"
        onClick={openAll}
      >What's new →</button>
    </div>
  );
}
