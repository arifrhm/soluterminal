// A WebSocket client (RFC 6455) over one private relay stream, shaped like
// the browser WebSocket the app already uses: send, close, readyState,
// bufferedAmount, protocol, binaryType, on* handlers and addEventListener.
//
// The source can also turn out to be an ordinary WebSocket (the fallback
// when the channel cannot open); the object then forwards to it, so call
// sites hold one object either way.

import { b64, bs, concat, toBytes, utf8 } from './bytes';
import { encodeRequest, readResponseHead } from './http';
import type { MuxStream } from './mux';
import { ByteReader } from './reader';

const CONNECTING = 0;
const OPEN = 1;
const CLOSING = 2;
const CLOSED = 3;

const OP_CONT = 0x0;
const OP_TEXT = 0x1;
const OP_BINARY = 0x2;
const OP_CLOSE = 0x8;
const OP_PING = 0x9;
const OP_PONG = 0xa;

const MAX_MESSAGE = 256 << 20;
const CLOSE_TIMEOUT_MS = 5_000;
const WS_GUID = '258EAFA5-E914-47DA-95CA-C5AB0DC85B11';

/** One client frame, masked as RFC 6455 requires of clients. */
export function encodeWsFrame(opcode: number, payload: Uint8Array, fin = true, mask?: Uint8Array): Uint8Array {
  const n = payload.length;
  const lenBytes = n < 126 ? 0 : n <= 0xffff ? 2 : 8;
  const out = new Uint8Array(2 + lenBytes + 4 + n);
  out[0] = (fin ? 0x80 : 0) | opcode;
  const dv = new DataView(out.buffer);
  if (lenBytes === 0) out[1] = 0x80 | n;
  else if (lenBytes === 2) {
    out[1] = 0x80 | 126;
    dv.setUint16(2, n, false);
  } else {
    out[1] = 0x80 | 127;
    dv.setBigUint64(2, BigInt(n), false);
  }
  const key = mask ?? crypto.getRandomValues(new Uint8Array(4));
  const at = 2 + lenBytes;
  out.set(key, at);
  for (let i = 0; i < n; i++) out[at + 4 + i] = payload[i] ^ key[i & 3];
  return out;
}

export interface WsFrame {
  fin: boolean;
  opcode: number;
  payload: Uint8Array;
}

/** Reads one frame (server frames are unmasked; a masked one is unmasked). */
export async function readWsFrame(reader: ByteReader): Promise<WsFrame | null> {
  const first = await reader.readSome(1);
  if (first === null) return null;
  const b0 = first[0];
  const b1 = (await reader.readExact(1))[0];
  if (b0 & 0x70) throw new WsProtocolError(1002, 'reserved bits set');
  let len = b1 & 0x7f;
  if (len === 126) {
    const b = await reader.readExact(2);
    len = (b[0] << 8) | b[1];
  } else if (len === 127) {
    const b = await reader.readExact(8);
    const big = new DataView(b.buffer, b.byteOffset, 8).getBigUint64(0, false);
    if (big > BigInt(MAX_MESSAGE)) throw new WsProtocolError(1009, 'frame too large');
    len = Number(big);
  }
  if (len > MAX_MESSAGE) throw new WsProtocolError(1009, 'frame too large');
  const mask = b1 & 0x80 ? (await reader.readExact(4)).slice() : null;
  const payload = (await reader.readExact(len)).slice();
  if (mask) for (let i = 0; i < payload.length; i++) payload[i] ^= mask[i & 3];
  return { fin: (b0 & 0x80) !== 0, opcode: b0 & 0x0f, payload };
}

export class WsProtocolError extends Error {
  constructor(readonly code: number, message: string) {
    super(`websocket: ${message}`);
  }
}

export async function wsAccept(key: string): Promise<string> {
  return b64(new Uint8Array(await crypto.subtle.digest('SHA-1', bs(utf8.encode(key + WS_GUID)))));
}

function makeEvent(type: string): Event {
  return new Event(type);
}

