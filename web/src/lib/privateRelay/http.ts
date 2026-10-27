// A minimal HTTP/1.1 client over one private relay stream: one request per
// stream (Connection: close). Returns a real Response whose body streams, so
// text/event-stream responses arrive as the daemon flushes them.

import { utf8 } from './bytes';
import type { MuxStream } from './mux';
import { ByteReader } from './reader';

/** What the cloud relay sets on every forwarded request (cloud/proxy.go). */
export const DAEMON_HOST = 'daemon.tunnel.invalid';

export interface StreamRequest {
  method: string;
  /** Daemon path and query, without the /m/<machine>[/b/<backend>] prefix. */
  path: string;
  headers: Headers;
  body: Uint8Array | null;
  /** The relay prefix the daemon would see as X-Forwarded-Prefix. */
  forwardedPrefix: string;
  signal?: AbortSignal | null;
}

export interface ResponseHead {
  status: number;
  statusText: string;
  headers: Array<[string, string]>;
}

// Credentials for the cloud hop, hop-by-hop fields and the per-request device
// signature (the channel's handshake authenticated the device) never go in.
const DROPPED_REQUEST_HEADERS = new Set([
  'authorization', 'cookie', 'host', 'connection', 'keep-alive', 'content-length', 'transfer-encoding',
  'te', 'trailer', 'upgrade', 'proxy-authorization', 'proxy-connection', 'x-forwarded-prefix',
  'x-solu-device', 'x-solu-device-time', 'x-solu-device-nonce', 'x-solu-device-target', 'x-solu-device-sig',
]);

/**
 * The query the daemon would receive through the cloud relay: the cloud drops
 * relay tickets and, outside /api/proxy/, access tokens (cloud/proxy.go), and
 * the device-signature parameters are meaningless inside the channel.
 */
export function daemonRequestTarget(pathAndQuery: string): string {
  const u = new URL(pathAndQuery, 'http://' + DAEMON_HOST);
  const q = u.searchParams;
  const drop = ['ticket', 'solu_dev', 'solu_dev_t', 'solu_dev_n', 'solu_dev_to', 'solu_dev_sig'];
  if (!u.pathname.startsWith('/api/proxy/')) drop.push('access_token');
  if (!drop.some((k) => q.has(k))) return u.pathname + u.search;
  for (const k of drop) q.delete(k);
  const search = q.toString();
  return u.pathname + (search ? `?${search}` : '');
}

export interface EncodeOptions {
  /** Extra headers for a WebSocket upgrade; the connection then stays open. */
  upgrade?: Array<[string, string]>;
}

function checkToken(name: string, what: string): void {
  if (!/^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/.test(name)) throw new TypeError(`private relay: bad ${what}`);
}

/** Request line and headers, plus the body when there is one. */
export function encodeRequest(req: StreamRequest, opts: EncodeOptions = {}): Uint8Array {
  const extra = opts.upgrade ?? [];
  const method = req.method.toUpperCase();
  checkToken(method, 'method');
  const target = daemonRequestTarget(req.path);
  const lines = [`${method} ${target} HTTP/1.1`, `Host: ${DAEMON_HOST}`];
  if (req.forwardedPrefix) lines.push(`X-Forwarded-Prefix: ${req.forwardedPrefix}`);
  let hasAcceptEncoding = false;
  req.headers.forEach((value, name) => {
    const lower = name.toLowerCase();
    if (DROPPED_REQUEST_HEADERS.has(lower)) return;
    if (lower === 'accept-encoding') hasAcceptEncoding = true;
    if (/[\r\n]/.test(value)) throw new TypeError('private relay: bad header value');
    lines.push(`${name}: ${value}`);
  });
  if (!hasAcceptEncoding && !extra.length) lines.push('Accept-Encoding: identity');
  for (const [k, v] of extra) lines.push(`${k}: ${v}`);
  if (!extra.length) lines.push('Connection: close');
  const body = req.body;
  if (body && body.length) lines.push(`Content-Length: ${body.length}`);
  else if (!['GET', 'HEAD', 'OPTIONS', 'DELETE'].includes(method)) lines.push('Content-Length: 0');
  // Header bytes are Latin-1; Headers already refuses anything wider.
  const head = lines.join('\r\n') + '\r\n\r\n';
  const headBytes = new Uint8Array(head.length);
  for (let i = 0; i < head.length; i++) headBytes[i] = head.charCodeAt(i) & 0xff;
  if (!body || !body.length) return headBytes;
  const out = new Uint8Array(headBytes.length + body.length);
  out.set(headBytes);
  out.set(body, headBytes.length);
  return out;
}

