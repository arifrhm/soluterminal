import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, configureApi } from '../api';
import { setCloudDualBackend } from '../dualBackend';
import { shouldAutoReloadBundle } from './bundleParity';
import { captureHarnessActive, driftedVersion, resolveBundleState, resolvePolledBundleState } from './useDaemonHealth';

const drifted = (daemonVersion: string, bundleVersion: string) => driftedVersion({ version: daemonVersion } as never, bundleVersion);

describe('stale bundle detection', () => {
  it('flags a daemon that swapped under a live page', () => {
    expect(drifted('0.0.974', '0.0.973')).toBe('0.0.974');
  });
  it('stays quiet when the versions match', () => {
    expect(drifted('0.0.975', '0.0.975')).toBeNull();
  });
  it('never nags in a dev server, where drift is normal', () => {
    expect(drifted('0.0.975', 'dev')).toBeNull();
    expect(drifted('dev', '0.0.975')).toBeNull();
    expect(drifted('0.0.0', '0.0.1047')).toBeNull();
    expect(drifted('models-fallbacks-evidence', '0.0.1047')).toBeNull();
  });
  it('ignores a daemon that reports no version', () => {
    expect(drifted('', '0.0.975')).toBeNull();
    expect(drifted('   ', '0.0.975')).toBeNull();
  });
});

describe('health provenance preference', () => {
  const health = (version: string, webVersion?: string) => ({ version, webVersion } as never);

  it('uses shell health provenance instead of active WSL daemon drift', () => {
    const state = resolveBundleState({
      health: health('0.0.992', '0.0.992'),
      shellHealth: health('0.0.991', '0.0.991'),
      otherHealth: health('0.0.992', '0.0.992'),
      runningBundleVersion: '0.0.991',
    });
    expect(state.kind).toBe('activation-gap');
  });

  it('uses the origin probe when shell health comes from an older daemon', () => {
    const state = resolveBundleState({
      health: health('0.0.992'),
      shellHealth: health('0.0.992'),
      probedWebVersion: '0.0.992',
      runningBundleVersion: '0.0.991',
    });
    expect(state.kind).toBe('transient-stale');
    expect(state.shellWebVersion).toBe('0.0.992');
  });

  it('prefers origin provenance when health reports different embedded bytes', () => {
    const state = resolveBundleState({
      health: health('0.0.993', '0.0.993'),
      shellHealth: health('0.0.993', '0.0.993'),
      probedWebVersion: '0.0.992',
      runningBundleVersion: '0.0.992',
    });
    expect(state.kind).toBe('packaged-mismatch');
    expect(state.shellWebVersion).toBe('0.0.992');
  });

  it('does not mistake active WSL health for shell health while Windows is unavailable', () => {
    const state = resolveBundleState({
      health: health('0.0.992'),
      shellHealth: null,
      otherHealth: health('0.0.992', '0.0.992'),
      probedWebVersion: '0.0.991',
      runningBundleVersion: '0.0.991',
    });
    expect(state.kind).toBe('activation-gap');
    expect(state.shellDaemonVersion).toBe('');
  });

  it('falls back to the legacy drift signal only when provenance is unavailable', () => {
    const state = resolveBundleState({
      health: health('0.0.992'),
      runningBundleVersion: '0.0.991',
    });
    expect(state.kind).toBe('unknown');
    expect(state.legacyStaleVersion).toBe('0.0.992');
  });

  it('keeps legacy drift recovery when a cloud origin has no provenance', () => {
    const state = resolveBundleState({
      health: health('0.0.992'),
      shellHealth: null,
      runningBundleVersion: '0.0.991',
    });
    expect(state.kind).toBe('unknown');
    expect(state.legacyStaleVersion).toBe('0.0.992');
  });

  it('accepts dev fixture provenance but preserves real semver mismatches', () => {
    expect(resolveBundleState({
      health: health('dev', 'dev'),
      probedWebVersion: '0.0.1047',
      runningBundleVersion: '0.0.1047',
    }).kind).toBe('ok');
    expect(resolveBundleState({
      health: health('0.0.990'),
      probedWebVersion: '0.0.1047',
      runningBundleVersion: '0.0.1047',
    }).kind).toBe('packaged-mismatch');
  });

  it('detects shell-bundle mismatch against the other backend in router mode', () => {
    const win = health('0.0.991', '0.0.990');
    const wsl = { version: '0.0.992', webVersion: '0.0.992' } as never;
    expect(resolveBundleState({
      health: wsl,
      shellHealth: win,
      otherHealth: wsl,
      runningBundleVersion: '0.0.991',
    }).kind).not.toBe('ok');
  });
});

