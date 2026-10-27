// @vitest-environment node
import { describe, expect, it } from 'vitest';
import { existsSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { createTempFixtures } from './temp-fixtures.mjs';

describe('temporary fixture ownership', () => {
  it('removes only its own fixtures after failed work and does not follow symlinks', () => {
    const outside = mkdtempSync(path.join(tmpdir(), 'solu-fixture-unowned-'));
    const fixtures = createTempFixtures();
    const roots = [];
    try {
      writeFileSync(path.join(outside, 'keep'), 'retained');
      for (let run = 0; run < 3; run++) {
        const dir = fixtures.mkdtempSync(path.join(tmpdir(), 'solu-fixture-owned-'));
        roots.push(dir);
        expect(() => {
          try {
            writeFileSync(path.join(dir, 'partial'), 'generated');
            symlinkSync(outside, path.join(dir, 'link'), 'junction');
            throw new Error('simulated failed assertion');
          } finally {
            fixtures.cleanup();
          }
        }).toThrow('simulated failed assertion');
      }
      expect(roots.every(dir => !existsSync(dir))).toBe(true);
      expect(existsSync(path.join(outside, 'keep'))).toBe(true);
      fixtures.dispose();
      fixtures.dispose();
    } finally {
      fixtures.dispose();
      rmSync(outside, { recursive: true, force: true });
    }
  });

  it.each([0, 1])('cleans fixtures on early worker exit %s', (code) => {
    const result = spawnSync(process.execPath, ['--input-type=module', '-e', `
      import { createTempFixtures } from ${JSON.stringify(new URL('./temp-fixtures.mjs', import.meta.url).href)};
      import { tmpdir } from 'node:os';
      import path from 'node:path';
      import { writeSync } from 'node:fs';
      const fixtures = createTempFixtures();
      const root = fixtures.mkdtempSync(path.join(tmpdir(), 'solu-fixture-exit-'));
      writeSync(1, root + '\\n');
      process.exit(${code});
    `], { encoding: 'utf8', timeout: 10000 });
    expect(result.status, result.stderr).toBe(code);
    const root = result.stdout.trim();
    expect(root).toContain('solu-fixture-exit-');
    expect(existsSync(root)).toBe(false);
  });
});
