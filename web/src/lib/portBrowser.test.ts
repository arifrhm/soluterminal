import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { configureApi, setBackend } from '../api';
import { browserAccessUrl, browserTileSandbox, loadRouterPreviewOrigin, serviceTarget, setRouterPreviewOrigin } from './portBrowser';
import { reconnectServiceBrowsers } from './ports';
import type { BrowserServiceTarget, Port } from '../types';
const target: BrowserServiceTarget = {
  url: 'https://[::1]:5173',
  previewPath: '/api/proxy/5173~YWJj/',
  backend: 'win',
  machinePrefix: '/m/host',
};
beforeEach(() => {
  vi.stubGlobal(
    'fetch',
    vi.fn(
      async () => new Response(JSON.stringify({ ticket: 'spp1.fixture.pass' }), { status: 200 }),
    ),
  );
});

describe('saved service restart recovery', () => {
  const original: Port = { port: 5173, addr: '127.0.0.1', proto: 'tcp', pid: 1, firstAt: '', process: 'node', cmdline: '', cwd: '', paneId: 'app-pane', backend: 'win', scheme: 'http', webStatus: 'ready', previewPath: '/api/proxy/5173~b2xk/' };
  const replacement: Port = { ...original, pid: 2, previewPath: '/api/proxy/5173~bmV3/' };
  const browsers = () => ({ saved: { id: 'saved', url: 'http://127.0.0.1:5173/app?view=1#top', createdAt: '', service: serviceTarget(original) } });

  it.each([
    ['another pane', { paneId: 'different-pane' }],
    ['another backend', { backend: 'wsl' }],
    ['another address', { addr: '0.0.0.0' }],
    ['another protocol', { proto: 'tcp6' }],
    ['another port', { port: 7000 }],
    ['an unowned listener', { paneId: undefined }],
    ['an unchecked listener', { webStatus: 'unverified' }],
    ['an unbound listener', { previewPath: undefined }],
  ] as const)('refuses %s on a reused port', (_label, patch) => {
    const saved = browsers();
    expect(reconnectServiceBrowsers(saved, [{ ...replacement, ...patch }], '')).toBe(saved);
  });

  it('does not rebind from another machine or an ambiguous response', () => {
    const saved = browsers();
    expect(reconnectServiceBrowsers(saved, [replacement], '/m/other')).toBe(saved);
    expect(reconnectServiceBrowsers(saved, [replacement, replacement], '')).toBe(saved);
    expect(reconnectServiceBrowsers(saved, [], '')).toBe(saved);
  });

  it('preserves page navigation while refreshing a scheme and listener', () => {
    const saved = browsers();
    const recovered = reconnectServiceBrowsers(saved, [{ ...replacement, scheme: 'https' }], '');
    expect(recovered.saved.url).toBe('https://127.0.0.1:5173/app?view=1#top');
    expect(recovered.saved.service?.previewPath).toBe(replacement.previewPath);
    expect(saved.saved.service?.previewPath).toBe(original.previewPath);
  });

  it('leaves external navigation and legacy targets without pane provenance alone', () => {
    const saved = browsers();
    saved.saved.url = 'https://example.com/app';
    expect(reconnectServiceBrowsers(saved, [replacement], '')).toBe(saved);
    saved.saved.url = original.scheme + '://127.0.0.1:5173';
    saved.saved.service.paneId = undefined;
    expect(reconnectServiceBrowsers(saved, [replacement], '')).toBe(saved);
  });
});
afterEach(() => {
  vi.unstubAllGlobals();
  configureApi({ prefix: '', token: null });
  setBackend(null);
});
describe('service browser routing', () => {
  it('keeps query and fragment separate from the scoped pass', async () => {
    configureApi({ prefix: '/m/host', token: 'a +/token' });
    const result = await browserAccessUrl('http://localhost:5173/app?view=1#section');
    const url = new URL(result!, 'https://cloud.test');
    expect(url.pathname).toBe('/m/host/api/proxy/5173/app');
    expect(url.searchParams.get('view')).toBe('1');
    expect(url.searchParams.get('access_token')).toBe('spp1.fixture.pass');
    expect(url.hash).toBe('#section');
  });
  it('uses the selected listener backend instead of the current backend', async () => {
    configureApi({ prefix: '/m/host', token: 'secret' });
    setBackend('wsl');
    expect(await browserAccessUrl('https://[::1]:5173/path?q=1#top', target)).toBe(
      '/m/host/b/win/api/proxy/5173~YWJj/path?q=1&access_token=spp1.fixture.pass#top',
    );
  });
  it('uses the backend route on desktop without adding a cloud credential', async () => {
    configureApi({ prefix: '', token: 'local-secret' });
    expect(await browserAccessUrl(target.url, { ...target, machinePrefix: '' })).toBe(
      '/b/win/api/proxy/5173~YWJj/',
    );
  });
  // On a dual-backend desktop the router serves previews on an origin of its
  // own: they keep scripts and data, and cannot act as the app.
  it('opens desktop router previews on the router preview origin', async () => {
    configureApi({ prefix: '', token: 'local-secret' });
    setRouterPreviewOrigin('http://127.0.0.1:41000');
    try {
      const url = await browserAccessUrl(target.url, { ...target, machinePrefix: '' });
      expect(url).toBe('http://127.0.0.1:41000/b/win/api/proxy/5173~YWJj/');
      expect(browserTileSandbox(url!)).toContain('allow-same-origin');
    } finally {
      setRouterPreviewOrigin(null);
    }
  });
  it('refuses a saved service from another machine', async () => {
    configureApi({ prefix: '/m/other', token: 'other-secret' });
    expect(await browserAccessUrl(target.url, target)).toBeNull();
  });
  it('never adds cloud credentials to external sites', async () => {
    configureApi({ prefix: '/m/host', token: 'secret' });
    expect(await browserAccessUrl('https://example.com/app', target)).toBe(
      'https://example.com/app',
    );
  });
  it('routes generic cloud localhost URLs through the current backend', async () => {
    configureApi({ prefix: '/m/host', token: null });
    setBackend('wsl');
    expect(await browserAccessUrl('http://localhost:3000/')).toBe('/m/host/b/wsl/api/proxy/3000/');
  });
});

