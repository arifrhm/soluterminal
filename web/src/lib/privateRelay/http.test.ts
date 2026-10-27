// @vitest-environment node
import { describe, expect, it } from 'vitest';
import { daemonRequestTarget, encodeRequest, httpOverStream, serializeBody } from './http';
import { fakePeer, until } from './testdata/fakePeer';

const dec = new TextDecoder();

function get(path: string, headers: Record<string, string> = {}) {
  return { method: 'GET', path, headers: new Headers(headers), body: null, forwardedPrefix: '/m/mach_1/b/wsl' };
}

async function pendingResponse(head: string, path = '/api/x') {
  const p = fakePeer();
  const st = p.mux.open();
  const resp = httpOverStream(st, get(path));
  await until(() => p.text(st.id).endsWith('\r\n\r\n'));
  p.send(st.id, head);
  return { p, st, resp };
}

describe('private relay HTTP client', () => {
  it('writes the request the daemon would get from the relay, minus the cloud’s credentials', () => {
    const bytes = encodeRequest({
      method: 'post',
      path: '/api/panes?b=2&a=1',
      headers: new Headers({
        'Content-Type': 'application/json',
        Authorization: 'Bearer jwt',
        Cookie: 'sb=1',
        'X-Solu-Device-Sig': 'sig',
        'X-Custom': 'yes',
      }),
      body: new TextEncoder().encode('{"a":1}'),
      forwardedPrefix: '/m/mach_1/b/wsl',
    });
    expect(dec.decode(bytes)).toBe(
      'POST /api/panes?b=2&a=1 HTTP/1.1\r\n' +
      'Host: daemon.tunnel.invalid\r\n' +
      'X-Forwarded-Prefix: /m/mach_1/b/wsl\r\n' +
      'content-type: application/json\r\n' +
      'x-custom: yes\r\n' +
      'Accept-Encoding: identity\r\n' +
      'Connection: close\r\n' +
      'Content-Length: 7\r\n\r\n{"a":1}',
    );
  });

  it('drops relay tickets and device signature parameters from the query', () => {
    expect(daemonRequestTarget('/api/ui/events?ticket=t&x=1&solu_dev_sig=s')).toBe('/api/ui/events?x=1');
    expect(daemonRequestTarget('/api/x?access_token=a')).toBe('/api/x');
    expect(daemonRequestTarget('/api/proxy/3000/?access_token=a')).toBe('/api/proxy/3000/?access_token=a');
    expect(daemonRequestTarget('/api/workspaces/My Work?q=a%20b')).toBe('/api/workspaces/My%20Work?q=a%20b');
  });

  it('reads a Content-Length body', async () => {
    const { p, st, resp } = await pendingResponse('HTTP/1.1 201 Created\r\nContent-Type: application/json\r\nContent-Length: 11\r\nSet-Cookie: x=1\r\n\r\n{"ok":true}');
    const r = await resp;
    expect(r.status).toBe(201);
    expect(r.statusText).toBe('Created');
    expect(r.headers.get('content-type')).toBe('application/json');
    expect(r.headers.get('set-cookie')).toBeNull();
    expect(await r.json()).toEqual({ ok: true });
    expect(p.closedByClient(st.id)).toBe(true);
  });

  it('decodes chunked bodies with extensions and trailers', async () => {
    const { resp } = await pendingResponse(
      'HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5;ext=1\r\nhello\r\n1\r\n \r\n5\r\nworld\r\n0\r\nX-Trailer: t\r\n\r\n',
    );
    expect(await (await resp).text()).toBe('hello world');
  });

  it('reads a close-delimited body until the stream ends', async () => {
    const { p, st, resp } = await pendingResponse('HTTP/1.1 200 OK\r\n\r\npart one, ');
    const r = await resp;
    p.send(st.id, 'part two');
    p.close(st.id);
    expect(await r.text()).toBe('part one, part two');
  });

  it('streams the body as it arrives (event streams)', async () => {
    const { p, st, resp } = await pendingResponse('HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n');
    const r = await resp;
    const reader = r.body!.getReader();
    p.send(st.id, 'b\r\ndata: one\n\n\r\n');
    expect(dec.decode((await reader.read()).value)).toBe('data: one\n\n');
    p.send(st.id, 'b\r\ndata: two\n\n\r\n');
    expect(dec.decode((await reader.read()).value)).toBe('data: two\n\n');
    await reader.cancel();
    expect(p.closedByClient(st.id)).toBe(true);
  });

  it('skips interim responses and gives 204 a null body', async () => {
    const { resp } = await pendingResponse('HTTP/1.1 100 Continue\r\n\r\nHTTP/1.1 204 No Content\r\n\r\n');
    const r = await resp;
    expect(r.status).toBe(204);
    expect(r.body).toBeNull();
  });

  it('fails when the stream closes before a response', async () => {
    const p = fakePeer();
    const st = p.mux.open();
    const resp = httpOverStream(st, get('/api/x'));
    p.close(st.id);
    await expect(resp).rejects.toThrow(/closed before a response/);
  });

  it('fails on a body cut short', async () => {
    const { p, st, resp } = await pendingResponse('HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\nabc');
    const r = await resp;
    p.close(st.id);
    await expect(r.text()).rejects.toThrow();
  });

  it('aborts on the caller’s signal and closes the stream', async () => {
    const p = fakePeer();
    const st = p.mux.open();
    const ctrl = new AbortController();
    const resp = httpOverStream(st, { ...get('/api/x'), signal: ctrl.signal });
    ctrl.abort();
    await expect(resp).rejects.toMatchObject({ name: 'AbortError' });
    expect(p.closedByClient(st.id)).toBe(true);
  });

  it('serialises bodies to the bytes and type a fetch would send', async () => {
    const h = new Headers();
    const bytes = await serializeBody('POST', { body: new URLSearchParams({ a: '1 2' }) }, h);
    expect(dec.decode(bytes!)).toBe('a=1+2');
    expect(h.get('content-type')).toMatch(/x-www-form-urlencoded/);
    expect(await serializeBody('GET', { body: 'x' }, new Headers())).toBeNull();
    expect(dec.decode((await serializeBody('PUT', { body: 'é' }, new Headers()))!)).toBe('é');
  });
});
