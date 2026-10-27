// Per-window identity for multi-monitor support (Phase 1a).
//
// The Tauri desktop shell injects `window.__SOLU_WINDOW__ = { label, role }`
// before any page script runs (see lib.rs main builder + workspace_window.rs).
// The main window is `{ label: "main", role: "primary" }`; secondary
// workspace windows are `{ label: "workspace_N", role: "secondary" }`.
//
// The label is the STABLE Tauri window label — it survives the frontend's
// cache-busting `_v` reload (the init script re-runs on every navigation),
// which is what lets each window restore its OWN workspace after an
// auto-update reload instead of all windows collapsing onto a shared one.
//
// Outside the desktop shell (a plain browser tab) the global is absent, so
// we default to the primary "main" identity — preserving the exact
// single-window behaviour that shipped before this feature.

import { isEmbedded, embedWindowIdentity } from '../embed';

export type WindowRole = 'primary' | 'secondary';

export interface SoluWindowInfo {
  label: string;
  role: WindowRole;
}

declare global {
  interface Window {
    __SOLU_WINDOW__?: { label?: unknown; role?: unknown };
  }
}

const DEFAULT_INFO: SoluWindowInfo = { label: 'main', role: 'primary' };

/** Resolve this window's identity, defaulting to the primary "main". */
export function getWindowInfo(): SoluWindowInfo {
  if (isEmbedded()) return { label: embedWindowIdentity(), role: 'secondary' };
  try {
    const g = window.__SOLU_WINDOW__;
    if (!g || typeof g.label !== 'string' || g.label.length === 0) return DEFAULT_INFO;
    const role: WindowRole = g.role === 'secondary' ? 'secondary' : 'primary';
    return { label: g.label, role };
  } catch {
    return DEFAULT_INFO;
  }
}

/** This window's stable label (e.g. "main", "workspace_2"). */
export function windowLabel(): string {
  return getWindowInfo().label;
}

/** True for the primary ("main") window. */
export function isPrimaryWindow(): boolean {
  return getWindowInfo().role === 'primary' && getWindowInfo().label === 'main';
}

/**
 * Namespace a localStorage key by this window so two windows don't fight
 * over per-window state (e.g. which workspace is active).
 *
 * The primary window keeps the ORIGINAL un-suffixed key: existing
 * single-window users must not lose their remembered state on upgrade, and
 * a browser tab (no injected identity → "main") behaves exactly as before.
 * Only secondary windows get a `.<label>` suffix.
 */
export function windowScopedKey(baseKey: string): string {
  const label = windowLabel();
  return label === 'main' ? baseKey : `${baseKey}.${label}`;
}
