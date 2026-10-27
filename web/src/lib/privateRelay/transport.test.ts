import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../deviceIdentity', async (importOriginal) => {
  const real = await importOriginal<typeof import('../deviceIdentity')>();
  // jsdom has no IndexedDB; a throwaway key stands in for the device's
  // (made once, so retries under fake timers don't wait on real crypto).
  const key = await crypto.subtle.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, false, ['sign', 'verify']) as CryptoKeyPair;
  return {
    ...real,
    deviceIdentity: async () => ({ id: 'dev_test', spki: 'spki', code: 'code', key }),
    signDeviceRequest: async (target: string) => ({ id: 'dev_test', time: '1', nonce: 'n', target, sig: 's' }),
  };
});

import { markDeviceEnrolled, pinComputerIdentity } from '../deviceIdentity';
import { openPrivateRelayEventStream } from './transport';
import {
  closePrivateRelayChannels,
  configurePrivateRelay,
  openPrivateRelayWebSocket,
  PRIVATE_RELAY_IDENTITY_EVENT,
  PRIVATE_RELAY_STATUS_EVENT,
  PrivateRelayApprovalNeeded,
  privateRelayCandidate,
  PrivateRelayIdentityRefused,
  privateRelayEnabled,
  privateRelayRetryIn,
  privateRelayTarget,
  privateRelayUnreachableTargets,
  relayFetch,
  setPrivateRelayEnabled,
} from './transport';
import { until } from './testdata/fakePeer';

