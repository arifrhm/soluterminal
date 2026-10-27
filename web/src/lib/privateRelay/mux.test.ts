// @vitest-environment node
import { describe, expect, it } from 'vitest';
import { hex } from './bytes';
import { decodeFrame, encodeFrame, FRAME_CLOSE, FRAME_DATA, FRAME_OPEN, FRAME_PING, FRAME_WINDOW, MAX_FRAME_PAYLOAD, STREAM_WINDOW } from './mux';
import { fakePeer, until } from './testdata/fakePeer';

describe('private relay mux', () => {
  it('encodes frames as [stream u32][type u8][payload]', () => {
    expect(hex(encodeFrame(0x01020304, FRAME_DATA, new Uint8Array([9, 8])))).toBe('01020304020908');
    expect(hex(encodeFrame(1, FRAME_OPEN))).toBe('0000000101');
    const f = decodeFrame(new Uint8Array([0, 0, 0, 5, 3]));
    expect(f).toMatchObject({ id: 5, type: FRAME_CLOSE });
    expect(f.payload).toHaveLength(0);
    expect(() => decodeFrame(new Uint8Array([0, 0, 0]))).toThrow(/short/);
    expect(() => encodeFrame(1, FRAME_DATA, new Uint8Array(MAX_FRAME_PAYLOAD + 1))).toThrow(/too large/);
  });

  it('opens odd stream ids from 1 and announces each', () => {
    const p = fakePeer();
    expect([p.mux.open().id, p.mux.open().id, p.mux.open().id]).toEqual([1, 3, 5]);
    expect(p.sent.map((f) => [f.id, f.type])).toEqual([[1, FRAME_OPEN], [3, FRAME_OPEN], [5, FRAME_OPEN]]);
  });

  it('splits writes into frames of at most 32 KiB and keeps their order', () => {
    const p = fakePeer();
    const st = p.mux.open();
    const big = new Uint8Array(MAX_FRAME_PAYLOAD * 2 + 10).map((_, i) => i & 0xff);
    st.write(big);
    const data = p.sent.filter((f) => f.type === FRAME_DATA);
    expect(data.map((f) => f.payload.length)).toEqual([MAX_FRAME_PAYLOAD, MAX_FRAME_PAYLOAD, 10]);
    expect(p.dataFrom(st.id)).toEqual(big);
  });

  it('delivers data per stream and ends a stream on the daemon’s close', async () => {
    const p = fakePeer();
    const a = p.mux.open();
    const b = p.mux.open();
    p.send(b.id, 'to b');
    p.send(a.id, 'to a');
    p.close(a.id);
    expect(new TextDecoder().decode((await a.read())!)).toBe('to a');
    expect(await a.read()).toBeNull();
    expect(new TextDecoder().decode((await b.read())!)).toBe('to b');
    expect(a.closed).toBe(true);
    expect(() => a.write(new Uint8Array(1))).toThrow();
    // Data for a closed or unknown stream is dropped, not an error.
    p.send(a.id, 'late');
    p.send(99, 'stray');
  });

  it('sends a close frame once when the client ends a stream', () => {
    const p = fakePeer();
    const st = p.mux.open();
    st.close();
    st.close();
    expect(p.sent.filter((f) => f.type === FRAME_CLOSE).map((f) => f.id)).toEqual([st.id]);
  });

  it('pings on stream 0 and ignores the daemon’s pings', async () => {
    const p = fakePeer();
    await p.mux.ping();
    expect(p.sent).toEqual([{ id: 0, type: FRAME_PING, payload: new Uint8Array() }]);
    p.mux.receive(encodeFrame(0, FRAME_PING));
  });

  it('treats a daemon-opened stream or an unknown frame as fatal', () => {
    const p = fakePeer();
    expect(() => p.mux.receive(encodeFrame(2, FRAME_OPEN))).toThrow(/peer opened/);
    expect(() => p.mux.receive(encodeFrame(1, 9))).toThrow(/unknown frame/);
  });

  // As daemon TestPrivateRelayDeliversDataQueuedBeforeClose: a close is the
  // end of the sender's data, never a reason to drop what came before it.
  it('delivers data queued before the daemon’s close', async () => {
    const p = fakePeer();
    const st = p.mux.open();
    const big = new Uint8Array(3 * MAX_FRAME_PAYLOAD + 7).map((_, i) => (i * 7) & 0xff);
    p.send(st.id, big);
    p.send(st.id, 'tail');
    p.close(st.id);
    const got: Uint8Array[] = [];
    for (let c = await st.read(); c !== null; c = await st.read()) got.push(c);
    const all = new Uint8Array(got.reduce((n, c) => n + c.length, 0));
    let at = 0;
    for (const c of got) { all.set(c, at); at += c.length; }
    expect(all.subarray(0, big.length)).toEqual(big);
    expect(new TextDecoder().decode(all.subarray(big.length))).toBe('tail');
  });

  it('sends only within the daemon’s credit and resumes on a window frame', () => {
    const p = fakePeer();
    const st = p.mux.open();
    st.write(new Uint8Array(STREAM_WINDOW + 100_000));
    const sent = () => p.dataFrom(st.id).length;
    expect(sent()).toBe(STREAM_WINDOW);
    expect(st.bufferedAmount).toBeGreaterThanOrEqual(100_000);
    p.window(st.id, 60_000);
    expect(sent()).toBe(STREAM_WINDOW + 60_000);
    p.window(st.id, 60_000);
    expect(sent()).toBe(STREAM_WINDOW + 100_000);
    // The open frame went out first.
    expect(p.sent[0]).toMatchObject({ id: st.id, type: FRAME_OPEN });
  });

  it('sends its close only after what it wrote, as credit allows', () => {
    const p = fakePeer();
    const st = p.mux.open();
    st.write(new Uint8Array(STREAM_WINDOW + 10));
    st.close();
    expect(p.closedByClient(st.id)).toBe(false);
    p.window(st.id, 10);
    expect(p.sent.at(-1)).toMatchObject({ id: st.id, type: FRAME_CLOSE });
    expect(p.dataFrom(st.id).length).toBe(STREAM_WINDOW + 10);
  });

  // As daemon TestPrivateRelayDeliversDataQueuedBeforeClose under load: a
  // reader far slower than its sender still gets every byte, because the
  // sender waits for credit instead of the stream being reset.
  it('returns credit as the reader takes data, so a slow reader gets everything', async () => {
    const p = fakePeer();
    const st = p.mux.open();
    const total = 3 << 20;
    const body = new Uint8Array(total).map((_, i) => (i * 31) & 0xff);
    // A daemon that honours credit: it sends while it has some, then waits.
    let credit = STREAM_WINDOW;
    let at = 0;
    const pump = () => {
      while (at < total && credit > 0) {
        const n = Math.min(MAX_FRAME_PAYLOAD, credit, total - at);
        p.send(st.id, body.subarray(at, at + n));
        at += n;
        credit -= n;
      }
      if (at === total) p.close(st.id);
    };
    p.onWindow = (id, n) => {
      if (id === st.id) {
        credit += n;
        pump();
      }
    };
    pump();
    expect(at).toBe(STREAM_WINDOW); // nothing beyond the first window until the reader reads
    const got = new Uint8Array(total);
    let n = 0;
    for (let c = await st.read(); c !== null; c = await st.read()) {
      got.set(c, n);
      n += c.length;
      if (n % (256 << 10) === 0) await new Promise((r) => setTimeout(r, 1)); // a slow reader
    }
    expect(n).toBe(total);
    expect(got.findIndex((b, i) => b !== body[i])).toBe(-1); // (toEqual on 3 MB is slow)
    const grants = p.sent.filter((f) => f.type === FRAME_WINDOW).map((f) => new DataView(f.payload.buffer).getUint32(0));
    expect(grants.every((g) => g >= STREAM_WINDOW / 2)).toBe(true);
  });

  it('resets a stream whose daemon sends beyond the credit granted', async () => {
    const p = fakePeer();
    const st = p.mux.open();
    const other = p.mux.open();
    p.send(st.id, new Uint8Array(STREAM_WINDOW));
    p.send(st.id, new Uint8Array(1));
    expect(p.sent.filter((f) => f.type === FRAME_CLOSE).map((f) => f.id)).toEqual([st.id]);
    await expect((async () => { for (;;) if ((await st.read()) === null) return; })()).rejects.toThrow(/credit/);
    p.send(other.id, 'unaffected');
    expect(new TextDecoder().decode((await other.read())!)).toBe('unaffected');
  });

  it('resets a stream on a grant beyond four windows, ignores grants for unknown streams, and rejects a malformed one', async () => {
    const p = fakePeer();
    const st = p.mux.open();
    p.window(99, 1000);
    p.window(st.id, 3 * STREAM_WINDOW + 1);
    expect(p.closedByClient(st.id)).toBe(true);
    await expect(st.read()).rejects.toThrow(/beyond the window/);
    expect(() => p.mux.receive(encodeFrame(1, FRAME_WINDOW, new Uint8Array(3)))).toThrow(/bad window/);
  });

  // As daemon TestPrivateRelayStalledStreamDoesNotFreezeTheChannel: a stream
  // whose reader stalls only stops its own sender.
  it('lets other streams flow while one waits for credit', async () => {
    const p = fakePeer();
    const stalled = p.mux.open();
    const live = p.mux.open();
    stalled.write(new Uint8Array(2 * STREAM_WINDOW)); // the daemon never reads it
    live.write(new TextEncoder().encode('hello'));
    expect(p.text(live.id)).toBe('hello');
    p.send(live.id, 'answer');
    expect(new TextDecoder().decode((await live.read())!)).toBe('answer');
    expect(p.dataFrom(stalled.id).length).toBe(STREAM_WINDOW);
    expect(p.closedByClient(stalled.id)).toBe(false);
  });

  it('errors every stream when the channel fails', async () => {
    const p = fakePeer();
    const st = p.mux.open();
    let ended: Error | null | undefined;
    st.onend = (e) => { ended = e; };
    const pending = st.read();
    p.mux.fail(new Error('channel gone'));
    await expect(pending).rejects.toThrow('channel gone');
    await until(() => ended !== undefined);
    expect(ended?.message).toBe('channel gone');
    expect(() => p.mux.open()).toThrow('channel gone');
  });
});
