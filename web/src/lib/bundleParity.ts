export type BundleParityKind =
  | 'ok'
  | 'transient-stale'
  | 'activation-gap'
  | 'packaged-mismatch'
  | 'unknown';

export interface BundleParityInput {
  runningBundleVersion: string;
  shellDaemonVersion?: string;
  shellWebVersion?: string;
  targetVersion?: string;
  harness?: boolean;
}

export interface BundleParityState {
  kind: BundleParityKind;
  runningBundleVersion: string;
  shellDaemonVersion: string;
  shellWebVersion: string;
  targetVersion: string;
  // Pre-provenance daemons cannot distinguish a stale cache from a packaged
  // mismatch. Keep the old daemon-vs-bundle signal for truthful legacy UI.
  legacyStaleVersion: string;
}

function clean(value: string | undefined): string {
  return (value ?? '').trim();
}

function semverParts(value: string): number[] | null {
  const match = /^(\d+)\.(\d+)\.(\d+)$/.exec(value);
  return match ? match.slice(1).map(Number) : null;
}

export function isDevLikeVersion(value: string | undefined): boolean {
  const version = clean(value);
  return version === '' || version === 'dev' || version === '0.0.0' || semverParts(version) === null;
}

export function isVersionNewer(candidate: string, baseline: string): boolean {
  const left = semverParts(candidate);
  const right = semverParts(baseline);
  if (!left || !right) return !!candidate && !!baseline && candidate !== baseline;
  for (let i = 0; i < left.length; i += 1) {
    if (left[i] !== right[i]) return left[i] > right[i];
  }
  return false;
}

export function newestVersion(...values: Array<string | undefined>): string {
  return values.map(clean).filter((value) => !isDevLikeVersion(value)).reduce((best, value) => (
    !best || isVersionNewer(value, best) ? value : best
  ), '');
}

export function classifyBundleParity(input: BundleParityInput): BundleParityState {
  const runningBundleVersion = clean(input.runningBundleVersion);
  const shellDaemonVersion = clean(input.shellDaemonVersion);
  const shellWebVersion = clean(input.shellWebVersion);
  const targetVersion = clean(input.targetVersion);
  const base = {
    runningBundleVersion,
    shellDaemonVersion,
    shellWebVersion,
    targetVersion,
    legacyStaleVersion: '',
  };

  if (
    !runningBundleVersion ||
    input.harness === true ||
    isDevLikeVersion(runningBundleVersion) ||
    (shellDaemonVersion && isDevLikeVersion(shellDaemonVersion)) ||
    (shellWebVersion && isDevLikeVersion(shellWebVersion))
  ) {
    return { ...base, kind: 'ok' };
  }

  if (!shellWebVersion) {
    const legacyStaleVersion = shellDaemonVersion && shellDaemonVersion !== runningBundleVersion
      ? shellDaemonVersion
      : '';
    return { ...base, kind: 'unknown', legacyStaleVersion };
  }

  // This must precede transient-stale: in the production failure the running
  // bundle and embedded bundle were both old while the executable was new.
  // Reloading could never produce different bytes.
  if (shellDaemonVersion && shellWebVersion !== shellDaemonVersion) {
    return { ...base, kind: 'packaged-mismatch' };
  }
  if (shellWebVersion !== runningBundleVersion) {
    return { ...base, kind: 'transient-stale' };
  }
  if (targetVersion && isVersionNewer(targetVersion, shellDaemonVersion || shellWebVersion)) {
    return { ...base, kind: 'activation-gap' };
  }
  return { ...base, kind: 'ok' };
}

export const BUNDLE_RELOAD_ATTEMPT_LIMIT = 3;
export const BUNDLE_RELOAD_ATTEMPT_PREFIX = 'solu.staleBundleReloadAttempts:';

type ReloadStorage = Pick<Storage, 'getItem' | 'setItem'>;

function sessionStore(storage?: ReloadStorage): ReloadStorage | null {
  if (storage) return storage;
  try { return sessionStorage; } catch { return null; }
}

export function bundleReloadAttemptCount(targetVersion: string, storage?: ReloadStorage): number | null {
  const target = clean(targetVersion);
  const store = sessionStore(storage);
  if (!target || !store) return null;
  try {
    const parsed = Number.parseInt(store.getItem(`${BUNDLE_RELOAD_ATTEMPT_PREFIX}${target}`) ?? '0', 10);
    return Number.isFinite(parsed) && parsed > 0 ? parsed : 0;
  } catch {
    return null;
  }
}

export function beginBundleReloadAttempt(targetVersion: string, storage?: ReloadStorage): boolean {
  const target = clean(targetVersion);
  const store = sessionStore(storage);
  if (!target || !store) return false;
  try {
    const count = bundleReloadAttemptCount(target, store);
    if (count === null || count >= BUNDLE_RELOAD_ATTEMPT_LIMIT) return false;
    store.setItem(`${BUNDLE_RELOAD_ATTEMPT_PREFIX}${target}`, String(count + 1));
    store.setItem('solu.reloadAttemptedFor', target);
    return true;
  } catch {
    return false;
  }
}

export function bundleReloadAttemptsExhausted(targetVersion: string, storage?: ReloadStorage): boolean {
  const count = bundleReloadAttemptCount(targetVersion, storage);
  return count !== null && count >= BUNDLE_RELOAD_ATTEMPT_LIMIT;
}

export function bundleReloadTarget(state: BundleParityState): string {
  if (state.kind === 'transient-stale') return state.shellWebVersion;
  // Pre-provenance daemons still use the legacy daemon-vs-bundle signal. Keep
  // that update self-heal, but bound it with the same per-target session cap.
  if (state.kind === 'unknown') return state.legacyStaleVersion;
  return '';
}

export function shouldAutoReloadBundle(state: BundleParityState, storage?: ReloadStorage): boolean {
  const target = bundleReloadTarget(state);
  return !!target && beginBundleReloadAttempt(target, storage);
}
