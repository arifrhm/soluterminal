// Test support: a Mux whose records are captured, with a fake daemon side
// that answers on its streams.

import { concat } from '../bytes';
import { decodeFrame, encodeFrame, FRAME_CLOSE, FRAME_DATA, FRAME_WINDOW, MAX_FRAME_PAYLOAD, Mux, windowPayload, type Frame } from '../mux';

export async function until(cond: () => boolean, ms = 2000): Promise<void> {
  const end = Date.now() + ms;
  while (!cond()) {
    if (Date.now() > end) throw new Error('timed out waiting');
    await new Promise((r) => setTimeout(r, 1));
  }
}

export function fakePeer() {
  const sent: Frame[] = [];
  const peer = { onWindow: null as ((id: number, n: number) => void) | null };
  const mux = new Mux(async (plain) => {
    const f = decodeFrame(plain);
    sent.push({ id: f.id, type: f.type, payload: f.payload.slice() });
    if (f.type === FRAME_WINDOW) {
      const n = new DataView(f.payload.buffer, f.payload.byteOffset, 4).getUint32(0);
      queueMicrotask(() => peer.onWindow?.(f.id, n));
    }
  });
  const dataFrom = (id: number, from = 0) =>
    concat(...sent.filter((f) => f.id === id && f.type === FRAME_DATA).map((f) => f.payload)).subarray(from);
  return Object.assign(peer, {
    mux,
    sent,
    /** Everything the client wrote on stream id. */
    dataFrom,
    text: (id: number, from = 0) => new TextDecoder().decode(dataFrom(id, from)),
    send(id: number, bytes: Uint8Array | string) {
      const b = typeof bytes === 'string' ? new TextEncoder().encode(bytes) : bytes;
      for (let at = 0; at < b.length; at += MAX_FRAME_PAYLOAD) {
        mux.receive(encodeFrame(id, FRAME_DATA, b.subarray(at, at + MAX_FRAME_PAYLOAD)));
      }
    },
    close(id: number) {
      mux.receive(encodeFrame(id, FRAME_CLOSE));
    },
    closedByClient: (id: number) => sent.some((f) => f.id === id && f.type === FRAME_CLOSE),
    /** The daemon grants n more bytes of credit on stream id. */
    window(id: number, n: number) {
      mux.receive(encodeFrame(id, FRAME_WINDOW, windowPayload(n)));
    },
  });
}
