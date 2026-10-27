import { readFileSync } from 'node:fs';
import { beforeEach, describe, expect, it } from 'vitest';
import { APP_THEME_KEY, initialAppThemePref, isFreshInstall } from './appThemeDefault';

function storageWith(entries: Record<string, string>) {
  const keys = Object.keys(entries);
  return {
    length: keys.length,
    key: (i: number) => keys[i] ?? null,
    getItem: (k: string) => (k in entries ? entries[k] : null),
  };
}

describe('initialAppThemePref', () => {
  it('follows the OS (System) on a brand-new install', () => {
    expect(initialAppThemePref(storageWith({}))).toBe('system');
  });

  it('keeps Dark for an existing install that never chose an appearance', () => {
    expect(initialAppThemePref(storageWith({ 'solu.terminalTheme': 'solu dark' }))).toBe('dark');
    expect(isFreshInstall(storageWith({ 'solu.sidebarWidth': '260' }))).toBe(false);
  });

  it('never overrides an explicit choice', () => {
    expect(initialAppThemePref(storageWith({ [APP_THEME_KEY]: 'dark' }))).toBe('dark');
    expect(initialAppThemePref(storageWith({ [APP_THEME_KEY]: 'light', 'solu.x': '1' }))).toBe('light');
    expect(initialAppThemePref(storageWith({ [APP_THEME_KEY]: 'system', 'solu.x': '1' }))).toBe('system');
  });

  it('ignores other apps\' keys and an unknown stored value', () => {
    expect(initialAppThemePref(storageWith({ 'other.app': '1' }))).toBe('system');
    expect(initialAppThemePref(storageWith({ [APP_THEME_KEY]: 'neon' }))).toBe('system');
  });

  it('falls back to Dark when storage is unavailable', () => {
    expect(initialAppThemePref(null)).toBe('dark');
  });
});

// The pre-paint script in index.html must apply the same rule, because it
// runs first and persists the decision the store then reads.
describe('index.html pre-paint appearance', () => {
  const html = readFileSync('index.html', 'utf8');
  const script = html.match(/<script>([\s\S]*?)<\/script>/)?.[1] ?? '';

  function run(prefersLight: boolean) {
    document.documentElement.removeAttribute('data-theme');
    window.matchMedia = ((q: string) => ({
      matches: q.includes('light') ? prefersLight : !prefersLight,
      media: q,
      addEventListener() {},
      removeEventListener() {},
    })) as unknown as typeof window.matchMedia;
    new Function(script)();
    return document.documentElement.getAttribute('data-theme');
  }

  beforeEach(() => localStorage.clear());

  it('opens a new install in light when the OS prefers light, and remembers System', () => {
    expect(run(true)).toBe('light');
    expect(localStorage.getItem(APP_THEME_KEY)).toBe('system');
  });

  it('opens a new install in dark when the OS prefers dark', () => {
    expect(run(false)).toBeNull();
    expect(localStorage.getItem(APP_THEME_KEY)).toBe('system');
  });

  it('keeps an existing install dark even when the OS prefers light', () => {
    localStorage.setItem('solu.terminalFontSize', '13');
    expect(run(true)).toBeNull();
    expect(localStorage.getItem(APP_THEME_KEY)).toBe('dark');
  });

  it('keeps an explicit light choice', () => {
    localStorage.setItem(APP_THEME_KEY, 'light');
    expect(run(false)).toBe('light');
  });
});