function makeMessageEvent(data: unknown): MessageEvent {
  if (typeof MessageEvent === 'function') return new MessageEvent('message', { data });
  return Object.assign(new Event('message'), { data }) as MessageEvent;
}

function makeCloseEvent(code: number, reason: string, wasClean: boolean): CloseEvent {
  if (typeof CloseEvent === 'function') return new CloseEvent('close', { code, reason, wasClean });
  return Object.assign(new Event('close'), { code, reason, wasClean }) as CloseEvent;
}

export type SocketSource =
  | { stream: MuxStream; path: string; forwardedPrefix: string }
  | { socket: WebSocket };

export class ChannelWebSocket extends EventTarget {
  static readonly CONNECTING = CONNECTING;
  static readonly OPEN = OPEN;
  static readonly CLOSING = CLOSING;
  static readonly CLOSED = CLOSED;
  readonly CONNECTING = CONNECTING;
  readonly OPEN = OPEN;
  readonly CLOSING = CLOSING;
  readonly CLOSED = CLOSED;

  onopen: ((this: WebSocket, ev: Event) => unknown) | null = null;
  onmessage: ((this: WebSocket, ev: MessageEvent) => unknown) | null = null;
  onclose: ((this: WebSocket, ev: CloseEvent) => unknown) | null = null;
  onerror: ((this: WebSocket, ev: Event) => unknown) | null = null;
  readonly extensions = '';

  private state = CONNECTING;
  private binary: BinaryType = 'blob';
  private stream: MuxStream | null = null;
  private real: WebSocket | null = null;
  private selected = '';
  private closeSent = false;
  private closeTimer: ReturnType<typeof setTimeout> | undefined;
  private finished = false;
  private pendingBlobs = 0;
  private outChain: Promise<void> = Promise.resolve();
  private queuedAfterClose = 0;

  constructor(readonly url: string, source: Promise<SocketSource>, private readonly protocols: string[] = []) {
    super();
    for (const type of ['open', 'message', 'close', 'error'] as const) {
      this.addEventListener(type, (ev) => {
        const h = (this as unknown as Record<string, ((ev: Event) => unknown) | null>)[`on${type}`];
        h?.call(this, ev);
      });
    }
    source.then(
      (s) => {
        if ('socket' in s) this.adopt(s.socket);
        else void this.run(s.stream, s.path, s.forwardedPrefix);
      },
      () => this.finish(1006, '', false, true),
    );
  }

  get readyState(): number {
    return this.real ? this.real.readyState : this.state;
  }

  get protocol(): string {
    return this.real ? this.real.protocol : this.selected;
  }

  get bufferedAmount(): number {
    if (this.real) return this.real.bufferedAmount;
    return (this.stream?.bufferedAmount ?? 0) + this.queuedAfterClose;
  }

  get binaryType(): BinaryType {
    return this.binary;
  }

  set binaryType(t: BinaryType) {
    if (t !== 'blob' && t !== 'arraybuffer') return;
    this.binary = t;
    if (this.real) this.real.binaryType = t;
  }

  send(data: string | ArrayBufferLike | Blob | ArrayBufferView): void {
    if (this.real) {
      this.real.send(data);
      return;
    }
    if (this.state === CONNECTING) throw new DOMException('WebSocket is not open', 'InvalidStateError');
    if (this.state !== OPEN || !this.stream) {
      // As a browser does: dropped, but counted.
      this.queuedAfterClose += typeof data === 'string'
        ? utf8.encode(data).length
        : typeof Blob !== 'undefined' && data instanceof Blob ? data.size : (data as ArrayBuffer).byteLength;
      return;
    }
    const opcode = typeof data === 'string' ? OP_TEXT : OP_BINARY;
    const isBlob = typeof Blob !== 'undefined' && data instanceof Blob;
    if (!isBlob && this.pendingBlobs === 0) {
      this.writeFrame(opcode, typeof data === 'string' ? utf8.encode(data) : bytesNow(data as ArrayBufferLike | ArrayBufferView));
      return;
    }
    // Keep message order behind an async Blob read.
    this.pendingBlobs++;
    this.outChain = this.outChain
      .then(() => toBytes(data))
      .then((bytes) => {
        if (this.state === OPEN) this.writeFrame(opcode, bytes);
      })
      .catch(() => {})
      .finally(() => {
        this.pendingBlobs--;
      });
  }

