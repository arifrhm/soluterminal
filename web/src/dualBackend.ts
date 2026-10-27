// Dual-backend awareness for the frontend.
//
// When the Tauri desktop app runs in Backend::Dual mode, it spawns
// both the WSL-bundled daemon and the Windows-native daemon and loads
// the webview from one of them with TWO query params:
//
//   ?solu_primary=<full url of the daemon this page was loaded from>
//   &solu_alt=<full url of the OTHER daemon>
//
// Both URLs carry their own ?token=<...>. The auth gate's redirect
// strips only the outer ?token=; solu_primary and solu_alt survive
// the redirect, so this module runs AFTER the auth dance and sees
// both values in window.location.
//
// This module is a read-only parser. The BackendSwitcher component
// calls `swapTarget()` to get the URL to navigate to on click (which
// sets the other backend as primary, with the current one becoming
// the alt — preserving round-trip ability).

function getParam(name: string): string | null {
  try {
    const params = new URLSearchParams(window.location.search);
    return params.get(name);
  } catch {
    return null;
  }
}

/** True if the app is running as the dual-backend Tauri setup. */
export function isDualBackend(): boolean {
  return !!getParam('solu_alt') || isRouterMode();
}

/**
 * True if the webview is loaded via the `solud router` reverse proxy
 * (desktop Dual-backend) OR if we're in cloud mode and the selected
 * machine has more than one paired daemon (wsl + win). Both cases
 * route through path prefixes /b/wsl/* and /b/win/* under a single
 * origin, so the rest of the app can treat them identically.
 *
 * Cloud-side opt-in is driven by setCloudDualBackend(true), called
 * from cloud.ts once the user picks a machine and /api/machines
 * reports it has multiple backends.
 */
export function isRouterMode(): boolean {
  return isDesktopRouterMode() || cloudDualBackendFlag;
}

/** True only when this page itself is served through the desktop router. */
export function isDesktopRouterMode(): boolean {
  return getParam('solu_router') === '1';
}

// Runtime flag flipped by cloud.ts when the user selects a machine
// that has multiple backends connected. We can't derive this purely
// from the URL (unlike the desktop router mode, which carries
// `?solu_router=1` on the webview's load URL) because cloud mode
// loads the SPA from soluterminal-cloud.onrender.com and the
// machine selection happens AFTER the bundle is running.
let cloudDualBackendFlag = false;
export function setCloudDualBackend(on: boolean) {
  cloudDualBackendFlag = on;
}

/** URL of the backend serving this page, with its token preserved. */
export function primaryUrl(): string | null {
  return getParam('solu_primary');
}

/** URL of the OTHER backend. */
export function altUrl(): string | null {
  return getParam('solu_alt');
}

/**
 * Build the URL to navigate to when the user clicks "switch backend".
 * Flips primary and alt so the new page reaches the other daemon and
 * can swap back later. Returns null when not in dual mode.
 */
export function swapTarget(): string | null {
  const primary = primaryUrl();
  const alt = altUrl();
  if (!primary || !alt) return null;
  // Build `<alt>?solu_primary=<alt>&solu_alt=<primary>` — `alt` becomes
  // the new page's primary URL. The alt already carries ?token=, so
  // we append our extras with & rather than ?.
  const sep = alt.includes('?') ? '&' : '?';
  return `${alt}${sep}solu_primary=${encodeURIComponent(alt)}&solu_alt=${encodeURIComponent(primary)}`;
}

/**
 * Heuristic label for each URL so the UI can show "WSL" vs "Windows".
 * The WSL-backed proxy listens on 18787; the Windows-native daemon on
 * 8787. Port is the cleanest signal we have without a round-trip.
 */
export function labelForUrl(url: string | null): 'WSL' | 'Windows' | 'Backend' {
  if (!url) return 'Backend';
  if (url.includes(':18787')) return 'WSL';
  if (url.includes(':8787')) return 'Windows';
  return 'Backend';
}

export type BackendId = 'wsl' | 'win';

/**
 * In router mode, reads the `b` URL param and returns which backend
 * should be active this session. Defaults to 'wsl' so first loads in
 * router mode match the Phase-3 default.
 */
export function activeBackendFromUrl(): BackendId {
  const v = getParam('b');
  return v === 'win' ? 'win' : 'wsl';
}

/**
 * Navigate to the current URL but with `?b=<backend>` (and optionally
 * `?ws=<workspace>` so the post-reload bootstrap loads a specific
 * workspace directly). A page reload is a simple, reliable way to
 * reset store + reopen all WS/SSE connections against the new
 * backend without wiring per-connection teardown code.
 */
export function switchActiveBackend(b: BackendId, workspaceName?: string) {
  const url = new URL(window.location.href);
  url.searchParams.set('b', b);
  if (workspaceName) {
    url.searchParams.set('ws', workspaceName);
  } else {
    url.searchParams.delete('ws');
  }
  window.location.href = url.toString();
}

/** Optional pre-selected workspace to load after the post-reload bootstrap. */
export function pendingWorkspace(): string | null {
  return getParam('ws');
}
