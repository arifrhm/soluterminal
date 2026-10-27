// When and how the app's relayed traffic goes through the private relay
// channel instead of the plain (signed) relay path.
//
// A target uses the channel when all of these hold: this browser hasn't
// switched it off (privateRelayEnabled, on by default), the device is
// enrolled with the target's daemon, and the computer identity was pinned at
// enrolment. Then
// relayed fetches, WebSockets and the UI event stream share one channel per
// target, strictly: nothing for that target ever takes the plaintext relay.
// If the channel cannot open, requests fail, the target is reported as
// unreachable (PRIVATE_RELAY_STATUS_EVENT) and the channel is retried with
// backoff until it opens. A fallback there would let a relay, or a computer
// coming back with another identity, take over a connection before the
// handshake could object.

import {
  deviceEnrolledFor,
  deviceIdentity,
  pinnedComputerIdentity,
  relayPrefixFor,
  relayTarget,
  signDeviceRequest,
  withDeviceSignatureQuery,
} from '../deviceIdentity';
import type { EventStream } from '../ticketedEventSource';
import { b64url, bs, utf8 } from './bytes';
import { ChannelClosedError, HANDSHAKE_REFUSED, PrivateChannel } from './channel';
import { ChannelEventStream } from './eventStream';
import { PrivateRelayIdentityError } from './handshake';
import { httpOverStream, serializeBody } from './http';
import { ChannelWebSocket } from './websocket';

const FLAG_KEY = 'soluterminal.privateRelay';
const PATH = '/api/private-relay';
const MAX_BACKOFF_MS = 30_000;
// While the computer is only unreachable (a cloud deploy, the daemon
// reconnecting), retry at least this often: the longest wait is for a
// refused handshake, which a quick retry would not fix.
const MAX_UNREACHABLE_BACKOFF_MS = 5_000;
// A channel that dies this soon after opening counts as a failure, so a
// relay that accepts and drops it cannot make every request pay a handshake.
const SHORT_LIVED_MS = 10_000;

/**
 * Whether this browser routes enrolled machines through the private channel.
 * On by default: it applies only to a computer that approved this browser
 * and whose key is pinned (privateRelayCandidate). Turning it off is stored
 * as an explicit "0" and stays off.
 */
export function privateRelayEnabled(): boolean {
  try {
    return localStorage.getItem(FLAG_KEY) !== '0';
  } catch {
    return false;
  }
}

/**
 * Opt this browser in or out. Turning it off closes open channels. Turning
 * it on does not touch connections already open on the plaintext path: the
 * caller reloads the page (Settings does) so every one of them is redone.
 */
export function setPrivateRelayEnabled(on: boolean): void {
  try {
    localStorage.setItem(FLAG_KEY, on ? '1' : '0');
  } catch {
    // Blocked storage: the channel stays off.
  }
  if (!on) closePrivateRelayChannels();
}

export interface PrivateRelayHooks {
  /** The cloud sign-in subprotocol offer for the channel's WebSocket. */
  protocols?: () => string[] | undefined;
  /** Overrides the channel URL (tests). */
  channelUrl?: (target: string) => string;
  /**
   * The daemon refused this device's handshake (it was removed, or isn't
   * approved): ask the person to approve it again (DeviceApprovalPrompt).
   */
  onApprovalNeeded?: (target: string) => void;
  /**
   * This device's standing on target's daemon (its exempt status endpoint),
   * asked after a connect failure: a daemon that no longer knows the device
   * answers the upgrade with 401, which a WebSocket only sees as close 1006.
   */
  deviceStatus?: (target: string, deviceId: string) => Promise<'approved' | 'not-approved' | 'unknown'>;
  WebSocketImpl?: typeof WebSocket;
}

let hooks: PrivateRelayHooks = {};

export function configurePrivateRelay(h: PrivateRelayHooks): void {
  hooks = { ...hooks, ...h };
}

/**
 * The daemon target this routing could use the channel for, whether or not
 * this browser opted in: relayed, enrolled, and its computer key pinned.
 */
