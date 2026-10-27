import { isPreviewAcceptanceHost } from './previewAcceptanceShell';

// Mounted workflow previews are static, relative-base builds with no daemon
// behind them. Keep their browser boot deterministic without changing the
// origin-root production, cloud, dev-server, or Tauri bundles.
export const isPreviewBuild = import.meta.env.BASE_URL !== '/';

export function mountedAssetURL(asset: string, base = import.meta.env.BASE_URL): string {
  return `${base.endsWith('/') ? base : `${base}/`}${asset.replace(/^\/+/, '')}`;
}

const previewBundleVersion = typeof __SOLU_WEB_VERSION__ === 'string' ? __SOLU_WEB_VERSION__ : 'dev';

const previewFixtures = new Map<string, unknown>([
  ...Object.entries(typeof __SOLU_PREVIEW_FIXTURES__ === 'undefined' ? {} : __SOLU_PREVIEW_FIXTURES__ ?? {}),
  ['/api/panes', []],
  ['/api/workspaces', []],
  ['/api/ports', []],
  ['/api/health', { status: 'ok', version: previewBundleVersion, webVersion: previewBundleVersion, uptimeSec: 0, panes: 0 }],
  ['/webdist.json', { webVersion: previewBundleVersion }],
]);

export function previewFixtureResponse(pathname: string): Response | null {
  if (!previewFixtures.has(pathname)) return null;
  return new Response(JSON.stringify(previewFixtures.get(pathname)), {
    status: 200,
    headers: { 'content-type': 'application/json' },
  });
}

function previewUnavailableResponse(): Response {
  return new Response(JSON.stringify({ error: 'preview backend unavailable' }), {
    status: 404,
    headers: { 'content-type': 'application/json' },
  });
}

// previewLiveResponseIsUsable distinguishes a real backend answer from the two
// static-boundary stand-ins a mounted preview can produce for an escaping API
// call: the honest "preview backend unavailable" JSON 404 the Go static server
// writes when no upstream is mounted, and a single-page-fallback HTML document
// a non-Go static server may return for any unknown path. Anything else — a
// real JSON payload, a genuine daemon 404, an SSE stream — is passed through.
async function previewLiveResponseIsUsable(res: Response, pathname: string): Promise<boolean> {
  if (res.status === 404) {
    const body = await res.clone().text().catch(() => '');
    return !body.includes('preview backend unavailable');
  }
  const contentType = (res.headers.get('content-type') || '').toLowerCase();
  if (pathname.startsWith('/api') && contentType.includes('text/html')) return false;
  return true;
}

// createPreviewFetch re-anchors origin-root API calls under the mounted preview
// document. By default it prefers a live mounted daemon and falls back to the
// deterministic fixture/stand-in. With `offline` set it never touches the
// network: every escaping API call is answered from the fixture map or a
// JSON-parseable synthetic 404. The daemon's browser-acceptance exposure has no
// reachable daemon behind /api/* (the origin gate rejects the exposed host), so
// it uses isolated capture fixtures with the offline shim. The real UI renders;
// writes and unknown reads fail explicitly rather than reaching a live daemon.
export function createPreviewFetch(
  nativeFetch: typeof fetch,
  documentURL: string,
  options: { offline?: boolean; onMissingRead?: (pathname: string) => void } = {},
): typeof fetch {
  const documentBase = new URL('.', documentURL);
  return async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const raw = typeof Request !== 'undefined' && input instanceof Request ? input.url : String(input);
    let target: URL;
    try {
      target = new URL(raw, documentURL);
    } catch {
      if (options.offline) return previewUnavailableResponse();
      return nativeFetch(input, init);
    }
    if (options.offline) {
      // Handle both origin-root and already-mounted API URLs. No mutation,
      // external fetch, or unknown API may fall through to a live daemon.
      const method = (init?.method ?? (typeof Request !== 'undefined' && input instanceof Request ? input.method : 'GET')).toUpperCase();
      if (target.origin !== documentBase.origin || !['GET', 'HEAD'].includes(method)) {
        return new Response(JSON.stringify({ error: 'Isolated preview: changes and external requests are disabled.' }), {
          status: 403, headers: { 'content-type': 'application/json' },
        });
      }
      const path = target.pathname.startsWith(documentBase.pathname)
        ? '/' + target.pathname.slice(documentBase.pathname.length) : target.pathname;
      if (/(^|\/)api(?:\/|$)/.test(path) || path === '/webdist.json') {
        const fixture = previewFixtureResponse(path);
        if (fixture) return fixture;
        options.onMissingRead?.(path);
        return previewUnavailableResponse();
      }
    }
    // The shell bundle's provenance is local to this preview document, never
    // the mounted upstream daemon. Serve it from the fixture directly so a
    // mounted acceptance probe never records a doomed /webdist.json 404.
    if (target.pathname === '/webdist.json') {
      const provenance = previewFixtureResponse(target.pathname);
      if (provenance) return provenance;
    }
    const escapesMount = target.origin === documentBase.origin
      && !target.pathname.startsWith(documentBase.pathname);
    if (!escapesMount) return nativeFetch(input, init);

    // A mounted preview app emits origin-root API calls (`/api/...`) because it
    // never learns its mount prefix. Re-anchor them under the document base so
    // the exposure/proxy chain can reach the daemon mounted behind the preview
    // instead of the static bundle's origin root.
    const mounted = new URL(target.toString());
    mounted.pathname = documentBase.pathname + target.pathname.replace(/^\/+/, '');

    // When a daemon is mounted behind the preview, escaping API calls must reach
    // it instead of a fixture; the shim only supplies deterministic stand-ins
    // for a genuinely backend-less static preview. Prefer the live answer and
    // fall back to the fixture/stand-in when the static boundary answered.
    if (!options.offline) {
      try {
        const live = await nativeFetch(mounted.toString(), init);
        if (await previewLiveResponseIsUsable(live, target.pathname)) return live;
      } catch {
        // Network-unreachable backend: fall through to the deterministic fixture.
      }
    }

    const fixture = previewFixtureResponse(target.pathname);
    if (fixture) return fixture;
    // Keep missing boot endpoints network-silent and JSON-parseable. API
    // helpers still see an honest 404, while a newly added poll cannot turn a
    // null response body into an uncaught SyntaxError during acceptance.
    return previewUnavailableResponse();
  };
}

