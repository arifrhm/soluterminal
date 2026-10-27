// Stream multiplexing over the record channel (daemon privateMux). Every
// record is one frame [stream u32][type u8][payload]. The device opens
// odd-numbered streams (1, 3, 5…); the daemon only accepts them. Each stream
// is a byte pipe carrying one HTTP/1.1 connection.
//
// Frames: open (1) announces a stream before any of its data; data (2);
// close (3) ends the sender's side: everything that arrived before it is
// still delivered, then the stream reads as ended (the daemon then tears the
// whole stream down, so the HTTP and WebSocket clients close a stream only
// when they are done with it); ping (4); window (5, payload u32 big-endian)
// grants the peer that many more bytes on the stream.
//
// Flow control, as in HTTP/2: each side starts every stream with
// STREAM_WINDOW bytes of credit, sends data only within it, and the receiver
// returns credit (a window frame) once the app has taken half a window. A
// slow reader so slows only its own sender; nothing is dropped. Data beyond
// the credit granted, or a grant that lifts credit above 4 windows, is a
// protocol violation and resets that stream alone.

export const FRAME_OPEN = 1;
export const FRAME_DATA = 2;
export const FRAME_CLOSE = 3;
export const FRAME_PING = 4;
export const FRAME_WINDOW = 5;
export const MAX_FRAME_PAYLOAD = 32 << 10;
/** Each stream's credit in each direction (daemon privateStreamWindow). */
export const STREAM_WINDOW = 256 << 10;

export interface Frame {
  id: number;
  type: number;
  payload: Uint8Array;
}

export function encodeFrame(id: number, type: number, payload?: Uint8Array): Uint8Array {
  const n = payload?.length ?? 0;
  if (n > MAX_FRAME_PAYLOAD) throw new Error('private relay: frame too large');
  const out = new Uint8Array(5 + n);
  new DataView(out.buffer).setUint32(0, id >>> 0, false);
  out[4] = type;
  if (payload) out.set(payload, 5);
  return out;
}

export function decodeFrame(record: Uint8Array): Frame {
  if (record.length < 5) throw new Error('private relay: short frame');
  const id = new DataView(record.buffer, record.byteOffset, record.byteLength).getUint32(0, false);
  return { id, type: record[4], payload: record.subarray(5) };
}

export function windowPayload(increment: number): Uint8Array {
  const p = new Uint8Array(4);
  new DataView(p.buffer).setUint32(0, increment >>> 0, false);
  return p;
}

/** Sends one plaintext record, in call order; resolves once it is on the wire. */
export type RecordSink = (plain: Uint8Array) => Promise<void>;

export class StreamClosedError extends Error {
  constructor(message = 'private relay: stream closed') {
    super(message);
    this.name = 'StreamClosedError';
  }
}

/** One byte pipe inside the channel. */
export class MuxStream {
  // Received: queued for the reader, and taken by it but not yet granted back.
  private chunks: Uint8Array[] = [];
  private size = 0;
  private unacked = 0;
  // Sent: waiting for credit, and handed to the channel but not yet on the wire.
  private out: Uint8Array[] = [];
  private outBytes = 0;
  private inflight = 0;
  private credit = STREAM_WINDOW;
  private closeQueued = false;
  private eof = false;
  private error: Error | null = null;
  private waiter: (() => void) | null = null;
  private localClosed = false;
  /** Called once when the stream ends from either side or the channel fails. */
  onend: ((error: Error | null) => void) | null = null;
  private ended = false;

  constructor(readonly id: number, private readonly mux: Mux) {}

  /** Bytes written but not yet on the channel's socket (some may wait for credit). */
  get bufferedAmount(): number {
    return this.outBytes + this.inflight;
  }

  get closed(): boolean {
    return this.localClosed || this.eof || this.error !== null;
  }

  /** Next chunk from the daemon; null at the end of the stream. */
  async read(): Promise<Uint8Array | null> {
    for (;;) {
      if (this.chunks.length) {
        const c = this.chunks.shift()!;
        this.size -= c.length;
        this.delivered(c.length);
        return c;
      }
      if (this.error) throw this.error;
      if (this.eof) return null;
      await new Promise<void>((resolve) => {
        this.waiter = resolve;
      });
    }
  }

  // The reader took n bytes: return credit once half a window has been taken.
  private delivered(n: number): void {
    this.unacked += n;
    if (this.unacked < STREAM_WINDOW / 2 || this.closed) return;
    const grant = this.unacked;
    this.unacked = 0;
    void this.mux.send(this.id, FRAME_WINDOW, windowPayload(grant)).catch(() => {});
  }

  /** Queues bytes; they go out as the daemon grants credit. */
  write(bytes: Uint8Array): void {
    if (this.closed) throw new StreamClosedError();
    if (!bytes.length) return;
    this.out.push(bytes.slice());
    this.outBytes += bytes.length;
    this.flush();
  }

