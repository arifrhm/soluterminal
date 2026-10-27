import { useEffect, useState } from 'react';
import { useStore } from '../store';
import type { CloudMachine } from '../cloud';

// Mode-A token rotation. Issues a new pairing token for the machine on
// the cloud; the daemon's existing poll loop picks it up within ~1 min.
// The previous token keeps working for a 10-minute grace window so the
// transition is invisible to users — the daemon swaps over before the
// old token expires.
//
// Crucially we never display the new token in the UI: there is no
// out-of-band re-pairing step in this flow. The daemon authenticates
// itself to the cloud and pulls the new token down on its own. Showing
// it would tempt users to copy/paste it manually, which is the failure
// mode this design avoids.

interface Props {
  machine: CloudMachine;
  bearer: string | null;
  // Injected for tests; defaults to the global toast in the store. The
  // button does not own any other shared state, so a single seam keeps
  // unit tests free of the heavy store import.
  toast?: (message: string, opts?: { kind?: 'info' | 'success' | 'warn' | 'error'; icon?: string; ttlMs?: number }) => void;
}

export function RotateTokenButton({ machine, bearer, toast }: Props) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);

  const fireToast = toast ?? ((msg, opts) => useStore.getState().toast(msg, opts));

  const onConfirm = async () => {
    if (!bearer) {
      fireToast('Not authorized to rotate this machine.', { kind: 'error', icon: '⚠' });
      setOpen(false);
      return;
    }
    setBusy(true);
    try {
      const resp = await fetch(`/api/machines/${encodeURIComponent(machine.id)}/rotate-token`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${bearer}` },
      });
      if (resp.ok) {
        // Read and discard the body — we deliberately do NOT surface
        // newToken to the operator. The daemon picks it up itself.
        try { await resp.json(); } catch { /* tolerate empty body */ }
        fireToast(
          `Rotation initiated for ${machine.name}. Daemon will pick up the new token within a minute.`,
          { kind: 'success', icon: '✓', ttlMs: 4000 },
        );
      } else if (resp.status === 409) {
        fireToast('A rotation is already in progress for this machine.', { kind: 'warn', icon: '⚠' });
      } else if (resp.status === 401 || resp.status === 403) {
        fireToast('Not authorized to rotate this machine.', { kind: 'error', icon: '⚠' });
      } else {
        let detail = '';
        try { detail = (await resp.text()).trim(); } catch { /* ignore */ }
        fireToast(
          `Rotation failed: ${detail || `HTTP ${resp.status}`}`,
          { kind: 'error', icon: '⚠' },
        );
      }
    } catch (e: any) {
      fireToast(`Rotation failed: ${e?.message ?? String(e)}`, { kind: 'error', icon: '⚠' });
    } finally {
      setBusy(false);
      setOpen(false);
    }
  };

  return (
    <>
      <button
        type="button"
        title={`Issue a new token for ${machine.name}. The current token works for 10 more minutes; the daemon swaps over within ~1 minute.`}
        onClick={() => setOpen(true)}
        style={iconBtnStyle}
      >
        Rotate token
      </button>
      {open && (
        <RotateTokenDialog
          machineName={machine.name}
          busy={busy}
          onCancel={() => { if (!busy) setOpen(false); }}
          onConfirm={onConfirm}
        />
      )}
    </>
  );
}

function RotateTokenDialog({
  machineName, busy, onCancel, onConfirm,
}: {
  machineName: string;
  busy: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  // Esc to cancel, matching BroadcastDialog.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !busy) onCancel();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onCancel, busy]);

  return (
    <div style={scrimStyle} onClick={onCancel} role="presentation">
      <div
        style={dialogStyle}
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label="Rotate token"
      >
        <h3 style={{ margin: 0, fontSize: 14 }}>Rotate token for {machineName}?</h3>
        <p style={bodyStyle}>
          A new token will be issued for this machine. The current token will
          keep working for 10 minutes, after which it becomes invalid. The
          daemon will pick up the new token within about a minute.
        </p>
        <p style={bodyStyle}>
          Use this if you suspect the token has leaked, or as routine hygiene.
        </p>
        <div style={actionsStyle}>
          <button type="button" onClick={onCancel} disabled={busy} style={cancelBtnStyle}>
            Cancel
          </button>
          <button
            type="button"
            onClick={onConfirm}
            disabled={busy}
            style={dangerBtnStyle}
          >
            {busy ? 'Rotating…' : 'Rotate token'}
          </button>
        </div>
      </div>
    </div>
  );
}

// Inline styles match MachinePicker's existing pattern (CloudGate.tsx
// keeps its small set of screens off styles.css to avoid bloating the
// global stylesheet for views users hit a handful of times). Color
// tokens mirror styles.css custom properties so theming still applies
// where a `var()` works inline.
const iconBtnStyle: React.CSSProperties = {
  background: 'transparent',
  border: '1px solid var(--border)',
  borderRadius: 4,
  color: 'var(--fg-dim)',
  cursor: 'pointer',
  fontSize: 11,
  padding: '4px 8px',
};
const scrimStyle: React.CSSProperties = {
  position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.5)',
  display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 200,
};
const dialogStyle: React.CSSProperties = {
  background: 'var(--bg-elev)',
  border: '1px solid var(--border)',
  borderRadius: 6,
  padding: 16,
  width: 420,
  maxWidth: '92vw',
  display: 'flex',
  flexDirection: 'column',
  gap: 10,
  color: 'var(--fg)',
  fontFamily: 'system-ui, sans-serif',
};
const bodyStyle: React.CSSProperties = {
  margin: 0,
  fontSize: 12,
  color: 'var(--fg-dim)',
  lineHeight: 1.45,
};
const actionsStyle: React.CSSProperties = {
  display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 4,
};
const cancelBtnStyle: React.CSSProperties = {
  padding: '6px 12px',
  background: 'transparent',
  color: 'var(--fg)',
  border: '1px solid var(--border)',
  borderRadius: 4,
  cursor: 'pointer',
  fontSize: 12,
};
// Danger styling mirrors button.danger in styles.css via theme tokens.
// Inline because this dialog is rendered in the
// MachinePicker shell, which doesn't pick up the app-level button
// classes.
const dangerBtnStyle: React.CSSProperties = {
  padding: '6px 12px',
  background: 'transparent',
  color: 'var(--danger)',
  border: '1px solid color-mix(in srgb, var(--danger) 22%, transparent)',
  borderRadius: 4,
  cursor: 'pointer',
  fontSize: 12,
  fontWeight: 600,
};
