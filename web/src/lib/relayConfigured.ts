import { useEffect, useSyncExternalStore } from 'react';
import { isTauri } from '../voice';

// Whether this computer's daemon has a cloud relay at all (the desktop shell
// passes --relay only when the build has a hosted cloud configured; see
// daemon/relay_supervisor.go `configured`). A build with an empty
// SOLU_CLOUD_URL has none, and then the Cloud hosting switch and phone pairing
// aren't offered.
//
// Read from GET /api/cloud/status in the desktop shell: once per page here,
// and on every phone-access status poll (cloudPair.ts). null until known, on a
// failed read, and outside the shell — callers treat null as "configured", so
// nothing disappears on a slow or older daemon.

let configured: boolean | null = null;
let requested = false;
// Bumped by the test reset so a read still in flight can't land in the next test.
let generation = 0;
const listeners = new Set<() => void>();

/** Records what a /api/cloud/status response said. */
export function noteRelayConfigured(value: boolean): void {
  if (configured === value) return;
  configured = value;
  listeners.forEach((listener) => listener());
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}

function snapshot(): boolean | null {
  return configured;
}

/** false only when the daemon reported that it has no relay. */
export function useRelayConfigured(): boolean | null {
  const shell = isTauri();
  useEffect(() => {
    if (!shell || requested || configured !== null) return;
    requested = true;
    const asked = generation;
    fetch('/api/cloud/status')
      .then((r) => (r.ok ? r.json() as Promise<{ configured?: unknown }> : null))
      .then((body) => {
        if (asked === generation && typeof body?.configured === 'boolean') noteRelayConfigured(body.configured);
      })
      .catch(() => { /* unknown: keep offering the cloud surfaces */ });
  }, [shell]);
  const value = useSyncExternalStore(subscribe, snapshot);
  return shell ? value : null;
}

export function resetRelayConfiguredForTests(): void {
  configured = null;
  requested = false;
  generation += 1;
  listeners.forEach((listener) => listener());
}
