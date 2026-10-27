// An EventSource whose URL carries a relay ticket minted just before it
// connects, instead of the sign-in token (cloud/relay_ticket.go). It exposes
// the slice of EventSource the UI uses, so callers keep their handler code.
//
// Reconnects: EventSource auto-reconnects with the same URL, so the spent
// ticket is resent. The cloud then falls back to the session cookie the first
// redemption banked. Once that also fails (the access token expired) the
// stream closes and the caller's rebuild loop mints a fresh ticket.

export type EventStream = Pick<EventSource, 'readyState' | 'close' | 'onopen' | 'onerror' | 'onmessage'>;

// EventSource.CONNECTING / CLOSED. Literal so jsdom (no EventSource) works.
const CONNECTING = 0;
const CLOSED = 2;

export function withQueryParam(url: string, name: string, value: string): string {
  const hashAt = url.indexOf('#');
  const base = hashAt >= 0 ? url.slice(0, hashAt) : url;
  const hash = hashAt >= 0 ? url.slice(hashAt) : '';
  const sep = base.includes('?') ? '&' : '?';
  return `${base}${sep}${encodeURIComponent(name)}=${encodeURIComponent(value)}${hash}`;
}

class TicketedEventSource implements EventStream {
  onopen: EventSource['onopen'] = null;
  onerror: EventSource['onerror'] = null;
  onmessage: EventSource['onmessage'] = null;
  private inner: EventSource | null = null;
  private closed = false;
  private failed = false;

  constructor(url: string, mint: () => Promise<string>, prepare?: (url: string) => Promise<string>) {
    Promise.all([mint(), prepare ? prepare(url) : Promise.resolve(url)]).then(
      ([ticket, preparedUrl]) => {
        if (this.closed) return;
        const es = new EventSource(withQueryParam(preparedUrl, 'ticket', ticket));
        es.onopen = (ev) => this.onopen?.call(es, ev);
        es.onerror = (ev) => this.onerror?.call(es, ev);
        es.onmessage = (ev) => this.onmessage?.call(es, ev);
        this.inner = es;
      },
      () => {
        if (this.closed) return;
        // Report a closed stream so the caller's rebuild loop retries.
        this.failed = true;
        this.onerror?.call(this as unknown as EventSource, new Event('error'));
      },
    );
  }

  get readyState(): number {
    if (this.inner) return this.inner.readyState;
    return this.closed || this.failed ? CLOSED : CONNECTING;
  }

  close(): void {
    this.closed = true;
    this.inner?.close();
  }
}

/** Open `url` as an event stream. With `mint`, a ticket is minted first and
 *  rides the URL; without it (local daemon) this is a plain EventSource.
 *  `prepare` (approved devices) rewrites the URL first, e.g. to sign it. */
export function openTicketedEventSource(
  url: string,
  mint: (() => Promise<string>) | null,
  prepare?: (url: string) => Promise<string>,
): EventStream {
  if (!mint) return new EventSource(url);
  return new TicketedEventSource(url, mint, prepare);
}
