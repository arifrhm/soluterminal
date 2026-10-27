// @vitest-environment node
import { describe, expect, it } from 'vitest';
import { ChannelEventStream, SseParser } from './eventStream';
import { until } from './testdata/fakePeer';

const enc = new TextEncoder();

describe('text/event-stream parser', () => {
  it('handles CRLF split across chunks, multi-line data, comments and names', () => {
    const p = new SseParser();
    const out = [
      ...p.push(enc.encode('﻿: comment\r')),
      ...p.push(enc.encode('\ndata: one\r\ndata:two\r')),
      ...p.push(enc.encode('\n\r\nevent: custom\nid: 7\ndata: x\n\ndata')),
      ...p.push(enc.encode('\n\n')),
    ];
    expect(out).toEqual([
      { type: 'message', data: 'one\ntwo', lastEventId: '' },
      { type: 'custom', data: 'x', lastEventId: '7' },
      { type: 'message', data: '', lastEventId: '7' },
    ]);
  });

  it('decodes UTF-8 split inside a character', () => {
    const p = new SseParser();
    const bytes = enc.encode('data: é\n\n');
    expect([...p.push(bytes.subarray(0, 7)), ...p.push(bytes.subarray(7))]).toEqual([{ type: 'message', data: 'é', lastEventId: '' }]);
  });
});

function sseResponse(chunks: string[]): Response {
  let i = 0;
  const body = new ReadableStream<Uint8Array>({
    pull(c) {
      if (i < chunks.length) c.enqueue(enc.encode(chunks[i++]));
      else c.close();
    },
  });
  return new Response(body, { headers: { 'content-type': 'text/event-stream' } });
}

describe('ChannelEventStream', () => {
  it('opens, delivers message events and reports CLOSED when the stream ends', async () => {
    const es = new ChannelEventStream(Promise.resolve({ response: async () => sseResponse(['data: {"type":"a"}\n\n', 'event: other\ndata: x\n\n', 'data: b\n\n']) }));
    const seen: string[] = [];
    es.onopen = () => seen.push(`open:${es.readyState}`);
    es.onmessage = (ev) => seen.push(`msg:${ev.data}`);
    es.onerror = () => seen.push(`error:${es.readyState}`);
    await until(() => seen.some((s) => s.startsWith('error')));
    expect(seen).toEqual(['open:1', 'msg:{"type":"a"}', 'msg:b', 'error:2']);
  });

  it('fails without opening on a non-event-stream response', async () => {
    const es = new ChannelEventStream(Promise.resolve({ response: async () => new Response('nope', { status: 403 }) }));
    const seen: string[] = [];
    es.onopen = () => seen.push('open');
    es.onerror = () => seen.push('error');
    await until(() => seen.length > 0);
    expect(seen).toEqual(['error']);
    expect(es.readyState).toBe(2);
  });

  it('aborts the request on close', async () => {
    let signal: AbortSignal | null = null;
    const es = new ChannelEventStream(Promise.resolve({
      response: (s: AbortSignal) => {
        signal = s;
        return new Promise<Response>(() => {});
      },
    }));
    await until(() => signal !== null);
    es.close();
    expect(signal!.aborted).toBe(true);
    expect(es.readyState).toBe(2);
  });

  it('forwards to the ordinary event stream when given one', async () => {
    const inner = { readyState: 1, close() { inner.readyState = 2; }, onopen: null, onerror: null, onmessage: null } as unknown as { -readonly [K in keyof EventSource]: EventSource[K] };
    const es = new ChannelEventStream(Promise.resolve({ fallback: inner }));
    const got: string[] = [];
    es.onmessage = (ev) => got.push(ev.data);
    await until(() => inner.onmessage !== null);
    inner.onmessage!.call(inner, { data: 'x' } as MessageEvent);
    expect(got).toEqual(['x']);
    es.close();
    expect(es.readyState).toBe(2);
  });
});