/** A socket to a relay that never completes the channel. */
class RefusingSocket {
  static made: string[] = [];
  binaryType = 'blob';
  readyState = 0;
  onopen: (() => void) | null = null;
  onclose: ((ev: { code: number; reason: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: (() => void) | null = null;
  constructor(readonly url: string) {
    RefusingSocket.made.push(url);
    setTimeout(() => {
      this.readyState = 3;
      this.onclose?.({ code: 1006, reason: '' });
    }, 0);
  }
  send() {}
  close() {}
}

/** A daemon that refuses the handshake, as it does once the device is removed. */
class RefusedHandshakeSocket {
  static made = 0;
  binaryType = 'blob';
  readyState = 0;
  onopen: (() => void) | null = null;
  onclose: ((ev: { code: number; reason: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: (() => void) | null = null;
  constructor() {
    RefusedHandshakeSocket.made++;
    setTimeout(() => {
      this.readyState = 1;
      this.onopen?.();
    }, 0);
  }
  send() {
    setTimeout(() => {
      this.readyState = 3;
      this.onclose?.({ code: 1008, reason: 'handshake refused' });
    }, 0);
  }
  close() {}
}

/** A computer answering with an identity other than the pinned one. */
class ImpostorSocket {
  static made = 0;
  binaryType = 'blob';
  readyState = 0;
  onopen: (() => void) | null = null;
  onclose: ((ev: { code: number; reason: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  constructor() {
    ImpostorSocket.made++;
    setTimeout(() => {
      this.readyState = 1;
      this.onopen?.();
    }, 0);
  }
  send() {
    setTimeout(() => this.onmessage?.({ data: JSON.stringify({ eph: 'AA', nonce: 'n', identity: 'MFkwOTHER', sig: 's' }) }), 0);
  }
  close() {
    this.readyState = 3;
  }
}

describe('private relay transport', () => {
  beforeEach(() => {
    localStorage.clear();
    closePrivateRelayChannels();
    RefusingSocket.made = [];
    configurePrivateRelay({
      WebSocketImpl: RefusingSocket as unknown as typeof WebSocket,
      channelUrl: (t) => `wss://cloud.test/${t}/api/private-relay`,
    });
  });
  afterEach(() => {
    closePrivateRelayChannels(); // and their retry timers
    vi.restoreAllMocks();
  });

  it('is on by default, and applies only once the computer is enrolled and pinned', () => {
    expect(privateRelayEnabled()).toBe(true);
    expect(privateRelayTarget('/m/mach_1', '/b/wsl')).toBeNull(); // not enrolled
    markDeviceEnrolled('mach_1/wsl');
    expect(privateRelayTarget('/m/mach_1', '/b/wsl')).toBeNull(); // nothing pinned
    pinComputerIdentity('mach_1/wsl', 'MFkw…');
    expect(privateRelayTarget('/m/mach_1', '/b/wsl')).toBe('mach_1/wsl');
    expect(privateRelayTarget('/m/mach_1', '/b/win')).toBeNull();
    expect(privateRelayTarget('', '')).toBeNull();
    setPrivateRelayEnabled(false);
    expect(privateRelayEnabled()).toBe(false); // an explicit "off" stays off
    expect(privateRelayTarget('/m/mach_1', '/b/wsl')).toBeNull();
    setPrivateRelayEnabled(true);
    expect(privateRelayTarget('/m/mach_1', '/b/wsl')).toBe('mach_1/wsl');
  });

  it('leaves requests alone when the channel does not apply', async () => {
    const fallback = vi.fn(async () => new Response('plain'));
    const init = { method: 'GET' };
    const r = await relayFetch('/m/mach_1/b/wsl/api/health', init, fallback as unknown as typeof fetch);
    expect(await r.text()).toBe('plain');
    expect(fallback).toHaveBeenCalledWith('/m/mach_1/b/wsl/api/health', init);
    expect(RefusingSocket.made).toEqual([]);
  });

  // Strict: once this browser opted in for a pinned computer, nothing for it
  // takes the plaintext relay. A fallback while the computer was down let a
  // pane socket reconnect in plaintext to a daemon that came back with
  // another identity, before the channel could notice.
  it('fails fetches, sockets and event streams when the channel cannot open, without any plaintext fallback', async () => {
    markDeviceEnrolled('mach_1/wsl');
    pinComputerIdentity('mach_1/wsl', 'MFkw…');
    setPrivateRelayEnabled(true);
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const status: Array<{ target: string; reachable: boolean }> = [];
    const onStatus = (e: Event) => status.push((e as CustomEvent<{ target: string; reachable: boolean }>).detail);
    window.addEventListener(PRIVATE_RELAY_STATUS_EVENT, onStatus);
    try {
      const fallback = vi.fn(async () => new Response('plaintext'));
      const unreachableErr = await relayFetch('/m/mach_1/b/wsl/api/health', {}, fallback as unknown as typeof fetch).catch((e: unknown) => e);
      expect((unreachableErr as Error).message).toMatch(/securely/);
      expect((unreachableErr as Error).cause).not.toBeInstanceOf(PrivateRelayApprovalNeeded); // unreachable, not refused
      expect(RefusingSocket.made).toHaveLength(1);
      expect(RefusingSocket.made[0]).toMatch(/^wss:\/\/cloud\.test\/mach_1\/wsl\/api\/private-relay\?solu_dev=dev_test&/);
      expect(warn).toHaveBeenCalledWith(expect.stringContaining('channel unavailable'));
      expect(status).toEqual([{ target: 'mach_1/wsl', reachable: false }]);
      expect(privateRelayUnreachableTargets()).toEqual(['mach_1/wsl']);

      // Within the backoff window requests fail at once, still never in plaintext.
      await expect(relayFetch('/m/mach_1/b/wsl/api/health', {}, fallback as unknown as typeof fetch)).rejects.toThrow(/securely/);
      const ws = openPrivateRelayWebSocket('wss://cloud.test/m/mach_1/b/wsl/api/panes/p1/io?cols=80', ['solu-auth', 'bearer.jwt'], { prefix: '/m/mach_1', backendPrefix: '/b/wsl' });
      const events: string[] = [];
      ws.addEventListener('error', () => events.push('error'));
      ws.addEventListener('close', () => events.push('close'));
      await until(() => events.length === 2);
      expect(events).toEqual(['error', 'close']);
      const es = openPrivateRelayEventStream('/m/mach_1/b/wsl/api/ui/events', { prefix: '/m/mach_1', backendPrefix: '/b/wsl' });
      await until(() => es.readyState === 2);
      expect(fallback).not.toHaveBeenCalled();
      expect(RefusingSocket.made).toHaveLength(1);

      // It retries on its own, with backoff, without waiting for a request.
      await until(() => RefusingSocket.made.length === 2, 3000);
    } finally {
      window.removeEventListener(PRIVATE_RELAY_STATUS_EVENT, onStatus);
    }
  });

  it('leaves a computer whose person turned it off on the ordinary path', async () => {
    markDeviceEnrolled('mach_1/wsl');
    pinComputerIdentity('mach_1/wsl', 'MFkw…');
    setPrivateRelayEnabled(false);
    const fallback = vi.fn(async () => new Response('signed path'));
    expect(await (await relayFetch('/m/mach_1/b/wsl/api/health', {}, fallback as unknown as typeof fetch)).text()).toBe('signed path');
    expect(RefusingSocket.made).toEqual([]);
  });

  // The daemon side of this is TestPrivateRelayEndsWhenTheDeviceIsRemoved:
  // a removed device's channel closes and its next handshake is refused.
  it('fails requests, asks for approval again and waits the longest after a refused handshake', async () => {
    markDeviceEnrolled('mach_1/wsl');
    pinComputerIdentity('mach_1/wsl', 'MFkw…');
    setPrivateRelayEnabled(true);
    const approvalNeeded = vi.fn();
    configurePrivateRelay({ WebSocketImpl: RefusedHandshakeSocket as unknown as typeof WebSocket, onApprovalNeeded: approvalNeeded });
    RefusedHandshakeSocket.made = 0;
    vi.spyOn(console, 'warn').mockImplementation(() => {});
    const fallback = vi.fn(async () => new Response('signed path'));
    for (let i = 0; i < 5; i++) {
      const err = await relayFetch('/m/mach_1/b/wsl/api/health', {}, fallback as unknown as typeof fetch).catch((e: unknown) => e);
      expect(err).toBeInstanceOf(TypeError);
      expect((err as Error).message).toMatch(/securely/);
      // The refusal and every request in its backoff say "approve this browser",
      // so the page shows the approval state rather than daemon troubleshooting.
      expect((err as Error).cause).toBeInstanceOf(PrivateRelayApprovalNeeded);
    }
    expect(fallback).not.toHaveBeenCalled();
    expect(RefusedHandshakeSocket.made).toBe(1);
    expect(privateRelayRetryIn('mach_1/wsl')).toBeGreaterThan(14_000);
    // The removed device learns it must be approved again (the prompt), not "retrying".
    expect(approvalNeeded).toHaveBeenCalledWith('mach_1/wsl');
    expect(privateRelayUnreachableTargets()).toEqual([]);
    configurePrivateRelay({ onApprovalNeeded: undefined });
  });

  // A daemon that no longer knows this device answers the channel's upgrade
  // with 401, which a WebSocket only sees as close 1006, never 1008.
  it('asks for approval again when a 1006 failure turns out to be a removed device', async () => {
    markDeviceEnrolled('mach_1/wsl');
    pinComputerIdentity('mach_1/wsl', 'MFkw…');
    setPrivateRelayEnabled(true);
    vi.spyOn(console, 'warn').mockImplementation(() => {});
    const approvalNeeded = vi.fn();
    const deviceStatus = vi.fn(async () => 'not-approved' as const);
    configurePrivateRelay({ onApprovalNeeded: approvalNeeded, deviceStatus });
    try {
      await expect(relayFetch('/m/mach_1/b/wsl/api/health', {})).rejects.toThrow(/securely/);
      expect(deviceStatus).toHaveBeenCalledWith('mach_1/wsl', 'dev_test');
      expect(approvalNeeded).toHaveBeenCalledWith('mach_1/wsl');
      expect(privateRelayUnreachableTargets()).toEqual([]);
      expect(privateRelayRetryIn('mach_1/wsl')).toBeGreaterThan(14_000);
    } finally {
      configurePrivateRelay({ onApprovalNeeded: undefined, deviceStatus: undefined });
    }
  });

  it('just retries a 1006 failure when the device is still approved (or its status is unknown)', async () => {
    markDeviceEnrolled('mach_1/wsl');
    pinComputerIdentity('mach_1/wsl', 'MFkw…');
    setPrivateRelayEnabled(true);
    vi.spyOn(console, 'warn').mockImplementation(() => {});
    const approvalNeeded = vi.fn();
    const deviceStatus = vi.fn(async () => 'approved' as const);
    configurePrivateRelay({ onApprovalNeeded: approvalNeeded, deviceStatus });
    try {
      await expect(relayFetch('/m/mach_1/b/wsl/api/health', {})).rejects.toThrow(/securely/);
      expect(deviceStatus).toHaveBeenCalledTimes(1);
      expect(approvalNeeded).not.toHaveBeenCalled();
      expect(privateRelayUnreachableTargets()).toEqual(['mach_1/wsl']);
      expect(privateRelayRetryIn('mach_1/wsl')).toBeLessThan(1_100);
      // Requests inside the backoff don't add status checks.
      await expect(relayFetch('/m/mach_1/b/wsl/api/health', {})).rejects.toThrow(/securely/);
      expect(deviceStatus).toHaveBeenCalledTimes(1);
      // The self-retry makes one more attempt, and one more check.
      await until(() => RefusingSocket.made.length === 2, 3000);
      await until(() => deviceStatus.mock.calls.length === 2, 3000);
      deviceStatus.mockRejectedValue(new Error('offline'));
      await until(() => RefusingSocket.made.length === 3, 5000);
      expect(approvalNeeded).not.toHaveBeenCalled();
    } finally {
      configurePrivateRelay({ onApprovalNeeded: undefined, deviceStatus: undefined });
    }
  });

  // A cloud deploy makes the computer unreachable for half a minute or so:
  // the channel must come back within a few seconds of it, not after a
  // long backoff (the longest wait is for a refused handshake).
  it('retries an unreachable computer at least every 5 s however long it is down', async () => {
    vi.useFakeTimers();
    try {
      markDeviceEnrolled('mach_1/wsl');
      pinComputerIdentity('mach_1/wsl', 'MFkw…');
      vi.spyOn(console, 'warn').mockImplementation(() => {});
      const first = expect(relayFetch('/m/mach_1/b/wsl/api/health', {})).rejects.toThrow(/securely/);
      await vi.advanceTimersByTimeAsync(100);
      await first;
      for (let i = 0; i < 12; i++) {
        expect(privateRelayRetryIn('mach_1/wsl')).toBeLessThanOrEqual(5_000);
        await vi.advanceTimersByTimeAsync(5_100);
      }
      expect(RefusingSocket.made.length).toBeGreaterThanOrEqual(12);
    } finally {
      vi.useRealTimers();
    }
  });

  it('stops retrying on its own once the app no longer uses the computer', async () => {
    vi.useFakeTimers();
    try {
      markDeviceEnrolled('mach_1/wsl');
      pinComputerIdentity('mach_1/wsl', 'MFkw…');
      setPrivateRelayEnabled(true);
      vi.spyOn(console, 'warn').mockImplementation(() => {});
      const first = expect(relayFetch('/m/mach_1/b/wsl/api/health', {})).rejects.toThrow(/securely/);
      await vi.advanceTimersByTimeAsync(100);
      await first;
      await vi.advanceTimersByTimeAsync(60_000);
      const whileInUse = RefusingSocket.made.length;
      expect(whileInUse).toBeGreaterThan(2); // it kept retrying
      expect(privateRelayUnreachableTargets()).toEqual(['mach_1/wsl']);
      await vi.advanceTimersByTimeAsync(5 * 60_000);
      const stopped = RefusingSocket.made.length;
      await vi.advanceTimersByTimeAsync(5 * 60_000);
      expect(RefusingSocket.made.length).toBe(stopped);
      expect(privateRelayUnreachableTargets()).toEqual([]);
    } finally {
      closePrivateRelayChannels();
      vi.useRealTimers();
    }
  });

  it('offers the channel only for an enrolled computer with a pinned key, flag or not', () => {
    expect(privateRelayCandidate('/m/mach_1', '/b/wsl')).toBeNull();
    markDeviceEnrolled('mach_1/wsl');
    expect(privateRelayCandidate('/m/mach_1', '/b/wsl')).toBeNull();
    pinComputerIdentity('mach_1/wsl', 'MFkw…');
    setPrivateRelayEnabled(false);
    expect(privateRelayCandidate('/m/mach_1', '/b/wsl')).toBe('mach_1/wsl');
  });

  // A computer that can't prove the pinned key may be someone in the middle:
  // nothing goes the ordinary way, the person is told, and it stays refused
  // until the pin changes.
  it('refuses requests, and warns once, when the computer is not the pinned one', async () => {
    const target = 'mach_2/wsl';
    markDeviceEnrolled(target);
    pinComputerIdentity(target, 'MFkwPINNED');
    setPrivateRelayEnabled(true);
    configurePrivateRelay({ WebSocketImpl: ImpostorSocket as unknown as typeof WebSocket });
    ImpostorSocket.made = 0;
    const error = vi.spyOn(console, 'error').mockImplementation(() => {});
    const warnings: string[] = [];
    const onWarn = (e: Event) => warnings.push((e as CustomEvent<{ target: string }>).detail.target);
    window.addEventListener(PRIVATE_RELAY_IDENTITY_EVENT, onWarn);
    try {
      const fallback = vi.fn(async () => new Response('signed path'));
      const url = '/m/mach_2/b/wsl/api/health';
      await expect(relayFetch(url, {}, fallback as unknown as typeof fetch)).rejects.toBeInstanceOf(PrivateRelayIdentityRefused);
      await expect(relayFetch(url, {}, fallback as unknown as typeof fetch)).rejects.toThrow(/identity changed/);
      expect(fallback).not.toHaveBeenCalled();
      expect(ImpostorSocket.made).toBe(1); // refused without another handshake
      expect(warnings).toEqual([target]);
      expect(error).toHaveBeenCalled();

      // Sockets and event streams are refused too.
      const ws = openPrivateRelayWebSocket('wss://cloud.test/m/mach_2/b/wsl/api/panes/p1/io', undefined, { prefix: '/m/mach_2', backendPrefix: '/b/wsl' });
      const events: string[] = [];
      ws.addEventListener('error', () => events.push('error'));
      ws.addEventListener('close', () => events.push('close'));
      await until(() => events.length === 2);
      expect(events).toEqual(['error', 'close']);
      const es = openPrivateRelayEventStream('/m/mach_2/b/wsl/api/ui/events', { prefix: '/m/mach_2', backendPrefix: '/b/wsl' });
      await until(() => es.readyState === 2);
      expect(ImpostorSocket.made).toBe(1);

      // Closing channels (sign-out, opt-out) does not lift the refusal.
      closePrivateRelayChannels();
      await expect(relayFetch(url, {}, fallback as unknown as typeof fetch)).rejects.toBeInstanceOf(PrivateRelayIdentityRefused);

      // A new pin (after removing and re-approving with matching codes) tries again.
      pinComputerIdentity(target, 'MFkwNEWPIN');
      await expect(relayFetch(url, {}, fallback as unknown as typeof fetch)).rejects.toBeInstanceOf(PrivateRelayIdentityRefused);
      expect(ImpostorSocket.made).toBe(2);
    } finally {
      window.removeEventListener(PRIVATE_RELAY_IDENTITY_EVENT, onWarn);
    }
  });
});
