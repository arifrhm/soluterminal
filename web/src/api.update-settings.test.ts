import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, configureApi, setBackend } from './api';
import { setCloudDualBackend } from './dualBackend';

afterEach(() => {
  vi.unstubAllGlobals();
  setCloudDualBackend(false);
  configureApi({ prefix: '', token: null });
  setBackend(null);
});

describe('dual-backend update settings', () => {
  it('persists the install-when-idle choice on both backends', async () => {
    setCloudDualBackend(true);
    setBackend('wsl');
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ url: String(input), init });
      return new Response(JSON.stringify({ autoApply: 'when-idle' }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      });
    }));

    await expect(api.putUpdateSettings({ autoApply: 'when-idle' })).resolves.toEqual({
      autoApply: 'when-idle',
    });

    expect(calls.map((call) => call.url).sort()).toEqual([
      '/b/win/api/update/settings',
      '/b/wsl/api/update/settings',
    ]);
    for (const call of calls) {
      expect(call.init?.method).toBe('PUT');
      expect(call.init?.body).toBe(JSON.stringify({ autoApply: 'when-idle' }));
    }
  });

  it('keeps the single-backend request unchanged', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
      new Response(JSON.stringify({ autoApply: 'off' }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      }));
    vi.stubGlobal('fetch', fetchMock);

    await api.putUpdateSettings({ autoApply: 'off' });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe('/api/update/settings');
  });
});