describe('capture harness flag', () => {
  it('requires both a harness build and the explicit query flag', () => {
    expect(captureHarnessActive({ harnessBuild: true, search: '?solu_capture=1' })).toBe(true);
    expect(captureHarnessActive({ harnessBuild: false, search: '?solu_capture=1' })).toBe(false);
    expect(captureHarnessActive({ harnessBuild: true, search: '?solu_router=1&b=wsl' })).toBe(false);
  });

  it('suppresses bundle skew when explicitly supplied to state resolution', () => {
    expect(resolveBundleState({
      health: { version: '0.0.990' } as never,
      probedWebVersion: '0.0.1047',
      runningBundleVersion: '0.0.1047',
      harness: true,
    }).kind).toBe('ok');
  });
});

describe('polled shell-origin provenance', () => {
  const health = (version: string, webVersion?: string) => ({ version, webVersion } as never);

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    configureApi({ prefix: '', token: null });
    setCloudDualBackend(false);
    window.history.replaceState(null, '', '/');
  });

  function probe(webVersion: string) {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      headers: { get: (name: string) => name.toLowerCase() === 'content-type' ? 'application/json' : null },
      json: async () => ({ webVersion }),
    }));
  }

  it('converges from a WSL-first activation gap to reloadable origin bytes', async () => {
    window.history.replaceState(null, '', '/?solu_router=1&b=wsl');
    let shellReady = false;
    vi.spyOn(api, 'healthBackend').mockImplementation(async (backend) => (
      backend === 'win'
        ? health(shellReady ? '0.0.992' : '0.0.991', shellReady ? '0.0.992' : '0.0.991')
        : health('0.0.992', '0.0.992')
    ));
    probe('0.0.991');

    const gap = await resolvePolledBundleState(health('0.0.992', '0.0.992'), '0.0.991');
    expect(gap.kind).toBe('activation-gap');
    const attempts = new Map<string, string>();
    const storage = {
      getItem: (key: string) => attempts.get(key) ?? null,
      setItem: (key: string, value: string) => { attempts.set(key, value); },
    };
    expect(shouldAutoReloadBundle(gap, storage)).toBe(false);

    shellReady = true;
    probe('0.0.992');
    const ready = await resolvePolledBundleState(health('0.0.992', '0.0.992'), '0.0.991');
    expect(ready.kind).toBe('transient-stale');
    expect(shouldAutoReloadBundle(ready, storage)).toBe(true);
  });

  it('takes the page from WSL while WSL runs a Preview build, as the router does', async () => {
    window.history.replaceState(null, '', '/?solu_router=1&b=wsl');
    vi.spyOn(api, 'healthBackend').mockImplementation(async (backend) => (
      backend === 'win'
        ? health('0.0.1224', '0.0.1224')
        : Object.assign(health('0.0.1224.3', '0.0.1224.3'), { build: { channel: 'preview' } })
    ));
    probe('0.0.1224.3');
    const state = await resolvePolledBundleState(health('0.0.1224.3', '0.0.1224.3'), '0.0.1224.3');
    expect(state.shellDaemonVersion).toBe('0.0.1224.3');
    expect(state.shellWebVersion).toBe('0.0.1224.3');
    expect(state.kind).not.toBe('transient-stale');
  });

  it('uses cloud origin provenance instead of attributing Windows webdist to it', async () => {
    configureApi({ prefix: '/m/machine-1', token: 'test-token' });
    setCloudDualBackend(true);
    vi.spyOn(api, 'healthBackend').mockResolvedValue(health('0.0.993', '0.0.993'));
    probe('0.0.991');

    const state = await resolvePolledBundleState(health('0.0.993', '0.0.993'), '0.0.991');
    expect(state.kind).toBe('activation-gap');
    expect(state.shellDaemonVersion).toBe('');
    expect(state.shellWebVersion).toBe('0.0.991');
  });

  it('does not classify cloud single-backend rollout lag as a packaged mismatch', async () => {
    configureApi({ prefix: '/m/machine-1', token: 'test-token' });
    probe('0.0.991');

    const state = await resolvePolledBundleState(health('0.0.993', '0.0.993'), '0.0.991');
    expect(state.kind).toBe('activation-gap');
    expect(state.shellDaemonVersion).toBe('');
    expect(state.shellWebVersion).toBe('0.0.991');
    expect(state.targetVersion).toBe('0.0.993');
  });

  it('ignores an HTML SPA fallback masquerading as webdist.json', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      headers: { get: () => 'text/html; charset=utf-8' },
      json: async () => { throw new Error('must not parse HTML'); },
    }));
    const state = await resolvePolledBundleState(health('0.0.992'), '0.0.991');
    expect(state.kind).toBe('unknown');
    expect(state.legacyStaleVersion).toBe('0.0.992');
  });
});