export function privateRelayCandidate(prefix: string, backendPrefix: string): string | null {
  if (typeof crypto === 'undefined' || !crypto.subtle || typeof WebSocket === 'undefined') return null;
  const target = relayTarget(prefix, backendPrefix);
  if (!target || !deviceEnrolledFor(target) || !pinnedComputerIdentity(target)) return null;
  return target;
}

/** The daemon target whose traffic for this routing goes through the channel, or null. */
export function privateRelayTarget(prefix: string, backendPrefix: string): string | null {
  if (!privateRelayEnabled()) return null;
  return privateRelayCandidate(prefix, backendPrefix);
}

/** Fired (detail: {target}) when a computer fails to prove the identity pinned for it. */
export const PRIVATE_RELAY_IDENTITY_EVENT = 'soluterminal:private-relay-identity-changed';

export const PRIVATE_RELAY_IDENTITY_MESSAGE =
  "This computer's identity changed — someone may be intercepting the connection, or SoluTerminal was " +
  'reinstalled there. Remove and re-approve this browser on the computer.';

/**
 * The computer behind target did not prove the identity this browser pinned.
 * Requests are refused, not sent the ordinary way: that is what someone in
 * the middle would want.
 */
export class PrivateRelayIdentityRefused extends Error {
  constructor(readonly target: string, readonly cause?: unknown) {
    super(PRIVATE_RELAY_IDENTITY_MESSAGE);
    this.name = 'PrivateRelayIdentityRefused';
  }
}

// Turning the switch on in another tab or window of this browser: reload
// here too, so this one's plaintext relay connections end as well.
if (typeof window !== 'undefined' && typeof window.addEventListener === 'function') {
  window.addEventListener('storage', (e: StorageEvent) => {
    if (e.key === FLAG_KEY && e.newValue === '1' && e.oldValue !== '1') {
      try {
        window.location.reload();
      } catch {
        // not a page
      }
    }
  });
}

/** Fired (detail: {target, reachable}) when a strict target becomes unreachable or reachable again. */
export const PRIVATE_RELAY_STATUS_EVENT = 'soluterminal:private-relay-status';

const unreachable = new Set<string>();

/** Targets whose channel cannot be opened right now (and whose requests fail). */
export function privateRelayUnreachableTargets(): string[] {
  return [...unreachable];
}

function setReachable(target: string, reachable: boolean): void {
  if (reachable === !unreachable.has(target)) return;
  if (reachable) unreachable.delete(target);
  else unreachable.add(target);
  try {
    window.dispatchEvent(new CustomEvent(PRIVATE_RELAY_STATUS_EVENT, { detail: { target, reachable } }));
  } catch {
    // no window
  }
}

export class PrivateRelayUnavailable extends Error {
  constructor(message: string, readonly cause?: unknown) {
    super(message);
    this.name = 'PrivateRelayUnavailable';
  }
}

/** The computer refused this browser's handshake: it isn't approved there (any more). */
export class PrivateRelayApprovalNeeded extends PrivateRelayUnavailable {
  constructor(message: string, cause?: unknown) {
    super(message, cause);
    this.name = 'PrivateRelayApprovalNeeded';
  }
}

interface Slot {
  /** The pinned key the computer failed to prove; requests are refused while it is still the pin. */
  refusedPin?: string;
  channel?: PrivateChannel;
  opening?: Promise<PrivateChannel>;
  failures: number;
  retryAt: number;
  retryTimer?: ReturnType<typeof setTimeout>;
  /** When the app last asked for this target; the self-retry stops once it hasn't for a while. */
  lastUsed: number;
  /** The last handshake was refused: this browser must be approved again. */
  approvalNeeded?: boolean;
}

// The self-retry gives up on a target the app stopped asking for (another
// machine or backend now in view), so an offline one doesn't keep a
// "Can't reach" note up or a retry loop going.
const RETRY_WHILE_UNUSED_MS = 2 * 60_000;

const slots = new Map<string, Slot>();

