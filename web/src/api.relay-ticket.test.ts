import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api, configureApi, mintRelayTicket } from './api';
import { shinoqueRelayURL, openShinoqueRelay } from './components/OpenShinoque';

const JWT = 'eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyIn0.c2lnbmF0dXJl';

class EventSourceStub {
  static instances: EventSourceStub[] = [];
  readyState = 0;
  onopen = null;
  onerror = null;
  onmessage = null;
  close = vi.fn();
  constructor(public url: string) { EventSourceStub.instances.push(this); }
}

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  EventSourceStub.instances = [];
  vi.stubGlobal('EventSource', EventSourceStub);
  fetchMock = vi.fn(async () => new Response(JSON.stringify({ ticket: 'rt1.minted', expiresInSec: 30 }), { status: 200 }));
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  configureApi({ prefix: '', token: null });
  vi.unstubAllGlobals();
});

describe('relay tickets keep the sign-in token out of URLs', () => {
  it('uiEventsUrl never carries a credential', () => {
    configureApi({ prefix: '/m/machine-1', token: JWT });
    expect(api.uiEventsUrl()).toBe('/m/machine-1/api/ui/events');
  });

  it('opens the relayed event stream with a ticket minted over an Authorization header', async () => {
    configureApi({ prefix: '/m/machine-1', token: JWT });
    api.openUiEvents();
    await vi.waitFor(() => expect(EventSourceStub.instances).toHaveLength(1));

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/relay/ticket');
    expect(init.method).toBe('POST');
    expect(new Headers(init.headers).get('Authorization')).toBe(`Bearer ${JWT}`);
    expect(JSON.parse(String(init.body))).toEqual({ path: '/m/machine-1/api/ui/events', kind: 'once' });

    const streamUrl = EventSourceStub.instances[0].url;
    expect(streamUrl).toBe('/m/machine-1/api/ui/events?ticket=rt1.minted');
    expect(streamUrl).not.toContain(JWT);
    expect(streamUrl).not.toContain('access_token');
  });

  it('opens local event streams without minting or any URL credential', () => {
    configureApi({ prefix: '', token: JWT });
    api.openUiEvents();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(EventSourceStub.instances[0].url).toBe('/api/ui/events');
  });

  it('rejects when the cloud refuses to mint', async () => {
    configureApi({ prefix: '/m/machine-1', token: JWT });
    fetchMock.mockResolvedValueOnce(new Response('nope', { status: 401 }));
    await expect(mintRelayTicket('/m/machine-1/api/ui/events', 'once')).rejects.toThrow(/401/);
  });

  it('builds the shinoque link without a token and navigates with a ticket', async () => {
    configureApi({ prefix: '/m/machine-1', token: JWT });
    const href = shinoqueRelayURL('machine-1', { prefix: '/m/machine-1', backendPrefix: '/b/wsl', key: 'k' }, JWT);
    expect(href).toBe('/m/machine-1/b/wsl/shinoque/');
    const navigate = vi.fn();
    await openShinoqueRelay(href!, navigate);
    expect(JSON.parse(String((fetchMock.mock.calls[0] as [string, RequestInit])[1].body)).path).toBe('/m/machine-1/b/wsl/shinoque/');
    expect(navigate).toHaveBeenCalledWith('/m/machine-1/b/wsl/shinoque/?ticket=rt1.minted');
  });
});

// Static guard: no source file may build a URL that carries the sign-in
// token. Header (Authorization, WS subprotocol) and ticket paths are fine.
describe('no web source builds a token-bearing URL', () => {
  const srcDir = __dirname;
  const files: string[] = [];
  const walk = (dir: string) => {
    for (const name of readdirSync(dir)) {
      const full = join(dir, name);
      if (statSync(full).isDirectory()) walk(full);
      else if (/\.(ts|tsx)$/.test(name) && !/\.test\.(ts|tsx)$/.test(name)) files.push(full);
    }
  };
  walk(srcDir);

  const forbidden: Array<[string, RegExp]> = [
    ['access_token query built from a variable', /[?&]access_token=\$\{/],
    ['token query built from a variable', /[?&](token|refresh_token|jwt|bearer)=\$\{/],
    ['credential set on searchParams', /searchParams\.(set|append)\(\s*['"](access_token|token|refresh_token|jwt)['"]/],
    ['URLSearchParams with a credential key', /URLSearchParams\(\{[^}]*\b(access_token|refresh_token)\s*:/],
    ['session token concatenated into a URL', /access_token=['"]\s*\+/],
    ['raw token passed to withQueryParam', /withQueryParam\([^)]*,\s*(token|currentToken\(\)|session\??\.access_token)\s*\)/],
  ];

  it('scans a meaningful number of files', () => {
    expect(files.length).toBeGreaterThan(100);
  });

  it.each(forbidden)('%s', (_label, pattern) => {
    const hits = files.filter((f) => pattern.test(readFileSync(f, 'utf8'))).map((f) => f.slice(srcDir.length));
    expect(hits).toEqual([]);
  });
});
