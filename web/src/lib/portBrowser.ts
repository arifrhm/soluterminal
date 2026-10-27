import {
  currentApiPrefix,
  currentPrefix,
  currentRoutingScope,
  mintRelayTicket,
  relayNeedsTicket,
} from '../api';
import { isDesktopRouterMode } from '../dualBackend';
import type { BrowserServiceTarget, Port } from '../types';
import { portUrl } from './ports';
import { withQueryParam } from './ticketedEventSource';

// The desktop router serves previews on an origin of its own
// (/router/preview-origin), so a pane's page neither runs as the app nor needs
// the sandbox that would strip its scripts and data. Null until known, and on
// a router too old to have one (previews then stay on the app's origin).
let routerPreviewOrigin: string | null = null;

/**
 * What /router/preview-origin said: 'ready' (previews have their own origin),
 * 'old-router' (a router from before 0.0.1224, still running after an in-app
 * update until the app restarts: its app shell answers with the page), or
 * 'unknown' (not router mode, router unreachable, or no preview port).
 */
export type RouterPreviewStatus = 'ready' | 'old-router' | 'unknown';

export async function loadRouterPreviewOrigin(): Promise<RouterPreviewStatus> {
  if (!isDesktopRouterMode()) return 'unknown';
  let r: Response;
  try {
    r = await fetch('/router/preview-origin', { cache: 'no-store' });
  } catch {
    return 'unknown'; // router down: not a restart problem
  }
  if (!r.ok) return 'unknown';
  if (!(r.headers.get('content-type') ?? '').includes('application/json')) return 'old-router';
  try {
    const origin = ((await r.json()) as { origin?: unknown }).origin;
    if (typeof origin === 'string' && /^http:\/\/(127\.0\.0\.1|localhost|\[::1\]):\d+$/.test(origin)) {
      routerPreviewOrigin = origin;
      return 'ready';
    }
  } catch {
    // unreadable answer
  }
  return 'unknown';
}

/** Shown while a router older than this web app keeps running after an update. */
export const ROUTER_RESTART_NOTICE =
  'Restart SoluTerminal to finish this update. Right-click the SoluTerminal icon in the taskbar tray (it may be under '
  + 'the ^ arrow), choose Quit, then open SoluTerminal again. Your agents keep running, and their panes come back when '
  + 'you reopen it.';

/** Test hook. */
export function setRouterPreviewOrigin(origin: string | null): void {
  routerPreviewOrigin = origin;
}

export function serviceTarget(p: Port): BrowserServiceTarget {
  return {
    url: portUrl(p),
    previewPath: p.paneId ? p.previewPath : undefined,
    backend: p.backend,
    machinePrefix: currentPrefix(),
    paneId: p.paneId,
    port: p.port,
    addr: p.addr,
    proto: p.proto,
  };
}

// Keep credentials in the proxy URL only. Callers retain the friendly URL in
// workspace data and build a fresh access URL whenever a browser is opened.
export function browserAccessPath(raw: string, service?: BrowserServiceTarget): string | null {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return raw;
  }
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) return raw;
  const local = ['localhost', '127.0.0.1', '[::1]'].includes(url.hostname);
  let matchesService = false;
  try {
    matchesService = !!service && url.origin === new URL(service.url).origin;
  } catch {
    return null;
  }
  if (!matchesService && !local) return raw;
  if (service && service.machinePrefix !== currentPrefix()) return null;
  const prefix = service?.backend ? `${currentPrefix()}/b/${service.backend}` : currentApiPrefix();
  if (!currentPrefix() && !service?.backend) return raw;
  const path =
    matchesService &&
    service?.previewPath &&
    /^\/api\/proxy\/\d+~[A-Za-z0-9_-]+\/$/.test(service.previewPath)
      ? service.previewPath.replace(/\/$/, '')
      : `/api/proxy/${url.port || (url.protocol === 'https:' ? '443' : '80')}`;
  const params = new URLSearchParams(url.search);
  // Never carry a pasted sign-in token or an old proxy pass into a new URL.
  params.delete('access_token');
  const query = params.toString();
  const origin = !currentPrefix() && service?.backend && routerPreviewOrigin ? routerPreviewOrigin : '';
  return `${origin}${prefix}${path}${url.pathname}${query ? `?${query}` : ''}${url.hash}`;
}

// A proxy pass is scoped to one machine, backend and exact listener. Sign-in
// tokens remain in the ticket-mint Authorization header, never in page URLs.
export async function browserAccessUrl(
  raw: string,
  service?: BrowserServiceTarget,
): Promise<string | null> {
  const scope = currentRoutingScope().key;
  const path = browserAccessPath(raw, service);
  if (path === null || !relayNeedsTicket(path)) return path;
  const match = path.match(/^(\/m\/[^/]+(?:\/b\/[^/]+)?\/api\/proxy\/[^/]+)\//);
  if (!match) return path;
  const pass = await mintRelayTicket(match[1] + '/', 'proxy');
  if (scope !== currentRoutingScope().key) return null;
  return withQueryParam(path, 'access_token', pass);
}

/**
 * The browser tile's iframe sandbox for src. A page on the app's own origin
 * (a pane's server through /api/proxy, or anything else the daemon or cloud
 * serves) must not keep that origin: it could read the app's storage and its
 * requests would pass as the person's. Other origins (a localhost dev server)
 * keep their own, so their cookies and storage work.
 */
export function browserTileSandbox(src: string): string {
  const base = 'allow-forms allow-modals allow-popups allow-scripts';
  let sameOrigin = true;
  try {
    const u = new URL(src, location.href);
    // The desktop router's /b/<backend> previews keep same-origin until they
    // get an origin of their own (the follow-up); sandboxed, they lose their
    // scripts and data.
    sameOrigin = u.origin === location.origin && !u.pathname.startsWith('/b/');
  } catch {
    // unparseable: treat as the app's own
  }
  return sameOrigin ? base : `${base} allow-same-origin`;
}
