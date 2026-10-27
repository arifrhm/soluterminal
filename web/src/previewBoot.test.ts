import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, configureApi, setBackend } from './api';
import { classifyBundleParity } from './lib/bundleParity';
import { createPreviewFetch, mountedAssetURL, previewFixtureResponse } from './previewBoot';

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  configureApi({ prefix: '', token: null });
  setBackend(null);
});

describe('mounted preview boot contracts', () => {
  it('keeps element-initiated assets inside the mounted base', () => {
    expect(mountedAssetURL('icon-mark-192.png', './')).toBe('./icon-mark-192.png');
    expect(mountedAssetURL('icon-mark-192.png', '/')).toBe('/icon-mark-192.png');
  });

  it('serves parseable health and provenance fixtures during periodic polling', async () => {
    const health = previewFixtureResponse('/api/health');
    expect(health?.status).toBe(200);
    const healthBody = await health?.json() as { status?: string; version?: string; webVersion?: string };
    expect(healthBody).toMatchObject({ status: 'ok' });

    const provenance = previewFixtureResponse('/webdist.json');
    expect(provenance?.status).toBe(200);
    const provenanceBody = await provenance?.json() as { webVersion?: string };

    const runningBundleVersion = typeof __SOLU_WEB_VERSION__ === 'string' ? __SOLU_WEB_VERSION__ : 'dev';
    expect(healthBody.version).toBe(runningBundleVersion);
    expect(healthBody.webVersion).toBe(runningBundleVersion);
    expect(provenanceBody.webVersion).toBe(runningBundleVersion);
    expect(classifyBundleParity({
      runningBundleVersion,
      shellDaemonVersion: healthBody.version,
      shellWebVersion: provenanceBody.webVersion,
    }).kind).toBe('ok');
  });

  it('prefers the mounted backend over the fixture for escaping fetches', async () => {
    const nativeFetch = vi.fn<typeof fetch>(async (input) => {
      const pathname = new URL(String(input), 'https://preview.test/preview/pv_00000000000000000000000000/').pathname;
      // Escaping API calls are re-anchored under the document mount first.
      expect(pathname.startsWith('/preview/pv_00000000000000000000000000/api/')).toBe(true);
      if (pathname.endsWith('/api/health')) {
        return new Response(JSON.stringify({ status: 'ok', version: '9.9.9', live: true }), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        });
      }
      return new Response(JSON.stringify({ error: 'preview backend unavailable' }) + '\n', {
        status: 404,
        headers: { 'content-type': 'application/json' },
      });
    });
    vi.stubGlobal('fetch', createPreviewFetch(
      nativeFetch,
      'https://preview.test/preview/pv_00000000000000000000000000/',
    ));

    const health = await fetch('/api/health');
    expect(health.status).toBe(200);
    await expect(health.json()).resolves.toMatchObject({ status: 'ok', live: true });

    // No mounted backend answering: fall back to the deterministic fixture.
    const panes = await fetch('/api/panes');
    expect(panes.status).toBe(200);
    await expect(panes.json()).resolves.toEqual([]);

    const unavailable = await fetch('/api/future-boot-poll');
    expect(unavailable.status).toBe(404);
    await expect(unavailable.json()).resolves.toMatchObject({ error: expect.any(String) });
    expect(nativeFetch).toHaveBeenCalledTimes(3);
  });

  it('keeps a backend-less preview network-silent and JSON-parseable', async () => {
    const nativeFetch = vi.fn<typeof fetch>(async () => new Response(
      JSON.stringify({ error: 'preview backend unavailable' }) + '\n',
      { status: 404, headers: { 'content-type': 'application/json' } },
    ));
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
    vi.stubGlobal('fetch', createPreviewFetch(
      nativeFetch,
      'https://preview.test/preview/pv_00000000000000000000000000/',
    ));

    const health = await api.health();
    expect(health).toMatchObject({ status: 'ok' });

    const unavailable = await fetch('/api/future-boot-poll');
    expect(unavailable.status).toBe(404);
    await expect(unavailable.json()).resolves.toMatchObject({ error: expect.any(String) });
    expect(consoleError).not.toHaveBeenCalled();
  });
});
