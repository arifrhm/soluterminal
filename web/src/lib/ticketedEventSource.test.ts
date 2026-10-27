import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { openTicketedEventSource, withQueryParam } from './ticketedEventSource';

class EventSourceStub {
  static instances: EventSourceStub[] = [];
  readyState = 0;
  onopen: ((ev: Event) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  close = vi.fn(() => { this.readyState = 2; });
  constructor(public url: string) { EventSourceStub.instances.push(this); }
}

beforeEach(() => {
  EventSourceStub.instances = [];
  vi.stubGlobal('EventSource', EventSourceStub);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('withQueryParam', () => {
  it('appends with ? or & and keeps the fragment last', () => {
    expect(withQueryParam('/m/a/api/ui/events', 'ticket', 'rt1.x')).toBe('/m/a/api/ui/events?ticket=rt1.x');
    expect(withQueryParam('/p?x=1#h', 'ticket', 'a b')).toBe('/p?x=1&ticket=a%20b#h');
  });
});

describe('openTicketedEventSource', () => {
  it('is a plain EventSource when no ticket is needed', () => {
    const es = openTicketedEventSource('/api/ui/events', null);
    expect(es).toBe(EventSourceStub.instances[0]);
    expect(EventSourceStub.instances[0].url).toBe('/api/ui/events');
  });

  it('mints first, then connects with the ticket and forwards handlers', async () => {
    const mint = vi.fn(async () => 'rt1.ticket');
    const es = openTicketedEventSource('/m/a/api/ui/events', mint);
    expect(es.readyState).toBe(0);
    const onmessage = vi.fn();
    es.onmessage = onmessage;
    await vi.waitFor(() => expect(EventSourceStub.instances).toHaveLength(1));
    const inner = EventSourceStub.instances[0];
    expect(inner.url).toBe('/m/a/api/ui/events?ticket=rt1.ticket');
    inner.readyState = 1;
    expect(es.readyState).toBe(1);
    inner.onmessage?.(new MessageEvent('message', { data: '{}' }));
    expect(onmessage).toHaveBeenCalledTimes(1);
    es.close();
    expect(inner.close).toHaveBeenCalled();
  });

  it('reports a failed mint as a closed stream so the caller rebuilds', async () => {
    const es = openTicketedEventSource('/m/a/api/ui/events', async () => { throw new Error('401'); });
    const onerror = vi.fn();
    es.onerror = onerror;
    await vi.waitFor(() => expect(onerror).toHaveBeenCalledTimes(1));
    expect(es.readyState).toBe(2);
    expect(EventSourceStub.instances).toHaveLength(0);
  });

  it('never connects when closed before the ticket arrives', async () => {
    let resolve!: (t: string) => void;
    const es = openTicketedEventSource('/m/a/api/ui/events', () => new Promise<string>((r) => { resolve = r; }));
    es.close();
    resolve('rt1.late');
    await Promise.resolve();
    await Promise.resolve();
    expect(EventSourceStub.instances).toHaveLength(0);
    expect(es.readyState).toBe(2);
  });
});
