import { useSyncExternalStore } from 'react';

export type SettingsFlag = 'credentialsSidebar' | 'legacyRedirects' | 'search' | 'deepseekRateChangeAlerts';

const FLAGS_CHANGED_EVENT = 'solu:settings-flags-changed';

export function readSettingsFlag(name: SettingsFlag, fallback: boolean): boolean {
  try {
    const value = localStorage.getItem(`solu.settings.${name}`);
    if (value === '0') return false;
    if (value === '1') return true;
  } catch {
    // Storage may be unavailable; callers retain their release fallback.
  }
  return fallback;
}

export function setSettingsFlag(name: SettingsFlag, value: boolean): void {
  try {
    localStorage.setItem(`solu.settings.${name}`, value ? '1' : '0');
  } catch {
    // The same-window event still notifies mounted consumers.
  }
  window.dispatchEvent(new CustomEvent(FLAGS_CHANGED_EVENT, { detail: { name, value } }));
}

function subscribe(listener: () => void): () => void {
  window.addEventListener('storage', listener);
  window.addEventListener(FLAGS_CHANGED_EVENT, listener);
  return () => {
    window.removeEventListener('storage', listener);
    window.removeEventListener(FLAGS_CHANGED_EVENT, listener);
  };
}

export function useSettingsFlag(name: SettingsFlag, fallback: boolean): boolean {
  return useSyncExternalStore(
    subscribe,
    () => readSettingsFlag(name, fallback),
    () => fallback,
  );
}
