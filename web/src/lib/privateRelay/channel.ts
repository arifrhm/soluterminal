// One private relay channel: a WebSocket to the daemon's /api/private-relay
// (through the cloud relay), the handshake, the record layer and the stream
// mux. The cloud relay sees only sealed records and their sizes.

import { clientHandshake, type HandshakeOptions, type RecordKeys } from './handshake';
import { Mux, type MuxStream } from './mux';
import { openRecord, RecordCounter, sealRecord } from './records';

export interface ChannelOptions extends HandshakeOptions {
  /** ws(s):// URL of the daemon's /api/private-relay, already signed. */
  url: string;
  /** Sec-WebSocket-Protocol offer (the cloud's sign-in bearer). */
  protocols?: string[];
  /** Defaults to the global WebSocket. */
  WebSocketImpl?: typeof WebSocket;
  /** Connect + handshake budget. */
  openTimeoutMs?: number;
  /** Keeps the daemon's 10 minute idle timer from firing on a quiet channel. */
  pingIntervalMs?: number;
}

const WS_OPEN = 1;

/** The channel's socket closed; code is the WebSocket close code. */
export class ChannelClosedError extends Error {
  constructor(readonly code: number | undefined, reason = '') {
    super(`private relay: channel closed${code !== undefined ? ` (${code}${reason ? ` ${reason}` : ''})` : ''}`);
    this.name = 'ChannelClosedError';
  }
}

function closedError(ev: CloseEvent | undefined): Error {
  return new ChannelClosedError(ev?.code, ev?.reason);
}

/** The daemon refuses a handshake with 1008 (device unknown or removed, stale, wrong target). */
export const HANDSHAKE_REFUSED = 1008;

export class PrivateChannel {
  private readonly mux: Mux;
  private sendChain: Promise<void> = Promise.resolve();
  private recvChain: Promise<void> = Promise.resolve();
  private readonly sendCounter = new RecordCounter();
  private readonly recvCounter = new RecordCounter();
  private keys!: Promise<RecordKeys>;
  private dead: Error | null = null;
  private pingTimer: ReturnType<typeof setInterval> | undefined;
  private peerAnswersPings = false;
  private lastReceivedAt = Date.now();
  /** Called once when the channel ends for any reason. */
  onclose: ((err: Error) => void) | null = null;
  readonly openedAt = Date.now();

  private constructor(private readonly ws: WebSocket) {
    this.mux = new Mux((plain) => this.sendRecord(plain));
  }

  /** Connects and completes the handshake, or rejects. */
  static open(opts: ChannelOptions): Promise<PrivateChannel> {
    const WS = opts.WebSocketImpl ?? WebSocket;
    const ws = new WS(opts.url, opts.protocols);
    ws.binaryType = 'arraybuffer';
    const ch = new PrivateChannel(ws);
    return ch.start(opts);
  }

