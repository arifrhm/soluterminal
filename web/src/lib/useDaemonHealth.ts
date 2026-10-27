import { useSyncExternalStore } from 'react';
import { api, currentPrefix, currentRoutingScope } from '../api';
import { isDesktopRouterMode, isRouterMode } from '../dualBackend';
import type { Health } from '../types';
import {
  classifyBundleParity,
  isDevLikeVersion,
  newestVersion,
  type BundleParityState,
} from './bundleParity';

// One shared daemon-health poller for the status bar and banners. Provenance is
// resolved against the SHELL-SERVING origin, which is distinct from the active
// API backend during a dual Windows/WSL rollout.
const HEALTH_POLL_MS = 10_000;
const HEALTH_FAIL_THRESHOLD = 2;

const bundleVersion = typeof __SOLU_WEB_VERSION__ === 'string' ? __SOLU_WEB_VERSION__ : 'dev';

export interface DaemonHealthState {
  health: Health | null;
  healthScopeKey: string | null;
  lastOkMs: number;
  polling: boolean;
  bundleState: BundleParityState;
}

// Release publish/desktop sidecar builds never set SOLU_CAPTURE_HARNESS, so
// the query flag is inert in production bundles.
export function captureHarnessActive(options: { harnessBuild?: boolean; search?: string } = {}): boolean {
  const harnessBuild = options.harnessBuild ?? (
    (typeof __SOLU_CAPTURE_HARNESS__ === 'boolean' && __SOLU_CAPTURE_HARNESS__) || import.meta.env.DEV
  );
  if (!harnessBuild) return false;
  try {
    return new URLSearchParams(options.search ?? window.location.search).get('solu_capture') === '1';
  } catch {
    return false;
  }
}

const initialBundleState = classifyBundleParity({
  runningBundleVersion: bundleVersion,
  harness: captureHarnessActive(),
});
let state: DaemonHealthState = {
  health: null,
  healthScopeKey: null,
  lastOkMs: Date.now(),
  polling: false,
  bundleState: initialBundleState,
};

export function driftedVersion(h: Health, runningVersion = bundleVersion): string | null {
  const daemon = (h.version ?? '').trim();
  if (!daemon || isDevLikeVersion(daemon) || isDevLikeVersion(runningVersion)) return null;
  return daemon === runningVersion ? null : daemon;
}

export function resolveBundleState(input: {
  health: Health;
  shellHealth?: Health | null;
  otherHealth?: Health | null;
  probedWebVersion?: string;
  runningBundleVersion?: string;
  targetVersion?: string;
  harness?: boolean;
}): BundleParityState {
  const shell = input.shellHealth === undefined ? input.health : input.shellHealth;
  // The page origin is authoritative: it is what a navigation/reload will
  // actually fetch. Health provenance is only a fallback for an older origin
  // that does not expose /webdist.json.
  const probedWebVersion = (input.probedWebVersion ?? '').trim();
  const shellWebVersion = probedWebVersion || (shell?.webVersion ?? '').trim();
  const targetVersion = newestVersion(
    input.targetVersion,
    input.health.version,
    input.shellHealth?.version,
    input.otherHealth?.version,
  );
  const resolved = classifyBundleParity({
    runningBundleVersion: input.runningBundleVersion ?? bundleVersion,
    shellDaemonVersion: shell?.version,
    shellWebVersion,
    targetVersion,
    harness: input.harness ?? captureHarnessActive(),
  });
  if (resolved.kind === 'unknown' && !resolved.legacyStaleVersion) {
    const legacyStaleVersion = driftedVersion(input.health, resolved.runningBundleVersion) ?? '';
    if (legacyStaleVersion) return { ...resolved, legacyStaleVersion };
  }
  return resolved;
}

async function probeShellWebVersion(): Promise<string> {
  try {
    const response = await fetch('/webdist.json', { cache: 'no-store' });
    if (!response.ok) return '';
    const contentType = response.headers.get('content-type')?.toLowerCase() ?? '';
    if (!contentType.includes('application/json')) return '';
    const value = await response.json() as { webVersion?: unknown };
    return typeof value.webVersion === 'string' ? value.webVersion.trim() : '';
  } catch {
    return '';
  }
}

