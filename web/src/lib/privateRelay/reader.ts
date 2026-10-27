// Buffered reads over a stream of chunks, for the HTTP and WebSocket parsers.

import { concat } from './bytes';

export interface ChunkSource {
  /** Next chunk; null at the end. */
  read(): Promise<Uint8Array | null>;
}

const EMPTY = new Uint8Array(0);

export class ByteReader {
  private buf: Uint8Array = EMPTY;
  private done = false;

  constructor(private readonly src: ChunkSource) {}

  /** Bytes already received and not yet consumed. */
  get buffered(): number {
    return this.buf.length;
  }

  /** Reads one more chunk into the buffer; false at the end of the stream. */
  private async fill(): Promise<boolean> {
    if (this.done) return false;
    const chunk = await this.src.read();
    if (chunk === null) {
      this.done = true;
      return false;
    }
    this.buf = this.buf.length ? concat(this.buf, chunk) : chunk;
    return true;
  }

  /**
   * One CRLF- (or LF-) terminated line without its terminator, decoded as
   * Latin-1 (HTTP header bytes). null when the stream ends before any byte.
   */
  async readLine(max = 64 << 10): Promise<string | null> {
    let scanned = 0;
    for (;;) {
      const at = this.buf.indexOf(0x0a, scanned);
      if (at >= 0) {
        const end = at > 0 && this.buf[at - 1] === 0x0d ? at - 1 : at;
        const line = this.buf.subarray(0, end);
        this.buf = this.buf.subarray(at + 1);
        let s = '';
        for (let i = 0; i < line.length; i++) s += String.fromCharCode(line[i]);
        return s;
      }
      scanned = this.buf.length;
      if (scanned > max) throw new Error('private relay: line too long');
      if (!(await this.fill())) {
        if (this.buf.length === 0) return null;
        throw new Error('private relay: stream ended mid-line');
      }
    }
  }

  async readExact(n: number): Promise<Uint8Array> {
    while (this.buf.length < n) {
      if (!(await this.fill())) throw new Error('private relay: stream ended early');
    }
    const out = this.buf.subarray(0, n);
    this.buf = this.buf.subarray(n);
    return out;
  }

  /** Whatever is buffered, else the next chunk, up to max bytes; null at the end. */
  async readSome(max = Infinity): Promise<Uint8Array | null> {
    if (this.buf.length === 0 && !(await this.fill())) return null;
    const n = Math.min(this.buf.length, max);
    const out = this.buf.subarray(0, n);
    this.buf = this.buf.subarray(n);
    return out;
  }
}
