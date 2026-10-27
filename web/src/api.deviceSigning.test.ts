import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('./lib/deviceIdentity', async (orig) => {
  const real = await orig<typeof import('./lib/deviceIdentity')>();
  return {
    ...real,
    signDeviceRequest: vi.fn(async (target: string) => ({ id: 'dev_t', time: '1', nonce: 'n', target, sig: 'sig' })),
  };
});

import { api, configureApi, signStreamUrl } from './api';
import { markDeviceEnrolled, signDeviceRequest } from './lib/deviceIdentity';

describe('approved-device request signing', () => {
  let fetchMock: ReturnType<typeof vi.fn>;
  beforeEach(() => {
    localStorage.clear();
    fetchMock = vi.fn(async () => new Response('{"cmd":[]}', { status: 200, headers: { 'content-type': 'application/json' } }));
    vi.stubGlobal('fetch', fetchMock);
  });
  afterEach(() => {
    configureApi({ prefix: '', token: null });
    vi.unstubAllGlobals();
    vi.mocked(signDeviceRequest).mockClear();
  });

  const sentHeaders = () => new Headers((fetchMock.mock.calls[0][1] as RequestInit).headers);

  it('leaves unenrolled machines unsigned', async () => {
    configureApi({ prefix: '/m/mach_x', token: 'jwt' });
    await api.listRemoteDevices().catch(() => null);
    expect(sentHeaders().get('X-Solu-Device')).toBeNull();
    expect(signDeviceRequest).not.toHaveBeenCalled();
  });

  it('signs relayed requests to an enrolled machine', async () => {
    markDeviceEnrolled('mach_x');
    configureApi({ prefix: '/m/mach_x', token: 'jwt' });
    await api.listRemoteDevices().catch(() => null);
    expect(sentHeaders().get('X-Solu-Device')).toBe('dev_t');
    expect(sentHeaders().get('X-Solu-Device-Target')).toBe('mach_x');
    expect(signDeviceRequest).toHaveBeenCalledWith('mach_x', 'GET', '/api/remote-devices', '', expect.any(Uint8Array));
  });

  it('never signs local requests', async () => {
    markDeviceEnrolled('mach_x');
    await api.listRemoteDevices().catch(() => null);
    expect(sentHeaders().get('X-Solu-Device')).toBeNull();
  });

  it('signs the decoded path the daemon checks', async () => {
    markDeviceEnrolled('mach_x');
    configureApi({ prefix: '/m/mach_x', token: 'jwt' });
    await signStreamUrl('wss://cloud.example/m/mach_x/api/workspaces/My%20Work%40home/events');
    expect(signDeviceRequest).toHaveBeenCalledWith('mach_x', 'GET', '/api/workspaces/My Work@home/events', '', expect.any(Uint8Array));
  });

  it('leaves out the query parameters the cloud strips', async () => {
    markDeviceEnrolled('mach_x');
    configureApi({ prefix: '/m/mach_x', token: 'jwt' });
    await signStreamUrl('wss://cloud.example/m/mach_x/api/ui/events?ticket=t1&access_token=a&x=1');
    expect(signDeviceRequest).toHaveBeenCalledWith('mach_x', 'GET', '/api/ui/events', 'x=1', expect.any(Uint8Array));
  });

  it('signs stream URLs for an enrolled machine with the daemon-side path', async () => {
    markDeviceEnrolled('mach_x');
    configureApi({ prefix: '/m/mach_x', token: 'jwt' });
    const url = await signStreamUrl('wss://cloud.example/m/mach_x/api/panes/p1/io?cols=80');
    expect(url).toContain('solu_dev=dev_t');
    expect(signDeviceRequest).toHaveBeenCalledWith('mach_x', 'GET', '/api/panes/p1/io', 'cols=80', expect.any(Uint8Array));
  });
});