/** Status line and headers. Interim 1xx responses other than 101 are skipped. */
export async function readResponseHead(reader: ByteReader): Promise<ResponseHead> {
  for (;;) {
    const statusLine = await reader.readLine();
    if (statusLine === null) throw new TypeError('private relay: connection closed before a response');
    const m = /^HTTP\/1\.[01] (\d{3})(?: (.*))?$/.exec(statusLine);
    if (!m) throw new TypeError('private relay: malformed status line');
    const status = Number(m[1]);
    const headers: Array<[string, string]> = [];
    let size = 0;
    for (;;) {
      const line = await reader.readLine();
      if (line === null) throw new TypeError('private relay: connection closed in the response headers');
      if (line === '') break;
      size += line.length;
      if (size > 1 << 20) throw new TypeError('private relay: response headers too large');
      if ((line[0] === ' ' || line[0] === '\t') && headers.length) {
        headers[headers.length - 1][1] += ' ' + line.trim();
        continue;
      }
      const colon = line.indexOf(':');
      if (colon <= 0) throw new TypeError('private relay: malformed header');
      headers.push([line.slice(0, colon).trim(), line.slice(colon + 1).trim()]);
    }
    if (status >= 100 && status < 200 && status !== 101) continue;
    return { status, statusText: m[2] ?? '', headers };
  }
}

function header(head: ResponseHead, name: string): string | null {
  const found = head.headers.filter(([k]) => k.toLowerCase() === name);
  return found.length ? found.map(([, v]) => v).join(', ') : null;
}

/** The response body as chunks: chunked, Content-Length or until the stream closes. */
export async function* bodyChunks(reader: ByteReader, head: ResponseHead, method: string): AsyncGenerator<Uint8Array> {
  if (method.toUpperCase() === 'HEAD' || head.status === 204 || head.status === 304) return;
  const te = header(head, 'transfer-encoding');
  if (te && /(^|,)\s*chunked\s*$/i.test(te)) {
    for (;;) {
      const sizeLine = await reader.readLine();
      if (sizeLine === null) throw new TypeError('private relay: body ended mid-chunk');
      const size = parseInt(sizeLine.split(';')[0].trim(), 16);
      if (!Number.isFinite(size) || size < 0 || !/^[0-9a-fA-F]+$/.test(sizeLine.split(';')[0].trim())) {
        throw new TypeError('private relay: bad chunk size');
      }
      if (size === 0) {
        // Trailers, then the blank line that ends the message.
        for (;;) {
          const t = await reader.readLine();
          if (t === null || t === '') return;
        }
      }
      let left = size;
      while (left > 0) {
        const part = await reader.readSome(left);
        if (part === null) throw new TypeError('private relay: body ended mid-chunk');
        left -= part.length;
        yield part.slice();
      }
      if ((await reader.readLine()) !== '') throw new TypeError('private relay: bad chunk terminator');
    }
  }
  const cl = header(head, 'content-length');
  if (cl !== null) {
    const values = new Set(cl.split(',').map((s) => s.trim()));
    const n = Number([...values][0]);
    if (values.size !== 1 || !/^\d+$/.test([...values][0]) || !Number.isSafeInteger(n)) {
      throw new TypeError('private relay: bad Content-Length');
    }
    let left = n;
    while (left > 0) {
      const part = await reader.readSome(left);
      if (part === null) throw new TypeError('private relay: body ended early');
      left -= part.length;
      yield part.slice();
    }
    return;
  }
  for (;;) {
    const part = await reader.readSome();
    if (part === null) return;
    yield part.slice();
  }
}

