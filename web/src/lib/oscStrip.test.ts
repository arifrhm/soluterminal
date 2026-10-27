import { describe, expect, it } from 'vitest';
import { newOscStripState, stripOscColorQueries } from './oscStrip';

const enc = (s: string) => new TextEncoder().encode(s);
const dec = (b: Uint8Array) => new TextDecoder().decode(b);

const stripAll = (input: string | Uint8Array) => {
  const bytes = typeof input === 'string' ? enc(input) : input;
  return dec(stripOscColorQueries(bytes, newOscStripState()));
};

describe('stripOscColorQueries', () => {
  it('drops OSC 11 background query (ST-terminated)', () => {
    expect(stripAll('before\x1b]11;?\x1b\\after')).toBe('beforeafter');
  });

  it('drops OSC 11 background query (BEL-terminated)', () => {
    expect(stripAll('before\x1b]11;?\x07after')).toBe('beforeafter');
  });

  it('drops OSC 10 foreground query', () => {
    expect(stripAll('x\x1b]10;?\x1b\\y')).toBe('xy');
  });

  it('drops OSC 12 cursor-color query', () => {
    expect(stripAll('x\x1b]12;?\x1b\\y')).toBe('xy');
  });

  it('drops OSC 4 palette query', () => {
    expect(stripAll('x\x1b]4;1;?\x1b\\y')).toBe('xy');
  });

  it('keeps OSC 11 SET command (rgb: form)', () => {
    const set = '\x1b]11;rgb:0000/0000/0000\x1b\\';
    expect(stripAll(`a${set}b`)).toBe(`a${set}b`);
  });

  it('keeps OSC 0 title set (BEL-terminated)', () => {
    const title = '\x1b]0;solu\x07';
    expect(stripAll(`x${title}y`)).toBe(`x${title}y`);
  });

  it('drops OSC 52 clipboard write (BEL-terminated)', () => {
    const clip = '\x1b]52;c;aGVsbG8=\x07';
    expect(stripAll(`x${clip}y`)).toBe('xy');
  });

  it('drops OSC 52 clipboard write (ST-terminated)', () => {
    const clip = '\x1b]52;c;aGVsbG8=\x1b\\';
    expect(stripAll(`x${clip}y`)).toBe('xy');
  });

  it('drops OSC 52 with primary-selection target', () => {
    // OSC 52 supports clipboards beyond `c` (e.g. `p` for primary).
    const clip = '\x1b]52;p;d29ybGQ=\x07';
    expect(stripAll(`a${clip}b`)).toBe('ab');
  });

  it('drops OSC 52 with a large base64 payload', () => {
    const payload = 'A'.repeat(1024);
    const clip = `\x1b]52;c;${payload}\x07`;
    expect(stripAll(`pre${clip}post`)).toBe('prepost');
  });

  it('keeps CSI sequences untouched', () => {
    const csi = '\x1b[31mred\x1b[0m';
    expect(stripAll(csi)).toBe(csi);
  });

  it('drops multiple back-to-back queries', () => {
    expect(stripAll('a\x1b]10;?\x1b\\\x1b]11;?\x1b\\b')).toBe('ab');
  });

  it('handles a sequence split across chunks', () => {
    // Chunk boundaries every which way.
    const s = newOscStripState();
    const out: number[] = [];
    const push = (chunk: string) => {
      const cleaned = stripOscColorQueries(enc(chunk), s);
      for (const b of cleaned) out.push(b);
    };
    push('before\x1b'); // ESC at boundary
    push(']11;?'); // body across boundary
    push('\x1b\\after');
    expect(dec(new Uint8Array(out))).toBe('beforeafter');
  });

  it('handles split between ESC and the closing backslash of ST', () => {
    const s = newOscStripState();
    const a = stripOscColorQueries(enc('x\x1b]11;?\x1b'), s);
    const b = stripOscColorQueries(enc('\\y'), s);
    expect(dec(a) + dec(b)).toBe('xy');
  });

  it('does not drop OSC numbers other than 4/10/11 even with ?', () => {
    // Some OSCs carry literal `?` data. Only 4/10/11 with `?` are
    // queries we strip; OSC 7 / 8 / 133 etc. pass through.
    const s = '\x1b]7;file://host/?q=1\x07';
    expect(stripAll(`a${s}b`)).toBe(`a${s}b`);
  });

  it('does not drop OSC 4 SET (no ?)', () => {
    const set = '\x1b]4;1;rgb:ff00/ff00/ff00\x1b\\';
    expect(stripAll(`x${set}y`)).toBe(`x${set}y`);
  });

  it('emits unbuffered ESC + non-] verbatim', () => {
    // ESC followed by 'c' (RIS reset) — not an OSC introducer; pass through.
    expect(stripAll('a\x1bcb')).toBe('a\x1bcb');
  });

  it('survives an unterminated OSC by re-emitting buffered bytes', () => {
    // Pathological: ESC ] body but no terminator before stream ends.
    // We don't drop the bytes — they get held in the stripper state
    // pending more input. A subsequent terminator-bearing chunk
    // completes the decision.
    const s = newOscStripState();
    const a = stripOscColorQueries(enc('keep\x1b]11;?'), s);
    const b = stripOscColorQueries(enc('\x1b\\done'), s);
    expect(dec(a) + dec(b)).toBe('keepdone');
  });

  // Regression: cross-call carry must not overflow the output buffer.
  // Before the fix, `out` was sized input.length; a chunk that re-emits
  // bytes carried in from the previous chunk (a swallowed ESC, or a
  // buffered non-query OSC body) exceeded that, and the over-length
  // writes were silently dropped (JS ignores OOB typed-array writes),
  // punching a hole in the replay. Each case asserts byte-for-byte
  // equality with the single-shot result.
  const splitByteEquality = (full: string, at: number) => {
    const bytes = enc(full);
    const oneShot = stripOscColorQueries(bytes, newOscStripState());
    const s = newOscStripState();
    const head = stripOscColorQueries(bytes.slice(0, at), s);
    const tail = stripOscColorQueries(bytes.slice(at), s);
    const joined = new Uint8Array(head.length + tail.length);
    joined.set(head, 0);
    joined.set(tail, head.length);
    return { oneShot: Array.from(oneShot), joined: Array.from(joined) };
  };

  it('does not drop bytes when a chunk ends on a bare ESC (CSI carry)', () => {
    const full = 'xy\x1b[31mz';
    const at = full.indexOf('\x1b') + 1; // split right after the ESC
    const { oneShot, joined } = splitByteEquality(full, at);
    expect(joined).toEqual(oneShot);
    expect(dec(new Uint8Array(joined))).toBe(full);
  });

  it('does not drop bytes when a non-query OSC body straddles a chunk', () => {
    const full = 'AAAA\x1b]0;a-title\x07BBBB';
    const at = full.indexOf('title'); // split inside the OSC body
    const { oneShot, joined } = splitByteEquality(full, at);
    expect(joined).toEqual(oneShot);
    expect(dec(new Uint8Array(joined))).toBe(full);
  });

  it('does not drop bytes across every possible split point of a mixed payload', () => {
    const full = 'pre\x1b]0;title\x07mid\x1b]11;?\x1b\\\x1b[1mbold\x1b[0mpost';
    const oneShot = Array.from(stripOscColorQueries(enc(full), newOscStripState()));
    const bytes = enc(full);
    for (let at = 1; at < bytes.length; at++) {
      const s = newOscStripState();
      const head = stripOscColorQueries(bytes.slice(0, at), s);
      const tail = stripOscColorQueries(bytes.slice(at), s);
      const joined = [...Array.from(head), ...Array.from(tail)];
      expect(joined, `split at ${at}`).toEqual(oneShot);
    }
  });

  it('keeps a long realistic xterm replay payload mostly intact', () => {
    // Chunk that mimics what a TUI session leaves in the ring buffer.
    const payload = [
      'normal text\r\n',
      '\x1b[2J\x1b[H', // clear+home
      '\x1b]0;my-title\x07', // title set
      '\x1b]11;?\x1b\\', // <-- background query (drop)
      '\x1b]10;rgb:e5e5/e9e9/f0f0\x1b\\', // fg set (keep)
      '\x1b[31merror\x1b[0m\r\n',
      '\x1b]4;0;?\x1b\\', // <-- palette query (drop)
    ].join('');
    const out = stripAll(payload);
    expect(out).not.toContain(']11;?');
    expect(out).not.toContain(']10;?');
    expect(out).not.toContain(']4;0;?');
    expect(out).toContain('rgb:e5e5/e9e9/f0f0');
    expect(out).toContain('my-title');
    expect(out).toContain('error');
  });
});
