// @vitest-environment node
import { describe, expect, it } from 'vitest';
import { bs, fromB64, fromHex, hex, utf8 } from './bytes';
import {
  clientHandshake,
  deriveRecordKeys,
  encodeHello,
  helloSigMessage,
  PrivateRelayIdentityError,
  replySigMessage,
  type HandshakeOptions,
} from './handshake';
import { openRecord, recordNonce, sealRecord } from './records';
import vector from './testdata/handshake-vector.json';

// testdata/handshake-vector.json comes from the daemon's own functions
// (daemon/private_relay_vector_test.go), for fixed keys.

const latin1 = (s: string) => Uint8Array.from(s, (c) => c.charCodeAt(0));

async function ecdhPair(jwk: typeof vector.clientEphJwk): Promise<CryptoKeyPair> {
  const privateKey = await crypto.subtle.importKey('jwk', { ...jwk, ext: true }, { name: 'ECDH', namedCurve: 'P-256' }, false, ['deriveBits']);
  const { d: _d, ...pub } = jwk;
  const publicKey = await crypto.subtle.importKey('jwk', { ...pub, ext: true }, { name: 'ECDH', namedCurve: 'P-256' }, true, []);
  return { privateKey, publicKey };
}

async function deviceSigner(): Promise<(m: string) => Promise<string>> {
  const key = await crypto.subtle.importKey('jwk', { ...vector.deviceJwk, ext: true }, { name: 'ECDSA', namedCurve: 'P-256' }, false, ['sign']);
  return async (m) => {
    const sig = new Uint8Array(await crypto.subtle.sign({ name: 'ECDSA', hash: 'SHA-256' }, key, bs(utf8.encode(m))));
    let s = '';
    for (const b of sig) s += String.fromCharCode(b);
    return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
  };
}

const recordedHelloSig = (JSON.parse(vector.helloRaw) as { sig: string }).sig;

async function vectorOptions(over: Partial<HandshakeOptions> = {}): Promise<HandshakeOptions> {
  return {
    deviceId: vector.deviceId,
    target: vector.target,
    // The daemon's ECDSA signatures are randomised; replaying the recorded
    // one makes the hello bytes comparable.
    sign: async (m) => {
      expect(m).toBe(vector.helloSigMessage);
      return recordedHelloSig;
    },
    pinnedIdentity: vector.identitySpki,
    now: () => Number(vector.time) * 1000 + 999,
    ephemeral: await ecdhPair(vector.clientEphJwk),
    nonce: fromB64(vector.nonce),
    ...over,
  };
}

function io(reply: string) {
  const sent: string[] = [];
  return { sent, sendText: (t: string) => void sent.push(t), nextText: async () => reply };
}