function abortError(signal: AbortSignal): unknown {
  return signal.reason instanceof Error ? signal.reason : new DOMException('The operation was aborted.', 'AbortError');
}

const NULL_BODY = new Set([101, 103, 204, 205, 304]);

/**
 * Send req over stream and resolve with the Response once its head arrives.
 * The stream is closed when the body has been read, cancelled or aborted.
 */
export async function httpOverStream(stream: MuxStream, req: StreamRequest, url = ''): Promise<Response> {
  const signal = req.signal ?? null;
  if (signal?.aborted) {
    stream.close();
    throw abortError(signal);
  }
  const reader = new ByteReader(stream);
  let onAbort: (() => void) | null = null;
  const aborted = new Promise<never>((_, reject) => {
    if (!signal) return;
    onAbort = () => {
      stream.close();
      reject(abortError(signal));
    };
    signal.addEventListener('abort', onAbort, { once: true });
  });
  aborted.catch(() => {});
  const release = () => {
    if (onAbort) signal?.removeEventListener('abort', onAbort);
    stream.close();
  };
  let head: ResponseHead;
  try {
    stream.write(encodeRequest(req));
    head = await Promise.race([readResponseHead(reader), aborted]);
  } catch (err) {
    release();
    throw err;
  }
  if (head.status < 200 || head.status > 599) {
    release();
    throw new TypeError(`private relay: unexpected status ${head.status}`);
  }
  const headers = new Headers();
  for (const [k, v] of head.headers) {
    if (k.toLowerCase() === 'set-cookie') continue; // as the cloud relay strips it
    try {
      headers.append(k, v);
    } catch {
      // A header the platform refuses to carry.
    }
  }
  const chunks = bodyChunks(reader, head, req.method);
  let body: ReadableStream<Uint8Array> | null = null;
  if (!NULL_BODY.has(head.status) && req.method.toUpperCase() !== 'HEAD') {
    body = new ReadableStream<Uint8Array>({
      async pull(controller) {
        try {
          const next = await Promise.race([chunks.next(), aborted]);
          if (next.done) {
            release();
            controller.close();
          } else {
            controller.enqueue(next.value);
          }
        } catch (err) {
          release();
          controller.error(err);
        }
      },
      cancel() {
        release();
      },
    });
    const encoding = (headers.get('content-encoding') ?? '').trim().toLowerCase();
    if ((encoding === 'gzip' || encoding === 'deflate') && typeof DecompressionStream !== 'undefined') {
      body = body.pipeThrough(new DecompressionStream(encoding) as unknown as ReadableWritablePair<Uint8Array, Uint8Array>);
      headers.delete('content-encoding');
      headers.delete('content-length');
    }
  } else {
    release();
  }
  let resp: Response;
  try {
    resp = new Response(body, { status: head.status, statusText: head.statusText, headers });
  } catch {
    resp = new Response(body, { status: head.status, headers });
  }
  if (url) {
    try {
      Object.defineProperty(resp, 'url', { value: url });
    } catch {
      // read-only on this platform
    }
  }
  return resp;
}

/**
 * The bytes and content type a RequestInit body serialises to, so the bytes
 * sent are exactly what the caller asked for.
 */
export async function serializeBody(method: string, init: RequestInit, headers: Headers): Promise<Uint8Array | null> {
  if (init.body == null || ['GET', 'HEAD'].includes(method.toUpperCase())) return null;
  if (typeof init.body === 'string') return utf8.encode(init.body);
  if (init.body instanceof Uint8Array) return init.body;
  const req = new Request('http://local.invalid/', {
    method, body: init.body, headers, ...({ duplex: 'half' } as Record<string, unknown>),
  });
  const type = req.headers.get('content-type');
  if (type && !headers.has('content-type')) headers.set('content-type', type);
  return new Uint8Array(await req.arrayBuffer());
}