// Retries on its own while the target is unreachable, so it recovers (or the
// identity check runs) without waiting for the app's next request.
function scheduleRetry(target: string, s: Slot): void {
  if (s.retryTimer !== undefined) clearTimeout(s.retryTimer);
  s.retryTimer = setTimeout(() => {
    s.retryTimer = undefined;
    if (slots.get(target) !== s || !privateRelayEnabled()) return;
    if (Date.now() - s.lastUsed > RETRY_WHILE_UNUSED_MS) {
      setReachable(target, true); // not in use: nothing to report
      return;
    }
    void privateChannelFor(target, true).catch(() => {});
  }, Math.max(0, s.retryAt - Date.now()) + 50);
}

function backoff(slot: Slot, refused = false): void {
  slot.failures++;
  const cap = refused ? MAX_BACKOFF_MS : MAX_UNREACHABLE_BACKOFF_MS;
  const base = Math.min(cap, 1000 * 2 ** (slot.failures - 1));
  slot.retryAt = Date.now() + base / 2 + Math.random() * (base / 2);
}

function defaultChannelUrl(target: string): string {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  return `${proto}://${location.host}${relayPrefixFor(target)}${PATH}`;
}

async function openChannel(target: string): Promise<PrivateChannel> {
  const pinned = pinnedComputerIdentity(target);
  if (!pinned) throw new Error('private relay: no pinned computer identity');
  const me = await deviceIdentity();
  // The daemon's device gate checks the upgrade like any relayed stream.
  const sig = await signDeviceRequest(target, 'GET', PATH, '', new Uint8Array());
  const url = withDeviceSignatureQuery((hooks.channelUrl ?? defaultChannelUrl)(target), sig);
  return PrivateChannel.open({
    url,
    protocols: hooks.protocols?.(),
    WebSocketImpl: hooks.WebSocketImpl,
    deviceId: me.id,
    target,
    pinnedIdentity: pinned,
    sign: async (message) => b64url(new Uint8Array(await crypto.subtle.sign(
      { name: 'ECDSA', hash: 'SHA-256' }, me.key.privateKey, bs(utf8.encode(message)),
    ))),
  });
}

/**
 * The open channel for target, opening it if needed. Rejects with
 * PrivateRelayUnavailable (cannot open now; retried with backoff) or
 * PrivateRelayIdentityRefused (the computer is not the pinned one). Either
 * way the request fails: there is no plaintext fallback for this target.
 */
