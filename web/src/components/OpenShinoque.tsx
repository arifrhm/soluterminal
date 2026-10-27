import { useSyncExternalStore } from 'react';
import { currentRoutingScope, currentToken, mintRelayTicket, onRoutingScopeChange, type RoutingScope } from '../api';
import { useCloud } from '../cloud';
import { debugLog } from '../debug';
import { withQueryParam } from '../lib/ticketedEventSource';
import { useDaemonHealth } from '../lib/useDaemonHealth';

// The relayed shinoque URL. It carries no credential: the click handler
// mints a single-use ticket and the cloud swaps it for the session cookie,
// then redirects to this clean URL (cloud/relay_ticket.go).
export function shinoqueRelayURL(machineId: string, scope: RoutingScope, token: string | null): string | null {
  if (!/^[A-Za-z0-9_-]+$/.test(machineId) || scope.prefix !== `/m/${machineId}` || !token) return null;
  if (scope.backendPrefix && !/^\/b\/[A-Za-z0-9_-]+$/.test(scope.backendPrefix)) return null;
  return `${scope.prefix}${scope.backendPrefix}/shinoque/`;
}

export async function openShinoqueRelay(href: string, navigate: (url: string) => void = (url) => window.location.assign(url)): Promise<void> {
  const ticket = await mintRelayTicket(href, 'once');
  navigate(withQueryParam(href, 'ticket', ticket));
}

export function OpenShinoque() {
  const routingKey = useSyncExternalStore(onRoutingScopeChange, () => currentRoutingScope().key);
  const mode = useCloud((s) => s.mode);
  const isLocal = useCloud((s) => s.isLocal);
  const machineId = useCloud((s) => s.selectedMachineId);
  const { health, healthScopeKey } = useDaemonHealth();
  if (mode !== 'cloud' || isLocal || !machineId || health?.shinoqueRelay !== true || healthScopeKey !== routingKey) return null;
  const scope = currentRoutingScope();
  // api.health() may fall back to another backend when the selected one is
  // offline. Its capability must never enable a link to the offline backend.
  if (scope.backendPrefix && health.backendID !== scope.backendPrefix.slice(3)) return null;
  const href = shinoqueRelayURL(machineId, scope, currentToken());
  if (!href) return null;
  // A plain click mints a ticket first. Modified clicks (new tab or window)
  // keep the clean href, which the session cookie already banked by this
  // page's event stream authenticates.
  return (
    <a
      className="topbar-shinoque"
      href={href}
      onClick={(e) => {
        if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
        e.preventDefault();
        openShinoqueRelay(href).catch((err) => {
          debugLog('[open-shinoque] ticket failed', String(err));
          window.location.assign(href);
        });
      }}
    >
      Open shinoque
    </a>
  );
}