describe('private relay handshake', () => {
  it('sends exactly the hello bytes the daemon expects', async () => {
    const t = io(vector.replyRaw);
    await clientHandshake(t, await vectorOptions());
    expect(t.sent).toEqual([vector.helloRaw]);
    expect(helloSigMessage(vector.target, vector.time, vector.nonce, vector.clientEph)).toBe(vector.helloSigMessage);
    expect(await replySigMessage(utf8.encode(vector.helloRaw), JSON.parse(vector.replyRaw).eph, vector.replyNonce, vector.identitySpki))
      .toBe(vector.replySigMessage);
    expect(encodeHello(JSON.parse(vector.helloRaw))).toBe(vector.helloRaw);
  });

  it('signs the hello with the device key in the form the daemon verifies', async () => {
    const sign = await deviceSigner();
    const t = io(vector.replyRaw);
    await expect(clientHandshake(t, await vectorOptions({ sign }))).rejects.toThrow(); // reply is bound to the recorded hello
    const hello = JSON.parse(t.sent[0]) as { sig: string };
    const { d: _d, ...pub } = vector.deviceJwk;
    const key = await crypto.subtle.importKey('jwk', { ...pub, ext: true }, { name: 'ECDSA', namedCurve: 'P-256' }, false, ['verify']);
    const ok = await crypto.subtle.verify({ name: 'ECDSA', hash: 'SHA-256' }, key, bs(fromB64(hello.sig)), bs(utf8.encode(vector.helloSigMessage)));
    expect(ok).toBe(true);
    expect(fromB64(hello.sig)).toHaveLength(64);
  });

  it('derives the daemon’s keys from the exact handshake bytes', async () => {
    const keys = await deriveRecordKeys(fromHex(vector.shared), utf8.encode(vector.helloRaw), utf8.encode(vector.replyRaw), true);
    expect(hex(new Uint8Array(await crypto.subtle.exportKey('raw', keys.c2s)))).toBe(vector.c2sKey);
    expect(hex(new Uint8Array(await crypto.subtle.exportKey('raw', keys.s2c)))).toBe(vector.s2cKey);

    const hs = await clientHandshake(io(vector.replyRaw), await vectorOptions());
    for (const r of vector.c2s) {
      expect(hex(await sealRecord(hs.c2s, BigInt(r.seq), latin1(r.plain)))).toBe(r.sealed);
    }
    for (const r of vector.s2c) {
      expect(await openRecord(hs.s2c, BigInt(r.seq), fromHex(r.sealed))).toEqual(latin1(r.plain));
    }
  });

  it('ends up with other keys when either message differs by a byte', async () => {
    const reply = vector.replyRaw.replace('{', '{ ');
    // Same fields, other bytes: the signature still verifies (it covers the
    // fields), but the transcript salt and so the keys differ.
    const hs = await clientHandshake(io(reply), await vectorOptions());
    await expect(openRecord(hs.s2c, 0n, fromHex(vector.s2c[0].sealed))).rejects.toThrow(/authentication/);
  });

  it('refuses a computer other than the one pinned', async () => {
    await expect(clientHandshake(io(vector.replyRaw), await vectorOptions({ pinnedIdentity: vector.deviceSpki })))
      .rejects.toBeInstanceOf(PrivateRelayIdentityError);
  });

  it('refuses a reply whose signature does not cover what was received', async () => {
    const r = JSON.parse(vector.replyRaw) as Record<string, string>;
    const otherEph = vector.clientEph; // a valid point the identity never signed
    await expect(clientHandshake(io(JSON.stringify({ ...r, eph: otherEph })), await vectorOptions()))
      .rejects.toThrow(/signature/);
    await expect(clientHandshake(io(JSON.stringify({ ...r, nonce: 'AAAA' })), await vectorOptions()))
      .rejects.toThrow(/signature/);
    const sig = fromB64(r.sig);
    sig[10] ^= 1;
    let s = '';
    for (const b of sig) s += String.fromCharCode(b);
    const tampered = btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    await expect(clientHandshake(io(JSON.stringify({ ...r, sig: tampered })), await vectorOptions()))
      .rejects.toThrow(/signature/);
    // A hello other than the one the reply was signed over.
    await expect(clientHandshake(io(vector.replyRaw), await vectorOptions({ now: () => 1_700_000_000_000, sign: async () => recordedHelloSig })))
      .rejects.toThrow(/signature/);
  });

  it('refuses malformed replies', async () => {
    await expect(clientHandshake(io('not json'), await vectorOptions())).rejects.toThrow(/bad reply/);
    await expect(clientHandshake(io('{"eph":1}'), await vectorOptions())).rejects.toThrow(/bad reply/);
  });
});

describe('private relay records', () => {
  it('uses 4 zero bytes and a big-endian counter as the nonce', () => {
    expect(hex(recordNonce(0n))).toBe('000000000000000000000000');
    expect(hex(recordNonce(0x0102030405060708n))).toBe('000000000102030405060708');
  });

  it('rejects tampered, replayed and reflected records', async () => {
    const k = await deriveRecordKeys(new Uint8Array(32), utf8.encode('hello'), utf8.encode('reply'));
    const sealed = await sealRecord(k.c2s, 0n, utf8.encode('rm -rf /'));
    expect(new TextDecoder().decode(await openRecord(k.c2s, 0n, sealed))).toBe('rm -rf /');
    const tampered = sealed.slice();
    tampered[3] ^= 1;
    await expect(openRecord(k.c2s, 0n, tampered)).rejects.toThrow(/authentication/);
    await expect(openRecord(k.c2s, 1n, sealed)).rejects.toThrow(/authentication/);
    await expect(openRecord(k.s2c, 0n, sealed)).rejects.toThrow(/authentication/);
  });
});
