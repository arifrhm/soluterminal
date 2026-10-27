// AES-256-GCM records in counter order (daemon privateRecords). Each
// direction has its own key; the nonce is 4 zero bytes and a 64-bit
// big-endian record count, the AAD the protocol version. An altered,
// replayed, reordered or reflected record fails to open.

import { bs, utf8 } from './bytes';
import { PRIVATE_RELAY_VERSION } from './handshake';

const AAD = utf8.encode(PRIVATE_RELAY_VERSION);
const MAX_SEQ = (1n << 64n) - 1n;

export function recordNonce(seq: bigint): Uint8Array {
  const n = new Uint8Array(12);
  new DataView(n.buffer).setBigUint64(4, seq, false);
  return n;
}

export async function sealRecord(key: CryptoKey, seq: bigint, plain: Uint8Array): Promise<Uint8Array> {
  return new Uint8Array(await crypto.subtle.encrypt(
    { name: 'AES-GCM', iv: bs(recordNonce(seq)), additionalData: bs(AAD), tagLength: 128 }, key, bs(plain),
  ));
}

export async function openRecord(key: CryptoKey, seq: bigint, sealed: Uint8Array): Promise<Uint8Array> {
  try {
    return new Uint8Array(await crypto.subtle.decrypt(
      { name: 'AES-GCM', iv: bs(recordNonce(seq)), additionalData: bs(AAD), tagLength: 128 }, key, bs(sealed),
    ));
  } catch {
    throw new Error('private relay: record failed authentication');
  }
}

/**
 * One direction's counter. next() hands out sequence numbers synchronously,
 * so callers can encrypt concurrently and still put records on the wire in
 * counter order.
 */
export class RecordCounter {
  private seq = 0n;

  next(): bigint {
    if (this.seq === MAX_SEQ) throw new Error('private relay: record counter exhausted');
    return this.seq++;
  }
}
