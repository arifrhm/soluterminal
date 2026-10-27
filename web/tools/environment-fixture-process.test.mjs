import { execFile } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { expect, it } from 'vitest';

// Keep lifecycle regressions in npm test, independently of the browser lane.
// The proof uses small fake launchers and never builds Go or starts a daemon.
it.skipIf(process.platform === 'win32')('closes environment fixtures after launcher, child and owner failures', async () => {
  const proof = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../e2e/environment-fixture-process.proof.mjs');
  const { stdout } = await promisify(execFile)(process.execPath, ['--test', '--test-reporter=tap', proof], {
    // Load-tolerant: the proof spawns five real subprocess lifecycles, and the
    // verification lane runs it beside a full suite. A 25s cap intermittently
    // killed it mid-run and surfaced as a bare "Command failed" with no
    // assertion detail; the lifecycle assertions themselves are unchanged.
    timeout: 120_000,
    maxBuffer: 1024 * 1024,
  });
  expect(stdout).toMatch(/# pass 5\b/);
  expect(stdout).toMatch(/# fail 0\b/);
  expect(stdout).toMatch(/# skipped 0\b/);
  for (const mode of ['normal', 'driver-exits-first', 'startup-failure', 'stubborn-child', 'owner-disconnect']) {
    expect(stdout).toContain(`"mode":"${mode}"`);
  }
}, 150_000);
