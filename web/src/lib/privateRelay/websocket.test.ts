// @vitest-environment node
import { describe, expect, it } from 'vitest';
import { concat, hex } from './bytes';
import { ByteReader } from './reader';
import { fakePeer, until } from './testdata/fakePeer';
import { ChannelWebSocket, encodeWsFrame, readWsFrame, wsAccept } from './websocket';

const enc = new TextEncoder();
const dec = new TextDecoder();

/** A server frame (unmasked). */
function serverFrame(opcode: number, payload: Uint8Array | string, fin = true): Uint8Array {
  const p = typeof payload === 'string' ? enc.encode(payload) : payload;
  const head = p.length < 126 ? [p.length] : [126, p.length >> 8, p.length & 0xff];
  return concat(new Uint8Array([(fin ? 0x80 : 0) | opcode, ...head]), p);
}

function readerOf(...chunks: Uint8Array[]): ByteReader {
  const q = [...chunks];
  return new ByteReader({ read: async () => q.shift() ?? null });
}

/** A ChannelWebSocket whose fake daemon has accepted the upgrade. */
async function openSocket(protocols: string[] = [], selected = '') {
  const p = fakePeer();
  const st = p.mux.open();
  const ws = new ChannelWebSocket('wss://cloud/m/mach_1/b/wsl/api/ws?x=1', Promise.resolve({ stream: st, path: '/api/ws?x=1', forwardedPrefix: '/m/mach_1/b/wsl' }), protocols);
  ws.binaryType = 'arraybuffer';
  const events: string[] = [];
  const messages: unknown[] = [];
  let closeEvent: CloseEvent | null = null;
  ws.onopen = () => events.push('open');
  ws.onmessage = (ev) => messages.push(ev.data);
  ws.onerror = () => events.push('error');
  ws.onclose = (ev) => {
    events.push('close');
    closeEvent = ev;
  };
  await until(() => p.text(st.id).includes('\r\n\r\n'));
  const req = p.text(st.id);
  const key = /Sec-WebSocket-Key: (.*)\r\n/.exec(req)![1];
  p.send(st.id, `HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ${await wsAccept(key)}\r\n${selected ? `Sec-WebSocket-Protocol: ${selected}\r\n` : ''}\r\n`);
  await until(() => events.includes('open'));
  const headLen = enc.encode(req).length;
  /** Client frames after the upgrade request. */
  const frames = async () => {
    const r = readerOf(p.dataFrom(st.id, headLen));
    const out = [];
    for (;;) {
      const f = await readWsFrame(r);
      if (!f) return out;
      out.push(f);
    }
  };
  return { p, st, ws, req, events, messages, frames, closeEvent: () => closeEvent as CloseEvent | null };
}

describe('private relay WebSocket framing', () => {
  it('masks client frames and encodes the three length forms', async () => {
    const mask = new Uint8Array([1, 2, 3, 4]);
    expect(hex(encodeWsFrame(1, enc.encode('Hi'), true, mask))).toBe('818201020304496b');
    for (const n of [0, 125, 126, 65535, 65536]) {
      const payload = new Uint8Array(n).map((_, i) => i & 0xff);
      const frame = encodeWsFrame(2, payload);
      expect(frame[1] & 0x80).toBe(0x80);
      const f = await readWsFrame(readerOf(frame.subarray(0, 3), frame.subarray(3)));
      expect(f).toMatchObject({ fin: true, opcode: 2 });
      expect(f!.payload).toEqual(payload);
    }
  });

  it('computes the RFC 6455 accept key', async () => {
    expect(await wsAccept('dGhlIHNhbXBsZSBub25jZQ==')).toBe('s3pPLMBiTxaQ9kYGzzhZRbK+xOo=');
  });

  it('rejects reserved bits', async () => {
    await expect(readWsFrame(readerOf(new Uint8Array([0xc1, 0x00])))).rejects.toThrow(/reserved/);
  });
});