export async function privateChannelFor(target: string, selfRetry = false): Promise<PrivateChannel> {
  let slot = slots.get(target);
  if (!slot) {
    slot = { failures: 0, retryAt: 0, lastUsed: Date.now() };
    slots.set(target, slot);
  }
  if (!selfRetry) slot.lastUsed = Date.now();
  const pin = pinnedComputerIdentity(target);
  if (slot.refusedPin !== undefined) {
    // Still the key the computer failed to prove: keep refusing until the
    // person forgets it and approves this browser again.
    if (slot.refusedPin === pin) throw new PrivateRelayIdentityRefused(target);
    slot.refusedPin = undefined;
  }
  if (slot.channel?.isOpen) return slot.channel;
  slot.channel = undefined;
  if (!slot.opening) {
    if (Date.now() < slot.retryAt) {
      throw slot.approvalNeeded
        ? new PrivateRelayApprovalNeeded('private relay: waiting for approval')
        : new PrivateRelayUnavailable('private relay: waiting to retry');
    }
    const s = slot;
    s.opening = openChannel(target).then(
      (ch) => {
        s.opening = undefined;
        if (slots.get(target) !== s) {
          // Channels were closed (sign-out, opt-out) while this one opened.
          ch.close();
          throw new PrivateRelayUnavailable('private relay: closed while opening');
        }
        s.channel = ch;
        s.failures = 0;
        s.retryAt = 0;
        s.approvalNeeded = false;
        setReachable(target, true);
        ch.onclose = () => {
          if (s.channel === ch) s.channel = undefined;
          if (slots.get(target) !== s) return; // closed on purpose
          if (Date.now() - ch.openedAt < SHORT_LIVED_MS) {
            backoff(s);
            setReachable(target, false); // requests wait for the retry meanwhile
          }
          // Reopen on our own: a computer that restarted is checked (and its
          // identity proved) before the app's sockets come back.
          scheduleRetry(target, s);
        };
        return ch;
      },
      async (err: unknown) => {
        if (err instanceof PrivateRelayIdentityError) {
          s.opening = undefined;
          s.refusedPin = pin ?? '';
          setReachable(target, true); // reachable, but refused: the identity warning says so
          console.error(`[private-relay] ${target}: ${err.message}; refusing requests to it`);
          try {
            window.dispatchEvent(new CustomEvent(PRIVATE_RELAY_IDENTITY_EVENT, { detail: { target } }));
          } catch {
            // no window (tests in node)
          }
          throw new PrivateRelayIdentityRefused(target, err);
        }
        // A refused handshake (the device was removed, or the clock is off)
        // won't succeed on a quick retry: wait the longest, and ask the person
        // to approve this browser again rather than report it unreachable.
        let refused = err instanceof ChannelClosedError && err.code === HANDSHAKE_REFUSED;
        if (!refused && err instanceof ChannelClosedError && hooks.deviceStatus) {
          // Refused before the upgrade (a removed device gets 401, seen as
          // 1006)? One status check per failed attempt, inside the backoff;
          // s.opening stays set meanwhile, so no attempt starts beside it.
          try {
            const me = await deviceIdentity();
            refused = (await hooks.deviceStatus(target, me.id)) === 'not-approved';
          } catch {
            // unknown: treat as unreachable
          }
        }
        s.opening = undefined;
        s.approvalNeeded = refused;
        if (refused) s.failures = Math.max(s.failures, 6);
        backoff(s, refused);
        if (slots.get(target) === s) {
          if (refused) {
            setReachable(target, true);
            hooks.onApprovalNeeded?.(target);
          } else {
            setReachable(target, false);
          }
          scheduleRetry(target, s);
        }
        console.warn(`[private-relay] ${target}: channel unavailable (${(err as Error)?.message ?? err}); retrying in ${Math.round((s.retryAt - Date.now()) / 1000)} s`);
        throw refused
          ? new PrivateRelayApprovalNeeded('private relay: approval needed', err)
          : new PrivateRelayUnavailable('private relay: channel unavailable', err);
      },
    );
  }
  return slot.opening!;
}

/** Milliseconds until target's channel may be retried (0 when it may now). */
export function privateRelayRetryIn(target: string): number {
  return Math.max(0, (slots.get(target)?.retryAt ?? 0) - Date.now());
}

/**
 * Closes every channel (sign-out, machine or account change, opt-out). A
 * computer that failed to prove its pinned identity stays refused.
 */
export function closePrivateRelayChannels(): void {
  for (const [target, s] of [...slots]) {
    if (s.retryTimer !== undefined) clearTimeout(s.retryTimer);
    // Replace the slot first, so the channel's close is seen as deliberate.
    if (s.refusedPin === undefined) slots.delete(target);
    else slots.set(target, { refusedPin: s.refusedPin, failures: 0, retryAt: 0, lastUsed: 0 });
    s.channel?.close();
    setReachable(target, true);
  }
}

/** The relay routing in a URL path: /m/<machine>[/b/<backend>]<rest>. */
export function splitRelayPath(pathname: string): { prefix: string; backendPrefix: string; rest: string } | null {
  const m = /^(\/m\/[^/]+)(\/b\/[^/]+)?(\/.*)?$/.exec(pathname);
  if (!m) return null;
  return { prefix: m[1], backendPrefix: m[2] ?? '', rest: m[3] ?? '/' };
}

function abortReason(signal: AbortSignal): unknown {
  return signal.reason instanceof Error ? signal.reason : new DOMException('The operation was aborted.', 'AbortError');
}