export async function resolvePolledBundleState(
  health: Health,
  runningBundleVersion = bundleVersion,
): Promise<BundleParityState> {
  // A non-empty machine prefix means /api/health is relayed to the selected
  // machine while the page itself is served by the cloud origin. Never pair
  // that remote daemon version with the cloud origin's webdist provenance.
  const cloudMode = currentPrefix() !== '';
  let shellHealth: Health | null = cloudMode ? null : health;
  let otherHealth: Health | null = cloudMode ? health : null;
  if (isRouterMode()) {
    const [win, wsl] = await Promise.allSettled([
      api.healthBackend('win'),
      api.healthBackend('wsl'),
    ]);
    const winHealth = win.status === 'fulfilled' ? win.value : null;
    const wslHealth = wsl.status === 'fulfilled' ? wsl.value : null;
    if (isDesktopRouterMode()) {
      // daemon/router.go sends non-/api routes to the Windows daemon, except
      // while WSL runs a Preview build (router_shell.go): then the page is
      // WSL's.
      const previewShell = wslHealth?.build?.channel === 'preview';
      shellHealth = previewShell ? wslHealth : winHealth;
      otherHealth = previewShell ? winHealth : wslHealth;
    } else {
      // Cloud dual-backend mode uses the same /b/{backend} API routing, but
      // the SPA is served by the cloud origin, not either installed daemon.
      shellHealth = null;
      otherHealth = winHealth;
      if (wslHealth && (!otherHealth || newestVersion(wslHealth.version, otherHealth.version) === wslHealth.version)) {
        otherHealth = wslHealth;
      }
    }
  }

  const probedWebVersion = await probeShellWebVersion();
  return resolveBundleState({ health, shellHealth, otherHealth, probedWebVersion, runningBundleVersion });
}

const listeners = new Set<() => void>();
const stateObservers = new Set<(next: DaemonHealthState) => void>();
let pollTimer: ReturnType<typeof setInterval> | null = null;
let inFlight = false;
let consecutiveFailures = 0;

function emit() {
  for (const listener of listeners) listener();
  for (const observer of stateObservers) observer(state);
}

function setState(patch: Partial<DaemonHealthState>) {
  state = { ...state, ...patch };
  emit();
}

async function pollOnce() {
  if (inFlight) return;
  inFlight = true;
  const scopeKey = currentRoutingScope().key;
  setState({ polling: true });
  try {
    const health = await api.health();
    const bundleState = await resolvePolledBundleState(health);
    consecutiveFailures = 0;
    setState({ health, healthScopeKey: scopeKey, bundleState, lastOkMs: Date.now(), polling: false });
  } catch {
    consecutiveFailures += 1;
    if (consecutiveFailures >= HEALTH_FAIL_THRESHOLD) setState({ health: null, healthScopeKey: null, polling: false });
    else setState({ polling: false });
  } finally {
    inFlight = false;
  }
}

export function refreshDaemonHealth(): void {
  void pollOnce();
}

export function getDaemonHealthState(): DaemonHealthState {
  return state;
}

// Store-side stale-bundle recovery observes the same poll results without
// starting a second interval or creating a React dependency.
export function subscribeDaemonHealthState(observer: (next: DaemonHealthState) => void): () => void {
  stateObservers.add(observer);
  return () => stateObservers.delete(observer);
}

function subscribe(listener: () => void): () => void {
  const first = listeners.size === 0;
  listeners.add(listener);
  if (first) {
    void pollOnce();
    pollTimer = setInterval(() => void pollOnce(), HEALTH_POLL_MS);
  }
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0 && pollTimer !== null) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  };
}

function getSnapshot(): DaemonHealthState {
  return state;
}

export function useDaemonHealth(): DaemonHealthState {
  return useSyncExternalStore(subscribe, getSnapshot);
}
