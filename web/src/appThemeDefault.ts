// First-launch appearance. A brand-new install follows the OS (System); an
// existing install that never picked an appearance keeps the Dark it has
// always had. An explicit choice always wins.
//
// The pre-paint script in index.html applies the same rule (it runs before
// any app code, so it is the one that sees a truly empty storage and persists
// the decision). Keep the two in sync.

export type InitialAppThemePref = 'dark' | 'light' | 'system';

export const APP_THEME_KEY = 'solu.appTheme';

type ThemeStorage = Pick<Storage, 'getItem' | 'key' | 'length'>;

/** True when no SoluTerminal setting other than the appearance key exists. */
export function isFreshInstall(storage: ThemeStorage): boolean {
  for (let i = 0; i < storage.length; i++) {
    const key = storage.key(i);
    if (key && key.startsWith('solu.') && key !== APP_THEME_KEY) return false;
  }
  return true;
}

export function initialAppThemePref(storage: ThemeStorage | null): InitialAppThemePref {
  if (!storage) return 'dark';
  try {
    const raw = storage.getItem(APP_THEME_KEY);
    if (raw === 'dark' || raw === 'light' || raw === 'system') return raw;
    return isFreshInstall(storage) ? 'system' : 'dark';
  } catch {
    return 'dark';
  }
}
