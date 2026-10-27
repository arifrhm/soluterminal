// @vitest-environment node
//
// End to end against the daemon's real /api/private-relay handler. The Go
// side (daemon/private_relay_web_e2e_test.go) starts the daemon fixture and
// runs this file with SOLU_PRIVATE_RELAY_E2E_CONFIG pointing at its details:
//
//   cd daemon && SOLU_PRIVATE_RELAY_WEB_E2E=1 go test -run TestPrivateRelayWebClientEndToEnd .
//
// Without that variable the suite is skipped.
import { readFileSync } from 'node:fs';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { b64url, bs, utf8 } from './bytes';
import { ChannelClosedError, HANDSHAKE_REFUSED, PrivateChannel } from './channel';
import { ChannelEventStream } from './eventStream';
import { httpOverStream } from './http';
import { ChannelWebSocket } from './websocket';

interface E2EConfig {
  url: string;
  deviceId: string;
  target: string;
  pinned: string;
  deviceJwk: JsonWebKey;
}

const configPath = process.env.SOLU_PRIVATE_RELAY_E2E_CONFIG;
const cfg: E2EConfig | null = configPath ? JSON.parse(readFileSync(configPath, 'utf8')) : null;
const PREFIX = '/m/mach_test01/b/wsl';

async function signer(jwk: JsonWebKey) {
  const key = await crypto.subtle.importKey('jwk', { ...jwk, ext: true }, { name: 'ECDSA', namedCurve: 'P-256' }, false, ['sign']);
  return async (m: string) => b64url(new Uint8Array(await crypto.subtle.sign({ name: 'ECDSA', hash: 'SHA-256' }, key, bs(utf8.encode(m)))));
}

describe.skipIf(!cfg)('private relay refusals against the daemon', () => {
  it('refuses a computer other than the pinned one', async () => {
    await expect(PrivateChannel.open({
      url: cfg!.url,
      deviceId: cfg!.deviceId,
      target: cfg!.target,
      pinnedIdentity: 'MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEnotthecomputer',
      sign: await signer(cfg!.deviceJwk),
    })).rejects.toThrow(/identity/);
  });

  it('is refused for another target', async () => {
    await expect(PrivateChannel.open({
      url: cfg!.url,
      deviceId: cfg!.deviceId,
      target: 'mach_other/wsl',
      pinnedIdentity: cfg!.pinned,
      sign: await signer(cfg!.deviceJwk),
    })).rejects.toThrow(/closed/);
  });
});