  private flush(): void {
    while (this.out.length && this.credit > 0 && !this.eof && !this.error) {
      const head = this.out[0];
      const n = Math.min(head.length, this.credit, MAX_FRAME_PAYLOAD);
      const part = head.subarray(0, n);
      if (n === head.length) this.out.shift();
      else this.out[0] = head.subarray(n);
      this.outBytes -= n;
      this.credit -= n;
      this.inflight += n;
      void this.mux.send(this.id, FRAME_DATA, part).then(
        () => { this.inflight -= n; },
        () => { this.inflight -= n; },
      );
    }
    if (this.closeQueued && !this.out.length) {
      this.closeQueued = false;
      if (!this.eof && !this.error) void this.mux.send(this.id, FRAME_CLOSE).catch(() => {});
      this.mux.forget(this.id, this);
    }
  }

  /**
   * Ends the stream both ways. What was written still goes out, as credit
   * allows, before the close frame; what arrives from now on is ignored.
   */
  close(): void {
    if (this.localClosed) return;
    this.localClosed = true;
    this.chunks = [];
    this.size = 0;
    this.closeQueued = true;
    this.flush();
    this.finish(null);
  }

  /** @internal false when the daemon sent beyond the credit granted. */
  pushData(payload: Uint8Array): boolean {
    if (this.localClosed || this.eof || this.error) return true;
    if (this.size + this.unacked + payload.length > STREAM_WINDOW) return false;
    this.size += payload.length;
    this.chunks.push(payload.slice());
    this.wake();
    return true;
  }

  /** @internal the daemon granted more credit; false when that is more than was ever outstanding. */
  pushWindow(increment: number): boolean {
    this.credit += increment;
    if (this.credit > 4 * STREAM_WINDOW) return false;
    this.flush();
    return true;
  }

  /** @internal the daemon closed the stream; what we had not sent is moot. */
  pushEnd(): void {
    if (this.eof || this.error) return;
    this.eof = true;
    this.dropOutgoing();
    this.wake();
    this.finish(null);
  }

  /** @internal the channel failed or the stream was reset: queued data is dropped. */
  pushError(err: Error): void {
    if (this.error) return;
    this.error = err;
    this.chunks = [];
    this.size = 0;
    this.dropOutgoing();
    this.wake();
    this.finish(err);
  }

  private dropOutgoing(): void {
    this.out = [];
    this.outBytes = 0;
    this.closeQueued = false;
  }

  private wake(): void {
    const w = this.waiter;
    this.waiter = null;
    w?.();
  }

  private finish(err: Error | null): void {
    if (this.ended) return;
    this.ended = true;
    this.onend?.(err);
  }
}

export class Mux {
  private streams = new Map<number, MuxStream>();
  private nextId = 1;
  private failed: Error | null = null;

  constructor(private readonly sink: RecordSink) {}

  get activeStreams(): number {
    return this.streams.size;
  }

  /** Opens a new stream (client ids are odd and never reused); its open frame precedes any data. */
  open(): MuxStream {
    if (this.failed) throw this.failed;
    const id = this.nextId;
    this.nextId += 2;
    if (this.nextId > 0xffffffff) throw new Error('private relay: stream ids exhausted');
    const st = new MuxStream(id, this);
    this.streams.set(id, st);
    void this.send(id, FRAME_OPEN).catch(() => {});
    return st;
  }

  /** Called when the peer sends a ping (a daemon answering ours). */
  onPing?: () => void;

  ping(): Promise<void> {
    return this.send(0, FRAME_PING);
  }

  /** @internal */
  send(id: number, type: number, payload?: Uint8Array): Promise<void> {
    if (this.failed) return Promise.reject(this.failed);
    return this.sink(encodeFrame(id, type, payload));
  }

  /** @internal */
  forget(id: number, st: MuxStream): void {
    if (this.streams.get(id) === st) this.streams.delete(id);
  }

  private reset(id: number, st: MuxStream, why: string): void {
    this.streams.delete(id);
    void this.send(id, FRAME_CLOSE).catch(() => {});
    st.pushError(new StreamClosedError(`private relay: stream reset (${why})`));
  }

  /** Handles one opened record; throws on a protocol violation that ends the channel. */
  receive(record: Uint8Array): void {
    const { id, type, payload } = decodeFrame(record);
    switch (type) {
      case FRAME_PING:
        this.onPing?.();
        return;
      case FRAME_OPEN:
        throw new Error('private relay: peer opened a stream');
      case FRAME_DATA: {
        const st = this.streams.get(id);
        if (st && !st.pushData(payload)) this.reset(id, st, 'data beyond the granted credit');
        return;
      }
      case FRAME_WINDOW: {
        if (payload.length !== 4) throw new Error('private relay: bad window frame');
        const st = this.streams.get(id);
        const increment = new DataView(payload.buffer, payload.byteOffset, 4).getUint32(0, false);
        if (st && !st.pushWindow(increment)) this.reset(id, st, 'credit beyond the window');
        return;
      }
      case FRAME_CLOSE: {
        const st = this.streams.get(id);
        this.streams.delete(id);
        st?.pushEnd();
        return;
      }
      default:
        throw new Error(`private relay: unknown frame type ${type}`);
    }
  }

  /** The channel ended: every stream errors. */
  fail(err: Error): void {
    if (this.failed) return;
    this.failed = err;
    const all = [...this.streams.values()];
    this.streams.clear();
    for (const st of all) st.pushError(err);
  }
}
