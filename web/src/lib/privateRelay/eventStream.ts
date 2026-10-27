// An event stream (text/event-stream) read through the private relay HTTP
// client, exposing the EventStream slice of EventSource the UI uses
// (lib/ticketedEventSource.ts). It does not reconnect by itself: when the
// stream ends it reports CLOSED through onerror, and the caller's rebuild
// loop opens a new one, as it does when an EventSource gives up.

import type { EventStream } from '../ticketedEventSource';

const CONNECTING = 0;
const OPEN = 1;
const CLOSED = 2;

export interface SseEvent {
  type: string;
  data: string;
  lastEventId: string;
}

/** Incremental text/event-stream parser (WHATWG HTML §9.2.6). */
export class SseParser {
  private decoder = new TextDecoder();
  private line = '';
  private skipLF = false;
  private data: string[] = [];
  private type = '';
  private lastId = '';
  private bom = true;

  push(chunk: Uint8Array, final = false): SseEvent[] {
    let text = this.decoder.decode(chunk, { stream: !final });
    if (this.bom && text.length) {
      if (text.charCodeAt(0) === 0xfeff) text = text.slice(1);
      this.bom = false;
    }
    const out: SseEvent[] = [];
    for (let i = 0; i < text.length; i++) {
      const c = text[i];
      if (this.skipLF) {
        this.skipLF = false;
        if (c === '\n') continue;
      }
      if (c === '\r' || c === '\n') {
        if (c === '\r') this.skipLF = true;
        this.processLine(this.line, out);
        this.line = '';
      } else {
        this.line += c;
      }
    }
    return out;
  }

  private processLine(line: string, out: SseEvent[]): void {
    if (line === '') {
      if (this.data.length) {
        out.push({ type: this.type || 'message', data: this.data.join('\n'), lastEventId: this.lastId });
      }
      this.data = [];
      this.type = '';
      return;
    }
    if (line[0] === ':') return;
    const colon = line.indexOf(':');
    const field = colon < 0 ? line : line.slice(0, colon);
    let value = colon < 0 ? '' : line.slice(colon + 1);
    if (value[0] === ' ') value = value.slice(1);
    if (field === 'data') this.data.push(value);
    else if (field === 'event') this.type = value;
    else if (field === 'id' && !value.includes('\0')) this.lastId = value;
  }
}

export type EventStreamSource =
  | { response: (signal: AbortSignal) => Promise<Response> }
  | { fallback: EventStream };

function messageEvent(e: SseEvent): MessageEvent {
  if (typeof MessageEvent === 'function') return new MessageEvent('message', { data: e.data, lastEventId: e.lastEventId });
  return Object.assign(new Event('message'), { data: e.data, lastEventId: e.lastEventId }) as MessageEvent;
}

export class ChannelEventStream implements EventStream {
  onopen: EventSource['onopen'] = null;
  onerror: EventSource['onerror'] = null;
  onmessage: EventSource['onmessage'] = null;
  private state = CONNECTING;
  private inner: EventStream | null = null;
  private abort = new AbortController();

  constructor(source: Promise<EventStreamSource>) {
    source.then(
      (s) => {
        if ('fallback' in s) this.adopt(s.fallback);
        else void this.run(s.response);
      },
      () => this.fail(),
    );
  }

  get readyState(): number {
    return this.inner ? this.inner.readyState : this.state;
  }

  close(): void {
    this.state = CLOSED;
    this.abort.abort();
    this.inner?.close();
  }

  private self(): EventSource {
    return this as unknown as EventSource;
  }

  private adopt(es: EventStream): void {
    if (this.state === CLOSED) {
      es.close();
      return;
    }
    this.inner = es;
    es.onopen = (ev) => this.onopen?.call(this.self(), ev);
    es.onerror = (ev) => this.onerror?.call(this.self(), ev);
    es.onmessage = (ev) => this.onmessage?.call(this.self(), ev);
  }

  private fail(): void {
    if (this.state === CLOSED) return;
    this.state = CLOSED;
    this.abort.abort();
    this.onerror?.call(this.self(), new Event('error'));
  }

  private async run(open: (signal: AbortSignal) => Promise<Response>): Promise<void> {
    if (this.state === CLOSED) return;
    let resp: Response;
    try {
      resp = await open(this.abort.signal);
    } catch {
      this.fail();
      return;
    }
    if (this.state === CLOSED) {
      void resp.body?.cancel().catch(() => {});
      return;
    }
    const type = resp.headers.get('content-type') ?? '';
    if (resp.status !== 200 || !/^text\/event-stream/i.test(type) || !resp.body) {
      void resp.body?.cancel().catch(() => {});
      this.fail();
      return;
    }
    this.state = OPEN;
    this.onopen?.call(this.self(), new Event('open'));
    const parser = new SseParser();
    const reader = resp.body.getReader();
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (this.state === CLOSED) break;
        for (const e of parser.push(value ?? new Uint8Array(), done)) {
          if (this.state === CLOSED) break;
          if (e.type === 'message') this.onmessage?.call(this.self(), messageEvent(e));
        }
        if (done) break;
      }
    } catch {
      // Treated as the end of the stream.
    }
    void reader.cancel().catch(() => {});
    this.fail();
  }
}