  private start(opts: ChannelOptions): Promise<PrivateChannel> {
    const ws = this.ws;
    return new Promise<PrivateChannel>((resolve, reject) => {
      let settled = false;
      const timer = setTimeout(() => fail(new Error('private relay: channel open timed out')), opts.openTimeoutMs ?? 10_000);
      const fail = (err: Error) => {
        if (!settled) {
          settled = true;
          clearTimeout(timer);
          reject(err);
        }
        this.fail(err);
      };
      let replyWaiter: { resolve: (s: string) => void; reject: (e: Error) => void } | null = null;
      let replyText: string | null = null;
      let handshaking = true;
      ws.onerror = () => {
        // A close event follows with the detail.
      };
      ws.onclose = (ev) => {
        const err = closedError(ev);
        replyWaiter?.reject(err);
        fail(err);
      };
      ws.onmessage = (ev) => {
        if (handshaking) {
          handshaking = false;
          if (typeof ev.data !== 'string') {
            const err = new Error('private relay: bad reply');
            replyWaiter?.reject(err);
            fail(err);
            return;
          }
          replyText = ev.data;
          replyWaiter?.resolve(ev.data);
          return;
        }
        this.receiveRecord(ev.data);
      };
      ws.onopen = () => {
        this.keys = clientHandshake(
          {
            sendText: (t) => ws.send(t),
            nextText: () => replyText !== null
              ? Promise.resolve(replyText)
              : new Promise<string>((res, rej) => { replyWaiter = { resolve: res, reject: rej }; }),
          },
          opts,
        );
        this.keys.then(
          () => {
            if (settled) return;
            settled = true;
            clearTimeout(timer);
            const every = opts.pingIntervalMs ?? 30_000;
            if (every > 0) {
              // A daemon that answers pings proves it is alive at least once
              // per interval; once one has, a silence of two intervals means
              // the connection is dead (a frozen relay), so the channel ends
              // and reopens. An older daemon that never answers is not
              // timed out.
              this.mux.onPing = () => {
                this.peerAnswersPings = true;
              };
              this.pingTimer = setInterval(() => {
                if (this.peerAnswersPings && Date.now() - this.lastReceivedAt > 2 * every + 5_000) {
                  this.fail(new Error('private relay: no reply from the computer'));
                  return;
                }
                void this.mux.ping().catch(() => {});
              }, every);
              // Ping once at open, so a computer that answers is known to at
              // once and a freeze soon after the handshake is caught too.
              void this.mux.ping().catch(() => {});
            }
            resolve(this);
          },
          (err: unknown) => {
            fail(err instanceof Error ? err : new Error(String(err)));
            try {
              ws.close(1000);
            } catch {
              // already closing
            }
          },
        );
      };
    });
  }

  get isOpen(): boolean {
    return this.dead === null && this.ws.readyState === WS_OPEN;
  }

  get activeStreams(): number {
    return this.mux.activeStreams;
  }

  openStream(): MuxStream {
    if (this.dead) throw this.dead;
    return this.mux.open();
  }

  close(): void {
    this.fail(new Error('private relay: channel closed'));
  }

  // Sequence numbers are taken in call order and encryption may finish out of
  // order, so records go on the wire through a chain that keeps the order.
  private sendRecord(plain: Uint8Array): Promise<void> {
    if (this.dead) return Promise.reject(this.dead);
    let seq: bigint;
    try {
      seq = this.sendCounter.next();
    } catch (err) {
      this.fail(err as Error);
      return Promise.reject(err);
    }
    const sealed = this.keys.then((k) => sealRecord(k.c2s, seq, plain));
    sealed.catch(() => {});
    const sent = this.sendChain.then(() => sealed).then((ct) => {
      if (this.dead) throw this.dead;
      this.ws.send(ct as unknown as ArrayBuffer);
    });
    this.sendChain = sent.catch(() => {});
    sent.catch((err: Error) => this.fail(err));
    return sent;
  }

  private receiveRecord(data: unknown): void {
    this.lastReceivedAt = Date.now();
    if (this.dead) return;
    if (!(data instanceof ArrayBuffer)) {
      this.fail(new Error('private relay: unexpected message type'));
      return;
    }
    let seq: bigint;
    try {
      seq = this.recvCounter.next();
    } catch (err) {
      this.fail(err as Error);
      return;
    }
    const opened = this.keys.then((k) => openRecord(k.s2c, seq, new Uint8Array(data)));
    opened.catch(() => {});
    this.recvChain = this.recvChain
      .then(() => opened)
      .then((plain) => {
        if (!this.dead) this.mux.receive(plain);
      })
      .catch((err: Error) => this.fail(err));
  }

  private fail(err: Error): void {
    if (this.dead) return;
    this.dead = err;
    if (this.pingTimer !== undefined) clearInterval(this.pingTimer);
    this.mux.fail(err);
    try {
      this.ws.close(1000);
    } catch {
      // already closed
    }
    this.onclose?.(err);
  }
}
