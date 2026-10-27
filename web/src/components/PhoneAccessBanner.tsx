import { useState } from 'react';
import { usePhoneAccessAllowed } from '../entitlements';
import { IconSmartphone } from './ui/icons';
import { useStore } from '../store';
import { usePhoneAccessStatus, isDesktopShell } from '../cloudPair';

// Product-first, dismissible first-run banner. Shown in the desktop shell when
// phone access is not yet set up (!hasCreds) and the user hasn't dismissed it.
// Primary "Set up" opens the pair panel; "×" dismisses. Once dismissed we don't
// nag — the header chip remains the entry point.
//
// Dismissal is persisted per-install (localStorage) so reopening the app
// doesn't re-nag, matching the "don't nag" requirement. Desktop-shell only.

const DISMISS_KEY = 'solu.phoneAccessBannerDismissed';

function readDismissed(): boolean {
  try {
    return localStorage.getItem(DISMISS_KEY) === '1';
  } catch {
    return false;
  }
}

export function PhoneAccessBanner() {
  const setPhoneAccessOpen = useStore((s) => s.setPhoneAccessOpen);
  const [dismissed, setDismissed] = useState(readDismissed);
  const { status } = usePhoneAccessStatus();
  const allowed = usePhoneAccessAllowed();

  // Desktop-shell only; never on the phone/relay web.
  if (!isDesktopShell() || !allowed) return null;

  // Only nag when phone access truly isn't set up yet. Wait for a status read
  // (status !== null) so we don't flash the banner before we know hasCreds.
  const notSetUp = status !== null && !status.hasCreds;
  const show = notSetUp && !dismissed;
  if (!show) return null;

  const dismiss = () => {
    try {
      localStorage.setItem(DISMISS_KEY, '1');
    } catch {
      /* ignore — session-level dismissal still applies via state */
    }
    setDismissed(true);
  };

  return (
    <div className="phone-access-banner" role="status">
      <span className="phone-access-banner-text">
        <span aria-hidden><IconSmartphone size={13} style={{ verticalAlign: '-2px' }} /></span> Control this computer from your phone
        <span className="phone-access-banner-sub"> — set up phone access</span>
      </span>
      <span className="phone-access-banner-actions">
        <button
          type="button"
          className="phone-access-banner-btn primary"
          onClick={() => setPhoneAccessOpen(true)}
        >
          Set up
        </button>
        <button
          type="button"
          className="phone-access-banner-btn ghost"
          title="Dismiss"
          aria-label="Dismiss"
          onClick={dismiss}
        >
          ×
        </button>
      </span>
    </div>
  );
}