/**
 * fetch(url, init) for a relayed URL: through the private channel when it
 * applies (failing when the channel cannot open), else the ordinary fetch.
 */
export async function relayFetch(url: string, init: RequestInit, fallback: typeof fetch = fetch): Promise<Response> {
  let u: URL;
  try {
    u = new URL(url, location.href);
  } catch {
    return fallback(url, init);
  }
  const route = u.origin === location.origin ? splitRelayPath(u.pathname) : null;
  const target = route ? privateRelayTarget(route.prefix, route.backendPrefix) : null;
  if (!route || !target) return fallback(url, init);
  const signal = init.signal ?? null;
  if (signal?.aborted) throw abortReason(signal);
  let channel: PrivateChannel;
  try {
    channel = await (signal
      ? Promise.race([privateChannelFor(target), new Promise<never>((_, rej) => {
          signal.addEventListener('abort', () => rej(abortReason(signal)), { once: true });
        })])
      : privateChannelFor(target));
  } catch (err) {
    // A TypeError like a failed fetch; the cause says whether it was a refusal.
    throw err instanceof PrivateRelayUnavailable ? new TypeError(`Can't reach this computer securely (${err.message})`, { cause: err }) : err;
  }
  const method = (init.method ?? 'GET').toUpperCase();
  const headers = new Headers(init.headers);
  const body = await serializeBody(method, init, headers);
  return httpOverStream(channel.openStream(), {
    method,
    path: route.rest + u.search,
    headers,
    body,
    forwardedPrefix: route.prefix + route.backendPrefix,
    signal,
  }, u.href);
}

/** Subprotocols that go inside the channel: the cloud's bearer stays outside. */
function innerProtocols(protocols?: string | string[]): string[] {
  const list = protocols === undefined ? [] : Array.isArray(protocols) ? protocols : [protocols];
  return list.filter((p) => !p.startsWith('bearer.'));
}

export interface RelayScope {
  prefix: string;
  backendPrefix: string;
}

/**
 * A WebSocket to a relayed URL through the private channel. When the channel
 * cannot open the socket fails (error, then close 1006) like any unreachable
 * socket, and the caller's reconnect loop tries again. Callers check
 * privateRelayTarget first.
 */
export function openPrivateRelayWebSocket(
  url: string,
  protocols: string | string[] | undefined,
  scope: RelayScope,
): WebSocket {
  const target = privateRelayTarget(scope.prefix, scope.backendPrefix);
  const u = new URL(url, location.href);
  const routed = scope.prefix + scope.backendPrefix;
  const path = (u.pathname.startsWith(routed) ? u.pathname.slice(routed.length) : u.pathname) + u.search;
  const source = (target ? privateChannelFor(target) : Promise.reject(new PrivateRelayUnavailable('not eligible'))).then(
    (ch) => ({ stream: ch.openStream(), path, forwardedPrefix: routed }),
  );
  return new ChannelWebSocket(url, source, innerProtocols(protocols)) as unknown as WebSocket;
}

/**
 * The UI event stream at a relayed URL through the private channel. When the
 * channel cannot open it reports CLOSED through onerror, and the caller's
 * rebuild loop (with its backoff) tries again.
 */
export function openPrivateRelayEventStream(url: string, scope: RelayScope): EventStream {
  const target = privateRelayTarget(scope.prefix, scope.backendPrefix);
  const u = new URL(url, location.href);
  const routed = scope.prefix + scope.backendPrefix;
  const path = (u.pathname.startsWith(routed) ? u.pathname.slice(routed.length) : u.pathname) + u.search;
  const source = (target ? privateChannelFor(target) : Promise.reject(new PrivateRelayUnavailable('not eligible'))).then(
    (ch) => ({
      response: (signal: AbortSignal) => httpOverStream(ch.openStream(), {
        method: 'GET',
        path,
        headers: new Headers({ Accept: 'text/event-stream', 'Cache-Control': 'no-cache' }),
        body: null,
        forwardedPrefix: routed,
        signal,
      }, u.href),
    }),
  );
  return new ChannelEventStream(source);
}
