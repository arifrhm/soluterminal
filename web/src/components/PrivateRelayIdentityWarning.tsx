import { useEffect, useId, useState } from 'react';
import { forgetComputer } from '../lib/deviceIdentity';
import { PRIVATE_RELAY_IDENTITY_EVENT, PRIVATE_RELAY_IDENTITY_MESSAGE } from '../lib/privateRelay';

// Shown when a computer this browser talks to over the private relay channel
// (lib/privateRelay) fails to prove the identity pinned at approval. Requests
// to it are refused rather than sent the ordinary way, so the person has to
// act: remove and re-approve this browser on the computer.

export const PRIVATE_RELAY_IDENTITY_TITLE = "This computer's identity changed";

export function PrivateRelayIdentityWarning() {
  const titleId = useId();
  const [target, setTarget] = useState<string | null>(null);

  useEffect(() => {
    const onChanged = (e: Event) => {
      const detail = (e as CustomEvent<{ target: string }>).detail;
      setTarget((current) => current ?? detail.target);
    };
    window.addEventListener(PRIVATE_RELAY_IDENTITY_EVENT, onChanged);
    return () => window.removeEventListener(PRIVATE_RELAY_IDENTITY_EVENT, onChanged);
  }, []);

  if (!target) return null;

  const close = () => setTarget(null);
  const forget = () => {
    // The fresh approval compares codes over both keys before pinning again,
    // so forgetting here cannot be used to slip another key in.
    forgetComputer(target);
    window.location.reload();
  };

  return (
    <div className="dialog-backdrop" onMouseDown={close}>
      <div
        className="dialog"
        role="alertdialog"
        aria-modal="true"
        aria-labelledby={titleId}
        onKeyDown={(e) => {
          e.stopPropagation();
          if (e.key === 'Escape') { e.preventDefault(); close(); }
        }}
        onMouseDown={(e) => e.stopPropagation()}
        style={{ width: 460, maxWidth: '92vw' }}
      >
        <h2 id={titleId} style={{ margin: 0, fontSize: 16 }}>{PRIVATE_RELAY_IDENTITY_TITLE}</h2>
        <div className="settings-error" style={{ marginTop: 8, fontSize: 12, lineHeight: 1.5 }}>
          {PRIVATE_RELAY_IDENTITY_MESSAGE}
        </div>
        <div style={{ marginTop: 8, fontSize: 12, color: 'var(--fg-dim)', lineHeight: 1.5 }}>
          Until then this browser won't send anything to it. On the computer, open SoluTerminal → Settings →
          Devices → Remote devices and remove this browser; then forget the old key here and approve this
          browser again, checking that both screens show the same code.
        </div>
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 14 }}>
          <button type="button" onClick={close} style={{ fontSize: 12, padding: '4px 12px' }}>Not now</button>
          <button type="button" className="primary" onClick={forget} style={{ fontSize: 12, padding: '4px 14px' }}>
            Forget the old key and approve again
          </button>
        </div>
      </div>
    </div>
  );
}
