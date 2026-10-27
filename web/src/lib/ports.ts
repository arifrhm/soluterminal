import type { BrowserInfo, Port, Workspace, WorkspaceSummary } from '../types';

export type PortScope = 'workspace' | 'panes' | 'all';

// A workspace read from the current daemon is fresh routing provenance. This
// lets the same saved tile work both locally and through that daemon's relay.
export function bindSavedBrowser(browser: BrowserInfo, machinePrefix: string): BrowserInfo {
  return browser.service ? { ...browser, service: { ...browser.service, machinePrefix } } : browser;
}
export function portKey(p: Port): string {
  return `${p.backend ?? ''}|${p.proto}|${p.addr}|${p.port}|${p.pid}|${p.firstAt}`;
}
export function portUrl(p: Port): string {
  let host = p.addr;
  if (!host || host === '0.0.0.0') host = '127.0.0.1';
  if (host === '::') host = '::1';
  if (host.includes(':')) host = `[${host}]`;
  return `${p.scheme ?? 'http'}://${host}:${p.port}`;
}
export function isWebService(p: Port): boolean {
  return p.webStatus === 'ready' && (p.scheme === 'http' || p.scheme === 'https');
}

// A saved tile follows the app in its original pane, not any process that
// happens to take the same port. Only fresh discovery from the bound machine
// can replace its listener selector; never fall back to an ambiguous numeric URL.
export function reconnectServiceBrowsers(
  browsers: Record<string, BrowserInfo>,
  ports: readonly Port[],
  machinePrefix: string,
): Record<string, BrowserInfo> {
  let next = browsers;
  for (const [id, browser] of Object.entries(browsers)) {
    const service = browser.service;
    if (!service?.paneId || service.machinePrefix !== machinePrefix) continue;
    const matches = ports.filter((p) =>
      p.paneId === service.paneId && p.backend === service.backend &&
      p.port === service.port && p.addr === service.addr && p.proto === service.proto &&
      !!p.previewPath && isWebService(p),
    );
    if (matches.length !== 1) continue;
    const port = matches[0];
    const url = portUrl(port);
    if (service.previewPath === port.previewPath && service.url === url) continue;
    let page: URL;
    try {
      page = new URL(browser.url);
      if (page.origin !== new URL(service.url).origin) continue;
    } catch { continue; }
    if (next === browsers) next = { ...browsers };
    next[id] = {
      ...browser,
      url: page.origin === new URL(url).origin ? browser.url : `${url}${page.pathname}${page.search}${page.hash}`,
      service: { ...service, url, previewPath: port.previewPath },
    };
  }
  return next;
}
export function workspacePorts(
  ports: readonly Port[],
  activeWorkspace: string,
  activeBackend: string | null,
  workspaces: readonly (Workspace | WorkspaceSummary)[],
): Port[] {
  const workspace = workspaces.find(
    (w) =>
      w.name === activeWorkspace && (!w.backend || !activeBackend || w.backend === activeBackend),
  );
  return ports.filter(
    (p) =>
      (!p.backend || !activeBackend || p.backend === activeBackend) &&
      ((!!p.paneId && !!workspace?.paneIds?.includes(p.paneId)) || p.workspace === activeWorkspace),
  );
}
export function visiblePorts(
  ports: readonly Port[],
  scope: PortScope,
  includeOther: boolean,
  current: readonly Port[],
): Port[] {
  const candidates =
    scope === 'workspace' ? current : scope === 'panes' ? ports.filter((p) => p.paneId) : ports;
  return candidates
    .filter(
      (p) => includeOther || isWebService(p) || (scope === 'all' && p.webStatus !== 'internal'),
    )
    .sort((a, b) => a.port - b.port || portKey(a).localeCompare(portKey(b)));
}
