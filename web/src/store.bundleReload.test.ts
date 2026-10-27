import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { bundleReloadAttemptCount, classifyBundleParity } from './lib/bundleParity';
import { scheduleStaleBundleReload } from './store';

describe('stale bundle reload scheduling', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    sessionStorage.clear();
  });

  afterEach(() => {
    vi.clearAllTimers();
    vi.useRealTimers();
  });

  it('deduplicates a pending reload for the same served target', () => {
    const state = classifyBundleParity({
      runningBundleVersion: '8.8.7',
      shellDaemonVersion: '8.8.8',
      shellWebVersion: '8.8.8',
      targetVersion: '8.8.8',
    });
    expect(scheduleStaleBundleReload(state)).toBe(true);
    expect(scheduleStaleBundleReload(state)).toBe(false);
    expect(bundleReloadAttemptCount('8.8.8')).toBe(1);
  });
});