  close(code?: number, reason?: string): void {
    if (this.real) {
      this.real.close(code, reason);
      return;
    }
    if (code !== undefined && code !== 1000 && (code < 3000 || code > 4999)) {
      throw new DOMException('invalid close code', 'InvalidAccessError');
    }
    const reasonBytes = utf8.encode(reason ?? '');
    if (reasonBytes.length > 123) throw new DOMException('close reason too long', 'SyntaxError');
    if (this.state === CLOSING || this.state === CLOSED) return;
    if (this.state === CONNECTING || !this.stream) {
      this.state = CLOSING;
      this.stream?.close();
      this.finish(1006, '', false, true);
      return;
    }
    this.state = CLOSING;
    this.sendClose(code, reasonBytes);
    this.closeTimer = setTimeout(() => {
      this.stream?.close();
      this.finish(code ?? 1005, reason ?? '', false);
    }, CLOSE_TIMEOUT_MS);
  }

  private adopt(ws: WebSocket): void {
    if (this.state === CLOSED || this.state === CLOSING) {
      ws.close();
      return;
    }
    this.real = ws;
    ws.binaryType = this.binary;
    ws.onopen = (ev) => this.dispatchEvent(cloneEvent(ev));
    ws.onmessage = (ev) => this.dispatchEvent(makeMessageEvent(ev.data));
    ws.onerror = () => this.dispatchEvent(makeEvent('error'));
    ws.onclose = (ev) => this.dispatchEvent(makeCloseEvent(ev.code, ev.reason, ev.wasClean));
  }

  private writeFrame(opcode: number, payload: Uint8Array): void {
    try {
      this.stream?.write(encodeWsFrame(opcode, payload));
    } catch {
      // The stream ended; the read loop reports the close.
    }
  }

  private sendClose(code: number | undefined, reason: Uint8Array): void {
    if (this.closeSent) return;
    this.closeSent = true;
    let payload: Uint8Array = new Uint8Array(0);
    if (code !== undefined) {
      payload = concat(new Uint8Array([code >> 8, code & 0xff]), reason);
    }
    this.writeFrame(OP_CLOSE, payload);
  }

  private async run(stream: MuxStream, path: string, forwardedPrefix: string): Promise<void> {
    this.stream = stream;
    if (this.state !== CONNECTING) {
      stream.close();
      return;
    }
    const reader = new ByteReader(stream);
    try {
      const key = b64(crypto.getRandomValues(new Uint8Array(16)));
      const upgrade: Array<[string, string]> = [
        ['Upgrade', 'websocket'],
        ['Connection', 'Upgrade'],
        ['Sec-WebSocket-Key', key],
        ['Sec-WebSocket-Version', '13'],
      ];
      if (this.protocols.length) upgrade.push(['Sec-WebSocket-Protocol', this.protocols.join(', ')]);
      stream.write(encodeRequest({ method: 'GET', path, headers: new Headers(), body: null, forwardedPrefix }, { upgrade }));
      const head = await readResponseHead(reader);
      const get = (name: string) => head.headers.find(([k]) => k.toLowerCase() === name)?.[1] ?? '';
      if (head.status !== 101) throw new Error(`websocket: upgrade refused (${head.status})`);
      if (get('upgrade').toLowerCase() !== 'websocket' || !/(^|,)\s*upgrade\s*(,|$)/i.test(get('connection'))) {
        throw new Error('websocket: not an upgrade');
      }
      if (get('sec-websocket-accept') !== (await wsAccept(key))) throw new Error('websocket: bad accept key');
      const selected = get('sec-websocket-protocol');
      if (selected && !this.protocols.includes(selected)) throw new Error('websocket: unexpected subprotocol');
      this.selected = selected;
    } catch {
      stream.close();
      this.finish(1006, '', false, true);
      return;
    }
    if (this.state !== CONNECTING) {
      stream.close();
      return;
    }
    this.state = OPEN;
    this.dispatchEvent(makeEvent('open'));
    await this.readLoop(reader, stream);
  }

