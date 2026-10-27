import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { computeDistDigest, emitWebdistProvenance } from '../scripts/emit-webdist-provenance.mjs';

const roots = [];

afterEach(() => {
  for (const root of roots.splice(0)) rmSync(root, { recursive: true, force: true });
});

function fixture() {
  const root = mkdtempSync(path.join(tmpdir(), 'solu-webdist-'));
  roots.push(root);
  mkdirSync(path.join(root, 'assets'));
  writeFileSync(path.join(root, 'index.html'), '<!doctype html>\n');
  writeFileSync(path.join(root, 'assets', 'app.js'), 'console.log("fixture")\n');
  return root;
}

describe('webdist provenance emission', () => {
  it('is byte-identical for the same dist and source', () => {
    const root = fixture();
    emitWebdistProvenance({ root, version: '9.9.9', sha: '0123456789abcdef' });
    const first = readFileSync(path.join(root, 'webdist.json'));
    emitWebdistProvenance({ root, version: '9.9.9', sha: '0123456789abcdef' });
    const second = readFileSync(path.join(root, 'webdist.json'));
    expect(second).toEqual(first);
  });

  it('changes the digest when any bundled asset byte changes', () => {
    const root = fixture();
    const before = emitWebdistProvenance({ root, version: '9.9.9', sha: '0123456789abcdef' });
    writeFileSync(path.join(root, 'assets', 'app.js'), 'console.log("mutated")\n');
    const after = computeDistDigest(root);
    expect(after).not.toBe(before.digest);
  });
});
