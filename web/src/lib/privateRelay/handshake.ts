// The private relay channel handshake, device side. Normative description:
// docs/DESIGN-trusted-devices-and-private-relay.md (step 2); reference
// implementation: privateClientHandshake in daemon/private_relay.go. The
// fixture in testdata/handshake-vector.json is checked by both the Go and the
// TypeScript tests.
//
//   device → daemon  {v, device, target, time, nonce, eph, sig}
//   daemon → device  {eph, nonce, identity, sig}
//
// The keys depend on the exact bytes of both messages, so the bytes hashed
// here are exactly the bytes sent and received.

import { b64url, bs, concat, fromB64, hex, sha256, utf8 } from './bytes';

export const PRIVATE_RELAY_VERSION = 'SOLU-PRIVATE-V1';

export interface PrivateHello {
  v: 1;
  device: string;
  target: string;
  time: string;
  nonce: string;
  eph: string;
  sig: string;
}

export interface PrivateReply {
  eph: string;
  nonce: string;
  identity: string;
  sig: string;
}

/** What the device key signs (daemon privateHelloMessage). */
export function helloSigMessage(target: string, time: string, nonce: string, eph: string): string {
  return [`${PRIVATE_RELAY_VERSION}-C`, target, time, nonce, eph].join('\n');
}

/** The hello as sent: the daemon's field order, JSON-encoded once. */
export function encodeHello(h: PrivateHello): string {
  return JSON.stringify({ v: h.v, device: h.device, target: h.target, time: h.time, nonce: h.nonce, eph: h.eph, sig: h.sig });
}

/** What the computer's identity key signs (daemon privateReplyMessage). */
export async function replySigMessage(helloBytes: Uint8Array, eph: string, nonce: string, identity: string): Promise<string> {
  return [`${PRIVATE_RELAY_VERSION}-D`, hex(await sha256(helloBytes)), eph, nonce, identity].join('\n');
}

export interface RecordKeys {
  c2s: CryptoKey;
  s2c: CryptoKey;
}

/**
 * HKDF-SHA256 over the ECDH secret with salt sha256(hello ‖ reply), one
 * AES-256-GCM key per direction (daemon privateKeys).
 */
export async function deriveRecordKeys(
  shared: Uint8Array,
  helloBytes: Uint8Array,
  replyBytes: Uint8Array,
  extractable = false,
): Promise<RecordKeys> {
  const salt = await sha256(concat(helloBytes, replyBytes));
  const ikm = await crypto.subtle.importKey('raw', bs(shared), 'HKDF', false, ['deriveKey']);
  const mk = (info: string) =>
    crypto.subtle.deriveKey(
      { name: 'HKDF', hash: 'SHA-256', salt: bs(salt), info: bs(utf8.encode(`${PRIVATE_RELAY_VERSION} ${info}`)) },
      ikm,
      { name: 'AES-GCM', length: 256 },
      extractable,
      ['encrypt', 'decrypt'],
    );
  return { c2s: await mk('c2s'), s2c: await mk('s2c') };
}

/**
 * The pinned computer identity did not answer. The transport refuses
 * requests to that computer (no fallback) and warns the person.
 */
export class PrivateRelayIdentityError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'PrivateRelayIdentityError';
  }
}

export interface HandshakeIO {
  sendText(text: string): void;
  /** The next text message from the daemon. */
  nextText(): Promise<string>;
}

export interface HandshakeOptions {
  deviceId: string;
  /** `<machineId>/<backendId>`, or a bare machine id. */
  target: string;
  /** The approved device key's P1363 signature over message, base64url. */
  sign(message: string): Promise<string>;
  /** The computer identity (base64 SPKI) pinned at enrolment. */
  pinnedIdentity: string;
  /** Milliseconds since the epoch; tests fix it. */
  now?: () => number;
  /** Tests fix the ephemeral key and nonce to replay a vector. */
  ephemeral?: CryptoKeyPair;
  nonce?: Uint8Array;
}

export async function newEphemeralKey(): Promise<CryptoKeyPair> {
  return crypto.subtle.generateKey({ name: 'ECDH', namedCurve: 'P-256' }, false, ['deriveBits']) as Promise<CryptoKeyPair>;
}

function parseReply(text: string): PrivateReply {
  let v: unknown;
  try {
    v = JSON.parse(text);
  } catch {
    throw new Error('private relay: bad reply');
  }
  const r = v as Record<string, unknown> | null;
  if (!r || typeof r !== 'object' || ['eph', 'nonce', 'identity', 'sig'].some((k) => typeof r[k] !== 'string')) {
    throw new Error('private relay: bad reply');
  }
  return { eph: r.eph as string, nonce: r.nonce as string, identity: r.identity as string, sig: r.sig as string };
}

/** Run the device side of the handshake; resolves with the record keys. */
export async function clientHandshake(io: HandshakeIO, opts: HandshakeOptions): Promise<RecordKeys> {
  const eph = opts.ephemeral ?? (await newEphemeralKey());
  const ephRaw = new Uint8Array(await crypto.subtle.exportKey('raw', eph.publicKey));
  const nonceBytes = opts.nonce ?? crypto.getRandomValues(new Uint8Array(16));
  const time = String(Math.floor((opts.now ? opts.now() : Date.now()) / 1000));
  const nonce = b64url(nonceBytes);
  const ephB64 = b64url(ephRaw);
  const sig = await opts.sign(helloSigMessage(opts.target, time, nonce, ephB64));
  const helloText = encodeHello({ v: 1, device: opts.deviceId, target: opts.target, time, nonce, eph: ephB64, sig });
  const helloBytes = utf8.encode(helloText);
  io.sendText(helloText);

  const replyText = await io.nextText();
  const replyBytes = utf8.encode(replyText);
  const reply = parseReply(replyText);
  if (reply.identity !== opts.pinnedIdentity) {
    throw new PrivateRelayIdentityError("private relay: the computer's identity is not the one approved");
  }
  let identityKey: CryptoKey;
  try {
    identityKey = await crypto.subtle.importKey(
      'spki', bs(fromB64(reply.identity)), { name: 'ECDSA', namedCurve: 'P-256' }, false, ['verify'],
    );
  } catch {
    throw new PrivateRelayIdentityError("private relay: the computer's identity key is unreadable");
  }
  const signed = utf8.encode(await replySigMessage(helloBytes, reply.eph, reply.nonce, reply.identity));
  let sigBytes: Uint8Array;
  try {
    sigBytes = fromB64(reply.sig);
  } catch {
    sigBytes = new Uint8Array();
  }
  const ok = sigBytes.length === 64 &&
    await crypto.subtle.verify({ name: 'ECDSA', hash: 'SHA-256' }, identityKey, bs(sigBytes), bs(signed));
  if (!ok) throw new PrivateRelayIdentityError("private relay: the computer's signature does not match");

  let peer: CryptoKey;
  try {
    peer = await crypto.subtle.importKey('raw', bs(fromB64(reply.eph)), { name: 'ECDH', namedCurve: 'P-256' }, false, []);
  } catch {
    throw new Error('private relay: bad ephemeral key');
  }
  const shared = new Uint8Array(await crypto.subtle.deriveBits({ name: 'ECDH', public: peer }, eph.privateKey, 256));
  return deriveRecordKeys(shared, helloBytes, replyBytes);
}
