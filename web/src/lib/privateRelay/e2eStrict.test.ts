// @vitest-environment node
//
// Strict mode end to end, against the daemon's real handler behind a relay
// the Go side can take down and bring back as another daemon
// (daemon/private_relay_web_e2e_test.go, which runs this file after
// e2e.test.ts). The bug this pins: while the computer was down the channel
// was "unavailable", so a pane socket fell back to the plaintext relay; when
// the computer came back with a new identity the socket reconnected in
// plaintext and streamed for over a minute before the channel noticed.
// Skipped without SOLU_PRIVATE_RELAY_E2E_CONFIG.
import { readFileSync } from 'node:fs';
import { afterAll, describe, expect, it, vi } from 'vitest';

interface E2EConfig {
  url: string;
  control: string;
  deviceId: string;
  target: string;
  pinned2: string;
  deviceJwk: JsonWebKey;
}

const { made } = vi.hoisted(() => {
  // The browser globals the transport and api.ts use.
  const store = new Map<string, string>();
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, String(v)),
      removeItem: (k: string) => void store.delete(k),
      clear: () => store.clear(),
    },
  });
  Object.defineProperty(globalThis, 'location', { configurable: true, value: new URL('https://cloud.test/') });
  const et = new EventTarget();
  Object.assign(globalThis, {
    addEventListener: et.addEventListener.bind(et),
    removeEventListener: et.removeEventListener.bind(et),
    dispatchEvent: et.dispatchEvent.bind(et),
  });
  Object.defineProperty(globalThis, 'window', { configurable: true, value: globalThis });
  // Every WebSocket the code constructs, plaintext or channel.
  const made: string[] = [];
  const NodeWebSocket = globalThis.WebSocket;
  if (NodeWebSocket) {
    Object.defineProperty(globalThis, 'WebSocket', {
      configurable: true,
      writable: true,
      value: class extends NodeWebSocket {
        constructor(url: string | URL, protocols?: string | string[]) {
          made.push(String(url));
          super(url, protocols);
        }
      },
    });
  }
  return { made };
});

vi.mock('../deviceIdentity', async (importOriginal) => {
  const real = await importOriginal<typeof import('../deviceIdentity')>();
  return {
    ...real,
    // The test daemon's approved device key stands in for IndexedDB's.
    deviceIdentity: async () => {
      const privateKey = await crypto.subtle.importKey('jwk', { ...cfg!.deviceJwk, ext: true }, { name: 'ECDSA', namedCurve: 'P-256' }, false, ['sign']);
      return { id: cfg!.deviceId, spki: '', code: '', key: { privateKey, publicKey: privateKey } as CryptoKeyPair };
    },
    // The relay in front doesn't gate the channel's upgrade.
    signDeviceRequest: async (target: string) => ({ id: cfg!.deviceId, time: '0', nonce: 'n', target, sig: 's' }),
  };
});

import { currentRoutingScope } from '../../api';
import { markDeviceEnrolled, pinComputerIdentity } from '../deviceIdentity';
import { openWebSocket } from '../signedStreams';
import {
  closePrivateRelayChannels,
  configurePrivateRelay,
  PRIVATE_RELAY_IDENTITY_EVENT,
  PRIVATE_RELAY_STATUS_EVENT,
  PrivateRelayIdentityRefused,
  relayFetch,
  setPrivateRelayEnabled,
} from './transport';

const configPath = process.env.SOLU_PRIVATE_RELAY_E2E_CONFIG;
const cfg: E2EConfig | null = configPath ? JSON.parse(readFileSync(configPath, 'utf8')) : null;

const until = async (cond: () => boolean, ms = 10_000) => {
  const end = Date.now() + ms;
  while (!cond()) {
    if (Date.now() > end) throw new Error('timed out waiting');
    await new Promise((r) => setTimeout(r, 10));
  }
};

const control = (what: string) => fetch(`${cfg!.control}/control/${what}`).then((r) => expect(r.status).toBe(204));

/** A socket opened the way Pane.tsx and the native renderer open theirs. */
function paneSocket() {
  const scope = { ...currentRoutingScope(), prefix: '/m/mach_test01', backendPrefix: '/b/wsl' };
  const ws = openWebSocket('wss://cloud.test/m/mach_test01/b/wsl/api/ws?pane=p1', ['solu-auth', 'bearer.jwt'], scope);
  ws.binaryType = 'arraybuffer';
  const events: string[] = [];
  const messages: string[] = [];
  ws.addEventListener('open', () => events.push('open'));
  ws.addEventListener('error', () => events.push('error'));
  ws.addEventListener('close', () => events.push('close'));
  ws.addEventListener('message', (ev) => messages.push(String((ev as MessageEvent).data)));
  return { ws, events, messages };
}

describe.skipIf(!cfg)('strict private relay across a restart with a new identity', () => {
  afterAll(() => closePrivateRelayChannels());

  it('never connects in plaintext while the computer is down, and refuses it when it returns as someone else', async () => {
    // Daemon 2 is the computer this browser approved and pinned.
    await control('up?daemon=2');
    markDeviceEnrolled(cfg!.target);
    pinComputerIdentity(cfg!.target, cfg!.pinned2);
    setPrivateRelayEnabled(true);
    configurePrivateRelay({ channelUrl: () => cfg!.url, protocols: () => ['solu-auth'] });
    const status: Array<{ reachable: boolean }> = [];
    const identity: string[] = [];
    window.addEventListener(PRIVATE_RELAY_STATUS_EVENT, (e) => status.push((e as CustomEvent).detail));
    window.addEventListener(PRIVATE_RELAY_IDENTITY_EVENT, (e) => identity.push((e as CustomEvent).detail.target));
    const plaintext = vi.fn(async () => new Response('plaintext'));

    const pane = paneSocket();
    await until(() => pane.messages.length > 0);
    expect(pane.messages[0]).toBe('hello:pane=p1');
    expect(await (await relayFetch('/m/mach_test01/b/wsl/api/echo', {}, plaintext as unknown as typeof fetch)).text()).toBe('GET ');

    // The computer goes down: the pane socket ends with the channel, and
    // while it is down nothing reaches it, in plaintext or otherwise.
    await control('down');
    await until(() => pane.events.includes('close'));
    await until(() => status.some((s) => !s.reachable));
    await expect(relayFetch('/m/mach_test01/b/wsl/api/echo', {}, plaintext as unknown as typeof fetch)).rejects.toThrow(/securely/);
    const whileDown = paneSocket(); // the pane's reconnect
    await until(() => whileDown.events.includes('close'));
    expect(whileDown.events).toEqual(['error', 'close']);

    // It comes back with another identity: the channel's own retry runs the
    // handshake, refuses it, and the person is warned.
    await control('up?daemon=3');
    await until(() => identity.length > 0);
    expect(identity).toEqual([cfg!.target]);
    await expect(relayFetch('/m/mach_test01/b/wsl/api/echo', {}, plaintext as unknown as typeof fetch)).rejects.toBeInstanceOf(PrivateRelayIdentityRefused);
    const afterReturn = paneSocket();
    await until(() => afterReturn.events.includes('close'));
    expect(afterReturn.events).toEqual(['error', 'close']);
    expect(afterReturn.messages).toEqual([]);

    // Not one request or socket took the plaintext relay.
    expect(plaintext).not.toHaveBeenCalled();
    expect(made.length).toBeGreaterThan(0);
    expect(made.filter((u) => !u.startsWith(cfg!.url))).toEqual([]);
  });
});
