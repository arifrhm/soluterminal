import { fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api, configureApi } from '../api';
import { serviceTarget } from '../lib/portBrowser';
import { useStore } from '../store';
import { __recomputeEmbedForTests } from '../embed';
import type { BrowserInfo } from '../types';
import { BrowserPane } from './BrowserPane';

const tauri = vi.hoisted(() => ({
  openExternalWindow: vi.fn(),
}));

vi.mock('../tauri-browser', () => ({
  browserPaneClose: vi.fn(),
  browserPaneCreate: vi.fn(),
  browserPaneNavigate: vi.fn(),
  browserPanePing: vi.fn(),
  browserPaneSetBounds: vi.fn(),
  browserPaneSetVisible: vi.fn(),
  isDesktopApp: vi.fn(() => false),
  openExternalWindow: tauri.openExternalWindow,
}));

const browserInfo = (url: string): BrowserInfo => ({
  id: 'browser_test',
  url,
  title: 'Dev server',
  createdAt: '2026-05-18T00:00:00.000Z',
});

describe('BrowserPane cloud proxy URLs', () => {
  const JWT = 'jwt token/with+symbols';
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchMock = vi.fn(async () => new Response(JSON.stringify({ ticket: 'spp1.123.mac', expiresInSec: 3600 }), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);
  });

  afterEach(() => {
    configureApi({ prefix: '', token: null });
    __recomputeEmbedForTests();
    tauri.openExternalWindow.mockReset();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('loads localhost iframe URLs through the cloud proxy with a scoped pass, never the sign-in token', async () => {
    configureApi({ prefix: '/m/machine-1', token: JWT });

    render(<BrowserPane info={browserInfo('http://localhost:5173/app?x=1')} />);

    const iframe = await screen.findByTitle('Dev server');
    expect(iframe).toHaveAttribute('src', '/m/machine-1/api/proxy/5173/app?x=1&access_token=spp1.123.mac');
    expect(iframe.getAttribute('src')).not.toContain(encodeURIComponent(JWT));
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/relay/ticket');
    expect(new Headers(init.headers).get('Authorization')).toBe(`Bearer ${JWT}`);
    expect(JSON.parse(String(init.body))).toEqual({ path: '/m/machine-1/api/proxy/5173/', kind: 'proxy' });
    expect(screen.getByDisplayValue('http://localhost:5173/app?x=1')).toBeInTheDocument();
  });

  it('uses the same proxied localhost URL when opening a browser tile popout', async () => {
    configureApi({ prefix: '/m/machine-1', token: 'jwt-token' });

    render(<BrowserPane info={browserInfo('http://127.0.0.1:3000/')} />);
    fireEvent.click(screen.getByTitle('Open in a new window'));

    await vi.waitFor(() => expect(tauri.openExternalWindow).toHaveBeenCalledWith(
      '/m/machine-1/api/proxy/3000/?access_token=spp1.123.mac',
      'Dev server',
    ));
  });

  it('does not mint or attach anything to non-localhost iframe URLs', () => {
    configureApi({ prefix: '/m/machine-1', token: 'jwt-token' });

    render(<BrowserPane info={browserInfo('https://example.com/dashboard')} />);

    expect(screen.getByTitle('Dev server')).toHaveAttribute('src', 'https://example.com/dashboard');
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('reloads a restarted service with its new listener selector and scoped pass', async () => {
    configureApi({ prefix: '/m/restart-machine', token: JWT });
    const original = { port: 5173, addr: '127.0.0.1', proto: 'tcp', pid: 1, firstAt: '', process: 'node', cmdline: '', cwd: '', paneId: 'app', scheme: 'http' as const, webStatus: 'ready' as const, previewPath: '/api/proxy/5173~b2xk/' };
    const info = { ...browserInfo('http://127.0.0.1:5173/app?view=1#top'), service: serviceTarget(original) };
    useStore.setState({ browsers: { [info.id]: info } });
    function LiveBrowser() {
      const saved = useStore((s) => s.browsers[info.id]);
      return <BrowserPane info={saved} />;
    }
    vi.spyOn(api, 'listPorts').mockResolvedValue([{ ...original, pid: 2, previewPath: '/api/proxy/5173~bmV3/' }]);
    render(<LiveBrowser />);
    await vi.waitFor(() => expect(screen.getByTitle('Dev server').getAttribute('src')).toContain('/5173~b2xk/'));
    fireEvent.click(screen.getByTitle('Reload'));
    await vi.waitFor(() => expect(screen.getByTitle('Dev server')).toHaveAttribute('src', '/m/restart-machine/api/proxy/5173~bmV3/app?view=1&access_token=spp1.123.mac#top'));
    // Routing changes also probe daemon health; inspect only scoped-pass requests.
    const ticketCalls = fetchMock.mock.calls.filter(([url]) => url === '/api/relay/ticket');
    const scopes = ticketCalls.map(([, init]) => JSON.parse(String((init as RequestInit).body)).path);
    expect(scopes).toContain('/m/restart-machine/api/proxy/5173~bmV3/');
  });
});

describe('BrowserPane shinoque Web action', () => {
  const embedded = {
    framed: true,
    search: '?embed=shinoque',
    saved: null,
    ancestorOrigin: 'http://127.0.0.1:4700',
    referrer: '',
  };

  afterEach(() => {
    __recomputeEmbedForTests();
    vi.restoreAllMocks();
  });

  it('does not show the action outside embed mode', () => {
    __recomputeEmbedForTests({ ...embedded, framed: false });
    render(<BrowserPane info={browserInfo('https://example.com/')} />);

    expect(screen.queryByRole('button', { name: 'Open in shinoque Web' })).not.toBeInTheDocument();
  });

  it('opens the current pane URL in shinoque Web only when clicked', () => {
    __recomputeEmbedForTests(embedded);
    const post = vi.spyOn(window.parent, 'postMessage').mockImplementation(() => {});
    const { rerender } = render(<BrowserPane info={browserInfo('http://localhost:5173/old')} />);
    const button = screen.getByRole('button', { name: 'Open in shinoque Web' });

    expect(button).toBeEnabled();
    expect(post).not.toHaveBeenCalled();
    rerender(<BrowserPane info={browserInfo('http://localhost:5173/current?view=1#section')} />);
    fireEvent.click(button);

    expect(post).toHaveBeenCalledExactlyOnceWith(
      { source: 'soluterminal', v: 1, kind: 'open-url', url: 'http://localhost:5173/current?view=1#section' },
      'http://127.0.0.1:4700',
    );
  });

  it.each(['file:///tmp/page.html', 'mailto:help@example.com', 'ftp://example.com/', 'not a url'])
    ('disables the action for a non-HTTP(S) URL: %s', (url) => {
      __recomputeEmbedForTests(embedded);
      const post = vi.spyOn(window.parent, 'postMessage').mockImplementation(() => {});
      render(<BrowserPane info={browserInfo(url)} />);

      const button = screen.getByRole('button', { name: 'Open in shinoque Web' });
      expect(button).toBeDisabled();
      fireEvent.click(button);
      expect(post).not.toHaveBeenCalled();
    });
});