describe.skipIf(!cfg)('private relay channel against the daemon', () => {
  let ch: PrivateChannel;

  beforeAll(async () => {
    ch = await PrivateChannel.open({
      url: cfg!.url,
      // The cloud's sign-in offer minus the bearer; the daemon must select it
      // or a browser fails the socket.
      protocols: ['solu-auth'],
      deviceId: cfg!.deviceId,
      target: cfg!.target,
      pinnedIdentity: cfg!.pinned,
      sign: await signer(cfg!.deviceJwk),
    });
  });
  afterAll(() => ch?.close());

  const request = (method: string, path: string, init: { body?: Uint8Array; headers?: Record<string, string> } = {}) =>
    httpOverStream(ch.openStream(), {
      method, path, headers: new Headers(init.headers), body: init.body ?? null, forwardedPrefix: PREFIX,
    });

  it('GETs through the channel with the relay context and no cloud credentials', async () => {
    const r = await request('GET', '/api/echo?a=1&ticket=t', { headers: { Authorization: 'Bearer leak' } });
    expect(r.status).toBe(200);
    expect(await r.text()).toBe('GET ');
    expect(r.headers.get('x-echo-prefix')).toBe(PREFIX);
    expect(r.headers.get('x-echo-auth')).toBe('');
    expect(r.headers.get('x-echo-query')).toBe('a=1');
  });

  it('POSTs a 100 KB body', async () => {
    const body = utf8.encode('x'.repeat(100_000));
    const r = await request('POST', '/api/echo', { body, headers: { 'Content-Type': 'text/plain' } });
    const text = await r.text();
    expect(text.length).toBe(5 + 100_000);
    expect(text.startsWith('POST xxx')).toBe(true);
    expect(r.headers.get('x-echo-type')).toBe('text/plain');
  });

  it('runs concurrent requests on separate streams', async () => {
    const all = await Promise.all(Array.from({ length: 20 }, (_, i) =>
      request('POST', '/api/echo', { body: utf8.encode(`n${i}`) }).then((r) => r.text())));
    expect(all).toEqual(Array.from({ length: 20 }, (_, i) => `POST n${i}`));
  });

  it('reads a chunked response', async () => {
    const r = await request('GET', '/api/chunked');
    expect(await r.text()).toBe('part-0;part-1;part-2;part-3;part-4;');
  });

  it('streams the UI event stream', async () => {
    const es = new ChannelEventStream(Promise.resolve({
      response: (signal: AbortSignal) => httpOverStream(ch.openStream(), {
        method: 'GET', path: '/api/ui/events', headers: new Headers({ Accept: 'text/event-stream' }), body: null, forwardedPrefix: PREFIX, signal,
      }),
    }));
    const seen: string[] = [];
    await new Promise<void>((resolve) => {
      es.onopen = () => seen.push('open');
      es.onmessage = (ev) => seen.push(String(ev.data));
      es.onerror = () => resolve();
    });
    expect(seen).toEqual(['open', '{"type":"tick","n":0}', '{"type":"tick","n":1}', '{"type":"tick","n":2}']);
  });

  it('carries a WebSocket: text, binary, a fragmented-size message and the daemon’s close', async () => {
    const ws = new ChannelWebSocket(`wss://cloud${PREFIX}/api/ws?pane=p1`,
      Promise.resolve({ stream: ch.openStream(), path: '/api/ws?pane=p1', forwardedPrefix: PREFIX }), ['solu-auth']);
    ws.binaryType = 'arraybuffer';
    const got: Array<string | ArrayBuffer> = [];
    let closed: CloseEvent | null = null;
    ws.onmessage = (ev) => got.push(ev.data);
    ws.onclose = (ev) => { closed = ev; };
    await new Promise<void>((resolve, reject) => {
      ws.onopen = () => resolve();
      ws.onerror = () => reject(new Error('socket error'));
    });
    expect(ws.protocol).toBe('solu-auth');
    const big = 'k'.repeat(200_000);
    ws.send('keys');
    ws.send(new Uint8Array([1, 2, 3]));
    ws.send(big);
    const deadline = Date.now() + 10_000;
    while (got.length < 4 && Date.now() < deadline) await new Promise((r) => setTimeout(r, 5));
    expect(got[0]).toBe('hello:pane=p1');
    expect(got[1]).toBe('echo:keys');
    expect(Array.from(new Uint8Array(got[2] as ArrayBuffer))).toEqual([...utf8.encode('echo:'), 1, 2, 3]);
    expect(got[3]).toBe('echo:' + big);
    ws.send('bye');
    while (!closed && Date.now() < deadline) await new Promise((r) => setTimeout(r, 5));
    expect(closed).toMatchObject({ code: 4001, reason: 'server says bye', wasClean: true });
  });

  // Credit flow control end to end: the daemon holds back for a reader that
  // stalls, the other streams carry on, and a slow reader gets every byte.
  it('holds a stalled 4 MiB response back without freezing the others, then delivers all of it slowly', async () => {
    const flood = await request('GET', '/api/flood');
    const reader = flood.body!.getReader();
    for (let i = 0; i < 5; i++) {
      expect(await (await request('GET', '/api/echo')).text()).toBe('GET ');
    }
    let n = 0;
    let ok = true;
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      for (let i = 0; i < value.length; i++) {
        if (value[i] !== 97 + (Math.floor((n + i) / (32 << 10)) % 26)) ok = false;
      }
      n += value.length;
      if (n % (256 << 10) < value.length) await new Promise((r) => setTimeout(r, 15)); // a slow reader
    }
    expect(n).toBe(128 * (32 << 10));
    expect(ok).toBe(true);
  });

  it('uploads 4 MiB to a slow reader within its credit', async () => {
    const body = new Uint8Array(4 << 20).map((_, i) => (i * 7 + 3) & 0xff);
    let sum = 0;
    for (const b of body) sum = (sum * 31 + b) % 1000000007;
    const r = await request('POST', '/api/upload', { body });
    expect(await r.text()).toBe(`${body.length} ${sum}`);
  });

  // The daemon answers pings, so a short ping interval proves the liveness
  // check: an answering computer keeps the channel open (no false timeout).
  it('stays open on a fast ping interval because the computer answers', async () => {
    const quick = await PrivateChannel.open({
      url: cfg!.url,
      protocols: ['solu-auth'],
      deviceId: cfg!.deviceId,
      target: cfg!.target,
      pinnedIdentity: cfg!.pinned,
      sign: await signer(cfg!.deviceJwk),
      pingIntervalMs: 150,
    });
    try {
      await new Promise((r) => setTimeout(r, 1_500));
      expect(quick.isOpen).toBe(true);
      expect((quick as unknown as { peerAnswersPings: boolean }).peerAnswersPings).toBe(true);
    } finally {
      quick.close();
    }
  });

  it('keeps working after all that (records stay in step)', async () => {
    const r = await request('GET', '/api/echo');
    expect(await r.text()).toBe('GET ');
  });

  // Last: it removes the device. The daemon's side of this is
  // TestPrivateRelayEndsWhenTheDeviceIsRemoved.
  it('ends when the device is removed, and the next handshake is refused', async () => {
    const ended = new Promise<Error>((resolve) => { ch.onclose = resolve; });
    const ws = new ChannelWebSocket(`wss://cloud${PREFIX}/api/ws`,
      Promise.resolve({ stream: ch.openStream(), path: '/api/ws', forwardedPrefix: PREFIX }), ['solu-auth']);
    const closed = new Promise<CloseEvent>((resolve) => { ws.onclose = resolve; });
    await new Promise<void>((resolve) => { ws.onopen = () => resolve(); });
    expect((await request('POST', '/api/remove-me')).status).toBe(202);
    await ended;
    expect((await closed).code).toBe(1006);
    expect(ch.isOpen).toBe(false);
    const refused = await PrivateChannel.open({
      url: cfg!.url,
      deviceId: cfg!.deviceId,
      target: cfg!.target,
      pinnedIdentity: cfg!.pinned,
      sign: await signer(cfg!.deviceJwk),
    }).then(() => null, (e: unknown) => e);
    expect(refused).toBeInstanceOf(ChannelClosedError);
    expect((refused as ChannelClosedError).code).toBe(HANDSHAKE_REFUSED);
  });
});