describe('private relay WebSocket client', () => {
  it('upgrades with the daemon path and the inner subprotocols', async () => {
    const s = await openSocket(['solu-auth'], 'solu-auth');
    expect(s.req).toMatch(/^GET \/api\/ws\?x=1 HTTP\/1\.1\r\n/);
    expect(s.req).toContain('Upgrade: websocket\r\n');
    expect(s.req).toContain('Sec-WebSocket-Version: 13\r\n');
    expect(s.req).toContain('Sec-WebSocket-Protocol: solu-auth\r\n');
    expect(s.req).toContain('X-Forwarded-Prefix: /m/mach_1/b/wsl\r\n');
    expect(s.req).not.toContain('Connection: close');
    expect(s.ws.readyState).toBe(ChannelWebSocket.OPEN);
    expect(s.ws.protocol).toBe('solu-auth');
  });

  it('sends text and binary messages and receives both', async () => {
    const s = await openSocket();
    s.ws.send('hello');
    s.ws.send(new Uint8Array([1, 2, 3]));
    s.ws.send(new Uint8Array([9, 9, 9, 7]).subarray(3));
    const frames = await s.frames();
    expect(frames.map((f) => [f.opcode, Array.from(f.payload)])).toEqual([[1, Array.from(enc.encode('hello'))], [2, [1, 2, 3]], [2, [7]]]);

    s.p.send(s.st.id, concat(serverFrame(1, 'text'), serverFrame(2, new Uint8Array([5, 6]))));
    await until(() => s.messages.length === 2);
    expect(s.messages[0]).toBe('text');
    expect(new Uint8Array(s.messages[1] as ArrayBuffer)).toEqual(new Uint8Array([5, 6]));
  });

  it('delivers Blobs when binaryType is blob', async () => {
    const s = await openSocket();
    s.ws.binaryType = 'blob';
    s.p.send(s.st.id, serverFrame(2, new Uint8Array([1, 2])));
    await until(() => s.messages.length === 1);
    expect(new Uint8Array(await (s.messages[0] as Blob).arrayBuffer())).toEqual(new Uint8Array([1, 2]));
  });

  it('reassembles fragmented messages around control frames and answers pings', async () => {
    const s = await openSocket();
    s.p.send(s.st.id, concat(
      serverFrame(1, 'frag', false),
      serverFrame(9, 'are you there'),
      serverFrame(0, 'men', false),
      serverFrame(0, 'ted'),
    ));
    await until(() => s.messages.length === 1);
    expect(s.messages[0]).toBe('fragmented');
    await until(() => s.p.dataFrom(s.st.id).length > 0);
    const frames = await s.frames();
    expect(frames).toHaveLength(1);
    expect(frames[0].opcode).toBe(10);
    expect(dec.decode(frames[0].payload)).toBe('are you there');
  });

  it('echoes the daemon’s close and reports its code', async () => {
    const s = await openSocket();
    s.p.send(s.st.id, serverFrame(8, concat(new Uint8Array([0x0f, 0xa1]), enc.encode('bye'))));
    await until(() => s.events.includes('close'));
    expect(s.closeEvent()).toMatchObject({ code: 4001, reason: 'bye', wasClean: true });
    expect(s.ws.readyState).toBe(ChannelWebSocket.CLOSED);
    const frames = await s.frames();
    expect(frames.map((f) => [f.opcode, Array.from(f.payload)])).toEqual([[8, [0x0f, 0xa1]]]);
    expect(s.p.closedByClient(s.st.id)).toBe(true);
    expect(s.events).toEqual(['open', 'close']);
  });

  it('closes from the client side with a close handshake', async () => {
    const s = await openSocket();
    s.ws.close(1000, 'done');
    expect(s.ws.readyState).toBe(ChannelWebSocket.CLOSING);
    expect(() => s.ws.close(1001)).toThrow();
    const frames = await s.frames();
    expect(frames[0].opcode).toBe(8);
    expect(dec.decode(frames[0].payload.subarray(2))).toBe('done');
    s.p.send(s.st.id, serverFrame(8, new Uint8Array([0x03, 0xe8])));
    await until(() => s.events.includes('close'));
    expect(s.closeEvent()).toMatchObject({ code: 1000, wasClean: true });
  });

  it('reports an abnormal close when the stream ends without a close frame', async () => {
    const s = await openSocket();
    s.p.close(s.st.id);
    await until(() => s.events.includes('close'));
    expect(s.events).toEqual(['open', 'error', 'close']);
    expect(s.closeEvent()).toMatchObject({ code: 1006, wasClean: false });
  });

  it('fails the connection on a refused upgrade', async () => {
    const p = fakePeer();
    const st = p.mux.open();
    const ws = new ChannelWebSocket('wss://x/api/ws', Promise.resolve({ stream: st, path: '/api/ws', forwardedPrefix: '' }));
    const events: string[] = [];
    ws.addEventListener('error', () => events.push('error'));
    ws.addEventListener('close', () => events.push('close'));
    await until(() => p.text(st.id).includes('\r\n\r\n'));
    p.send(st.id, 'HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n');
    await until(() => events.length === 2);
    expect(events).toEqual(['error', 'close']);
    expect(() => ws.send('x')).not.toThrow(); // closed sockets drop sends
  });

  it('throws on send before open, as a browser socket does', () => {
    const ws = new ChannelWebSocket('wss://x/api/ws', new Promise(() => {}));
    expect(ws.readyState).toBe(ChannelWebSocket.CONNECTING);
    expect(() => ws.send('x')).toThrow(/not open/);
  });

  it('forwards to the ordinary socket when the channel was unavailable', async () => {
    const listeners: Record<string, (ev: unknown) => void> = {};
    const real = {
      readyState: 1, protocol: 'solu-auth', bufferedAmount: 0, binaryType: 'blob',
      sent: [] as unknown[],
      send(d: unknown) { this.sent.push(d); },
      close() {},
      set onmessage(f: (ev: unknown) => void) { listeners.message = f; },
      set onopen(f: (ev: unknown) => void) { listeners.open = f; },
      set onclose(f: (ev: unknown) => void) { listeners.close = f; },
      set onerror(f: (ev: unknown) => void) { listeners.error = f; },
    };
    const ws = new ChannelWebSocket('wss://x/api/ws', Promise.resolve({ socket: real as unknown as WebSocket }));
    ws.binaryType = 'arraybuffer';
    const got: unknown[] = [];
    ws.onmessage = (ev) => got.push(ev.data);
    await until(() => listeners.message !== undefined);
    expect(real.binaryType).toBe('arraybuffer');
    listeners.message({ data: 'hi' });
    ws.send('up');
    expect(got).toEqual(['hi']);
    expect(real.sent).toEqual(['up']);
    expect(ws.protocol).toBe('solu-auth');
  });
});