  private async readLoop(reader: ByteReader, stream: MuxStream): Promise<void> {
    let msgOpcode = -1;
    let parts: Uint8Array[] = [];
    let size = 0;
    const decoder = new TextDecoder('utf-8', { fatal: true });
    try {
      for (;;) {
        const f = await readWsFrame(reader);
        if (f === null) break;
        if (f.opcode >= 0x8) {
          if (!f.fin || f.payload.length > 125) throw new WsProtocolError(1002, 'bad control frame');
          if (f.opcode === OP_PING) {
            if (this.state === OPEN) this.writeFrame(OP_PONG, f.payload);
          } else if (f.opcode === OP_CLOSE) {
            let code = 1005;
            let reason = '';
            if (f.payload.length === 1) throw new WsProtocolError(1002, 'bad close frame');
            if (f.payload.length >= 2) {
              code = (f.payload[0] << 8) | f.payload[1];
              reason = decoder.decode(f.payload.subarray(2));
            }
            this.sendClose(f.payload.length >= 2 ? code : undefined, new Uint8Array(0));
            this.state = CLOSING;
            stream.close();
            this.finish(code, reason, true);
            return;
          } else if (f.opcode !== OP_PONG) {
            throw new WsProtocolError(1002, 'unknown opcode');
          }
          continue;
        }
        if (f.opcode === OP_CONT) {
          if (msgOpcode < 0) throw new WsProtocolError(1002, 'unexpected continuation');
        } else if (f.opcode === OP_TEXT || f.opcode === OP_BINARY) {
          if (msgOpcode >= 0) throw new WsProtocolError(1002, 'interleaved message');
          msgOpcode = f.opcode;
        } else {
          throw new WsProtocolError(1002, 'unknown opcode');
        }
        size += f.payload.length;
        if (size > MAX_MESSAGE) throw new WsProtocolError(1009, 'message too large');
        parts.push(f.payload);
        if (!f.fin) continue;
        const whole = parts.length === 1 ? parts[0] : concat(...parts);
        const op = msgOpcode;
        msgOpcode = -1;
        parts = [];
        size = 0;
        if (this.state !== OPEN && this.state !== CLOSING) continue;
        let data: string | ArrayBuffer | Blob;
        if (op === OP_TEXT) data = decoder.decode(whole);
        else if (this.binary === 'arraybuffer') data = whole.buffer.slice(whole.byteOffset, whole.byteOffset + whole.byteLength) as ArrayBuffer;
        else data = new Blob([whole as unknown as BlobPart]);
        this.dispatchEvent(makeMessageEvent(data));
      }
    } catch (err) {
      if (err instanceof WsProtocolError || err instanceof TypeError) {
        this.sendClose(err instanceof WsProtocolError ? err.code : 1007, new Uint8Array(0));
      }
    }
    stream.close();
    this.finish(1006, '', false, true);
  }

  private finish(code: number, reason: string, wasClean: boolean, error = false): void {
    if (this.finished) return;
    this.finished = true;
    if (this.closeTimer !== undefined) clearTimeout(this.closeTimer);
    this.state = CLOSED;
    if (error) this.dispatchEvent(makeEvent('error'));
    this.dispatchEvent(makeCloseEvent(code, reason, wasClean));
  }
}

function bytesNow(data: ArrayBufferLike | ArrayBufferView): Uint8Array {
  if (ArrayBuffer.isView(data)) return new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
  return new Uint8Array(data);
}

function cloneEvent(ev: Event): Event {
  return makeEvent(ev.type);
}
