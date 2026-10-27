import { describe, expect, it, vi } from 'vitest';
import {
  BUNDLE_RELOAD_ATTEMPT_LIMIT,
  beginBundleReloadAttempt,
  bundleReloadAttemptCount,
  classifyBundleParity,
  isDevLikeVersion,
  newestVersion,
  shouldAutoReloadBundle,
} from './bundleParity';

const classify = (
  runningBundleVersion: string,
  shellDaemonVersion: string,
  shellWebVersion: string,
  targetVersion = '',
) => classifyBundleParity({ runningBundleVersion, shellDaemonVersion, shellWebVersion, targetVersion });

describe('bundle parity classifier', () => {
  it.each([
    ['ok', '0.0.992', '0.0.992', '0.0.992', '0.0.992'],
    ['transient-stale', '0.0.991', '0.0.992', '0.0.992', '0.0.992'],
    ['activation-gap', '0.0.991', '0.0.991', '0.0.991', '0.0.992'],
    // Exact production incident: the new daemon executable embedded the old UI.
    ['packaged-mismatch', '0.0.991', '0.0.992', '0.0.991', '0.0.992'],
    ['unknown', '0.0.991', '0.0.992', '', '0.0.992'],
  ] as const)('%s', (kind, running, daemon, web, target) => {
    expect(classify(running, daemon, web, target).kind).toBe(kind);
  });

  it('models the WSL-first activation gap until the Windows shell swaps', () => {
    const before = classify('0.0.991', '0.0.991', '0.0.991', '0.0.992');
    const after = classify('0.0.991', '0.0.992', '0.0.992', '0.0.992');
    expect(before.kind).toBe('activation-gap');
    expect(after.kind).toBe('transient-stale');
  });

  it('short-circuits dev builds', () => {
    expect(classify('dev', '0.0.992', '0.0.992').kind).toBe('ok');
    expect(classify('0.0.992', 'dev', 'dev').kind).toBe('ok');
  });

  it.each(['dev', '0.0.0', 'dev-abc123', 'models-fallbacks-evidence'])(
    'treats dev-like daemon version %s as non-production',
    (daemon) => {
      expect(classify('0.0.1047', daemon, '0.0.1047').kind).toBe('ok');
    },
  );

  it('does not let an empty daemon version assert a mismatch', () => {
    expect(classify('0.0.1047', '', '0.0.1047').kind).toBe('ok');
  });

  it('keeps the production packaged-mismatch for a real semver skew', () => {
    expect(classify('0.0.1047', '0.0.990', '0.0.1047').kind).toBe('packaged-mismatch');
    expect(classify('0.0.1047', '0.0.990', '0.0.1047', '0.0.1047').kind).toBe('packaged-mismatch');
  });

  it('suppresses a mismatch only when the harness flag is set', () => {
    const input = {
      runningBundleVersion: '0.0.1047',
      shellDaemonVersion: '0.0.990',
      shellWebVersion: '0.0.1047',
    };
    expect(classifyBundleParity({ ...input, harness: true }).kind).toBe('ok');
    expect(classifyBundleParity({ ...input, harness: false }).kind).toBe('packaged-mismatch');
  });

  it.each([
    ['', true],
    ['dev', true],
    ['0.0.0', true],
    ['dev-abc123', true],
    ['0.0.990', false],
    ['0.0.1047', false],
  ] as const)('classifies %j as dev-like=%s', (version, expected) => {
    expect(isDevLikeVersion(version)).toBe(expected);
  });

  it('excludes dev-like labels when choosing the newest version', () => {
    expect(newestVersion('models-fallbacks-evidence', '0.0.1047', 'dev')).toBe('0.0.1047');
  });
});

describe('bounded reload attempts', () => {
  function memoryStorage() {
    const values = new Map<string, string>();
    return {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => { values.set(key, value); },
      removeItem: (key: string) => { values.delete(key); },
    };
  }

  it('caps attempts independently per served target without re-arming an old target', () => {
    const storage = memoryStorage();
    for (let i = 0; i < BUNDLE_RELOAD_ATTEMPT_LIMIT; i += 1) {
      expect(beginBundleReloadAttempt('0.0.992', storage)).toBe(true);
    }
    expect(beginBundleReloadAttempt('0.0.992', storage)).toBe(false);
    expect(bundleReloadAttemptCount('0.0.992', storage)).toBe(3);

    expect(beginBundleReloadAttempt('0.0.993', storage)).toBe(true);
    expect(bundleReloadAttemptCount('0.0.993', storage)).toBe(1);
    expect(beginBundleReloadAttempt('0.0.992', storage)).toBe(false);
    expect(bundleReloadAttemptCount('0.0.992', storage)).toBe(3);
  });

  it('reloads only a transient state, including convergence after an activation gap', () => {
    const storage = memoryStorage();
    const gap = classify('0.0.991', '0.0.991', '0.0.991', '0.0.992');
    expect(shouldAutoReloadBundle(gap, storage)).toBe(false);
    expect(shouldAutoReloadBundle(gap, storage)).toBe(false);

    const ready = classify('0.0.991', '0.0.992', '0.0.992', '0.0.992');
    expect(shouldAutoReloadBundle(ready, storage)).toBe(true);
    expect(shouldAutoReloadBundle(classify('0.0.991', '0.0.992', '0.0.991'), storage)).toBe(false);
  });

  it('preserves the bounded legacy reload for daemons without provenance', () => {
    const storage = memoryStorage();
    const unknown = classify('0.0.991', '0.0.992', '', '0.0.992');
    expect(unknown).toMatchObject({ kind: 'unknown', legacyStaleVersion: '0.0.992' });
    for (let i = 0; i < BUNDLE_RELOAD_ATTEMPT_LIMIT; i += 1) {
      expect(shouldAutoReloadBundle(unknown, storage)).toBe(true);
    }
    expect(shouldAutoReloadBundle(unknown, storage)).toBe(false);
  });

  it('reads the attempt count without mutating storage', () => {
    const storage = memoryStorage();
    const setItem = vi.spyOn(storage, 'setItem');
    const removeItem = vi.spyOn(storage, 'removeItem');
    expect(bundleReloadAttemptCount('0.0.992', storage)).toBe(0);
    expect(setItem).not.toHaveBeenCalled();
    expect(removeItem).not.toHaveBeenCalled();
  });

  it('fails closed when session storage is unavailable', () => {
    const throwing = {
      getItem: () => { throw new Error('disabled'); },
      setItem: () => { throw new Error('disabled'); },
      removeItem: () => { throw new Error('disabled'); },
    };
    expect(beginBundleReloadAttempt('0.0.992', throwing)).toBe(false);
  });
});
