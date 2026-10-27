import { useEffect, useState } from 'react';
import { useCloud } from '../cloud';
import { PRIVATE_RELAY_STATUS_EVENT, privateRelayUnreachableTargets } from '../lib/privateRelay';

// A small, persistent note while a computer this browser only reaches
// through the private relay channel can't be reached that way. Requests to it
// fail rather than take the plaintext relay, and the channel keeps retrying
// with backoff; this says so instead of leaving panes silently stalled.

export function privateRelayStatusText(name: string): string {
  return `Can't reach ${name} securely — retrying`;
}

export function PrivateRelayStatus() {
  const [targets, setTargets] = useState<string[]>(privateRelayUnreachableTargets);
  const machines = useCloud((s) => s.machines);

  useEffect(() => {
    const onStatus = () => setTargets(privateRelayUnreachableTargets());
    window.addEventListener(PRIVATE_RELAY_STATUS_EVENT, onStatus);
    return () => window.removeEventListener(PRIVATE_RELAY_STATUS_EVENT, onStatus);
  }, []);

  if (!targets.length) return null;
  const nameOf = (target: string) => {
    const machine = target.split('/')[0];
    return machines.find((m) => m.id === machine)?.name || machine;
  };
  return (
    <div
      role="status"
      className="private-relay-status"
      style={{
        position: 'fixed', bottom: 12, left: '50%', transform: 'translateX(-50%)', zIndex: 900,
        padding: '6px 12px', borderRadius: 6, fontSize: 12,
        background: 'var(--bg-elevated, var(--bg))', color: 'var(--fg)', border: '1px solid var(--border)',
        boxShadow: '0 2px 8px rgba(0,0,0,0.25)',
      }}
    >
      {[...new Set(targets.map(nameOf))].map(privateRelayStatusText).join(' · ')}
    </div>
  );
}