if (isPreviewBuild && typeof window !== 'undefined') {
  document.documentElement.setAttribute('data-solu-preview-build', '1');
  try {
    // Acceptance screenshots should show the app shell, not first-run modals.
    localStorage.setItem('solu.seenWelcome', '1');
    localStorage.setItem('solu.seenChecklist', '1');
  } catch { /* storage may be unavailable; rendering still proceeds */ }
  const missingReads = new Set<string>();
  window.fetch = createPreviewFetch(window.fetch.bind(window), window.location.href, {
    offline: isPreviewAcceptanceHost(window.location.hostname),
    onMissingRead(path) {
      // Bounded diagnostics only: never retain query parameters or payloads.
      if (missingReads.size < 32) missingReads.add(path);
      document.documentElement.setAttribute('data-solu-preview-missing-reads', JSON.stringify([...missingReads]));
    },
  });

  class PreviewWebSocket extends EventTarget {
    static readonly CONNECTING = 0;
    static readonly OPEN = 1;
    static readonly CLOSING = 2;
    static readonly CLOSED = 3;
    readonly CONNECTING = 0;
    readonly OPEN = 1;
    readonly CLOSING = 2;
    readonly CLOSED = 3;
    readonly url: string;
    readonly protocol = '';
    readonly extensions = '';
    readonly bufferedAmount = 0;
    readonly binaryType: BinaryType = 'blob';
    readyState = PreviewWebSocket.CONNECTING;
    onopen: ((this: WebSocket, ev: Event) => unknown) | null = null;
    onmessage: ((this: WebSocket, ev: MessageEvent) => unknown) | null = null;
    onerror: ((this: WebSocket, ev: Event) => unknown) | null = null;
    onclose: ((this: WebSocket, ev: CloseEvent) => unknown) | null = null;

    constructor(url: string | URL) {
      super();
      this.url = String(url);
    }

    close(): void {
      this.readyState = PreviewWebSocket.CLOSED;
    }

    send(): void { /* no backend exists in a static preview */ }
  }

  class PreviewEventSource extends EventTarget {
    static readonly CONNECTING = 0;
    static readonly OPEN = 1;
    static readonly CLOSED = 2;
    readonly CONNECTING = 0;
    readonly OPEN = 1;
    readonly CLOSED = 2;
    readonly url: string;
    readonly withCredentials = false;
    readyState = PreviewEventSource.CONNECTING;
    onopen: ((this: EventSource, ev: Event) => unknown) | null = null;
    onmessage: ((this: EventSource, ev: MessageEvent) => unknown) | null = null;
    onerror: ((this: EventSource, ev: Event) => unknown) | null = null;

    constructor(url: string | URL) {
      super();
      this.url = String(url);
    }

    close(): void {
      this.readyState = PreviewEventSource.CLOSED;
    }
  }

  window.WebSocket = PreviewWebSocket as unknown as typeof WebSocket;
  window.EventSource = PreviewEventSource as unknown as typeof EventSource;
}
