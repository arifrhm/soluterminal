import { useEffect, useRef, useState } from 'react';
import { api } from '../../api';
import { useCloud } from '../../cloud';
import { MARKETING_CONSENT_LABEL, MARKETING_CONSENT_WORDING } from '../../marketingConsent';
import { SettingsCheck } from './SettingsRow';

export const MARKETING_CONSENT_HINT =
  'Occasional emails about new features. Sign-in codes and security notices are sent either way.';

// The product-updates email choice for the signed-in account. Hidden when
// signed out or when the cloud can't be reached, rather than showing a
// switch that does nothing.
export function MarketingConsentSetting() {
  const signedIn = useCloud((s) => Boolean(s.session));
  const [consent, setConsent] = useState<boolean | null>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const pendingRef = useRef(false);

  useEffect(() => {
    if (!signedIn) {
      setConsent(null);
      return;
    }
    let mounted = true;
    void api
      .getMarketingConsent()
      .then((s) => {
        if (mounted) setConsent(s.consent === true);
      })
      .catch(() => {
        if (mounted) setConsent(null);
      });
    return () => {
      mounted = false;
    };
  }, [signedIn]);

  if (!signedIn || consent === null) return null;

  const change = async (next: boolean) => {
    if (pendingRef.current) return;
    pendingRef.current = true;
    const previous = consent;
    setPending(true);
    setError(null);
    setConsent(next);
    try {
      const saved = await api.putMarketingConsent(next, MARKETING_CONSENT_WORDING);
      setConsent(saved.consent === true);
    } catch (e) {
      setConsent(previous);
      setError(`Couldn't save the email setting: ${String(e)}`);
    } finally {
      pendingRef.current = false;
      setPending(false);
    }
  };

  return (
    <>
      <SettingsCheck
        label={MARKETING_CONSENT_LABEL}
        checked={consent}
        disabled={pending}
        onChange={(next) => void change(next)}
        hint={MARKETING_CONSENT_HINT}
      />
      {error && <p className="settings-row-hint settings-error">{error}</p>}
    </>
  );
}