describe('browserTileSandbox', () => {
  it('never lets a page on the app\'s own origin keep that origin', () => {
    for (const src of ['/api/proxy/8981/', '/m/mach_1/api/proxy/3000~abc/', `${location.origin}/api/preview-serve/p1/`, '/m/mach_1/b/wsl/api/proxy/5173/']) {
      expect(browserTileSandbox(src), src).not.toContain('allow-same-origin');
      expect(browserTileSandbox(src)).toContain('allow-scripts');
    }
  });

  it('keeps a localhost dev server on its own origin', () => {
    expect(browserTileSandbox('http://localhost:59173/')).toContain('allow-same-origin');
  });

  it('leaves desktop router previews as they were until they get their own origin', () => {
    expect(browserTileSandbox('/b/wsl/api/proxy/5173~abc/')).toContain('allow-same-origin');
  });
});

// After an in-app update the desktop keeps its old router until the app
// restarts. The new web app tells that apart from a router that is down.
describe('loadRouterPreviewOrigin', () => {
  const inRouterMode = async (answer: () => Promise<Response>) => {
    const before = window.location.href;
    window.history.replaceState(null, '', '/?solu_router=1');
    vi.stubGlobal('fetch', vi.fn(answer));
    try {
      return await loadRouterPreviewOrigin();
    } finally {
      window.history.replaceState(null, '', before);
      setRouterPreviewOrigin(null);
    }
  };

  it('is ready when the router publishes its preview origin', async () => {
    expect(await inRouterMode(async () => new Response(JSON.stringify({ origin: 'http://127.0.0.1:41000' }), { headers: { 'Content-Type': 'application/json' } }))).toBe('ready');
  });

  it('spots a router from before the update: its shell answers with the page', async () => {
    expect(await inRouterMode(async () => new Response('<!doctype html>', { headers: { 'Content-Type': 'text/html; charset=utf-8' } }))).toBe('old-router');
  });

  it('says nothing for a router that is down', async () => {
    expect(await inRouterMode(async () => { throw new TypeError('Failed to fetch'); })).toBe('unknown');
    expect(await inRouterMode(async () => new Response('down', { status: 502 }))).toBe('unknown');
  });

  it('does nothing outside router mode', async () => {
    expect(await loadRouterPreviewOrigin()).toBe('unknown');
  });
});
