import { useState } from 'react';
import { usePersonalView, useEntitlements } from '../entitlements';
import { useStore } from '../store';

// Shown while the personal view is on, so a team member never forgets the
// app is hiding team features. One click goes back to the full view.
export function PersonalViewPill() {
  const personal = usePersonalView();
  const [busy, setBusy] = useState(false);
  if (!personal) return null;
  const exit = async () => {
    setBusy(true);
    try {
      await useEntitlements.getState().setPersonalView(false);
    } catch {
      useStore.getState().toast('Could not change the view. It can only be changed on this computer.', {
        kind: 'error',
        icon: '⚠',
      });
    } finally {
      setBusy(false);
    }
  };
  return (
    <button
      type="button"
      className="personal-view-pill"
      data-tauri-drag-region={false}
      disabled={busy}
      onClick={() => void exit()}
      title="You're seeing the app as a free account does. Team engines keep running. Click to show team features again."
    >
      Personal view
      <span className="personal-view-pill-exit" aria-hidden="true">×</span>
    </button>
  );
}
