import { useState } from 'react';
import { useEntitlements, usePersonalView, usePersonalViewAvailable } from '../../entitlements';
import { SettingsCheck } from './SettingsRow';

export const PERSONAL_VIEW_LABEL = 'View as personal';
export const PERSONAL_VIEW_HINT =
  'See SoluTerminal exactly as a free account does: team features are hidden and new agent panes get the public tools. ' +
  'Team engines keep running and open panes keep their tools. Only changes on this computer.';

// The personal-view switch for team and operator installs. Installs with
// nothing to hide never see it.
export function PersonalViewSetting() {
  const available = usePersonalViewAvailable();
  const personal = usePersonalView();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState('');
  if (!available) return null;
  const change = async (on: boolean) => {
    setPending(true);
    setError('');
    try {
      await useEntitlements.getState().setPersonalView(on);
    } catch {
      setError('Could not change the view. It can only be changed on this computer.');
    } finally {
      setPending(false);
    }
  };
  return (
    <>
      <SettingsCheck
        label={PERSONAL_VIEW_LABEL}
        checked={personal}
        disabled={pending}
        onChange={(on) => void change(on)}
        hint={PERSONAL_VIEW_HINT}
      />
      {error && <p className="settings-row-hint settings-error" role="alert">{error}</p>}
    </>
  );
}
