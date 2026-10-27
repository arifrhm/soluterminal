// Tiny lifecycle proof: no Go build, daemon, browser, provider, or live APIs.
// Run explicitly with node --test; this is not a Playwright test file.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { startEnvironmentFixture } from './environment-fixture-process.mjs';

const waitUntil = async (predicate, description) => {
  // Generous deadlines: this proof spawns real subprocesses, and under the
  // verification lane's full-suite load a 4s poll deadline intermittently
  // expired before the fixture even announced readiness (the known web-tests
  // flake), which reported a timeout rather than a lifecycle defect.
  const deadline = Date.now() + 20_000;
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error(`timed out: ${description}`);
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
};
const executing = (pid) => {
  try {
    // A zombie has already exited; the outer Linux subreaper proof reaps it.
    if (process.platform === 'linux') return fs.readFileSync(`/proc/${pid}/stat`, 'utf8').split(') ')[1][0] !== 'Z';
    process.kill(pid, 0);
    return true;
  } catch (error) {
    if (error.code === 'ENOENT' || error.code === 'ESRCH') return false;
    throw error;
  }
};

for (const mode of ['normal', 'driver-exits-first', 'startup-failure', 'stubborn-child', 'owner-disconnect']) {
  test(`fixture cleanup: ${mode}`, { skip: process.platform === 'win32', timeout: 30_000 }, async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'solu-environment-lifecycle-proof-'));
    const go = path.join(dir, 'fake-go.cjs');
    fs.writeFileSync(go, `#!/usr/bin/env node
const fs = require('node:fs');
const path = require('node:path');
const mode = process.env.FIXTURE_PROOF_MODE;
if (mode === 'startup-failure') process.exit(7);
const child = require('node:child_process').spawn(process.execPath, ['-e', \`
const fs = require('node:fs');
const path = require('node:path');
const dir = process.env.SOLU_ENVIRONMENT_E2E_DATA;
fs.writeFileSync(path.join(dir, 'child.pid'), String(process.pid));
if (process.env.FIXTURE_PROOF_MODE === 'stubborn-child') {
  process.on('SIGTERM', () => {});
  setInterval(() => {}, 100);
} else {
  setInterval(() => {
    try {
      if (fs.readFileSync(path.join(dir, '.fixture-owner'), 'utf8') === process.env.SOLU_ENVIRONMENT_E2E_OWNER) return;
    } catch {}
    process.exit(0);
  }, 20);
}
\`], { stdio: ['ignore', 'inherit', 'inherit'] });
const ready = setInterval(() => {
  if (!fs.existsSync(path.join(process.env.SOLU_ENVIRONMENT_E2E_DATA, 'child.pid'))) return;
  clearInterval(ready);
  console.log('ENVIRONMENT_E2E_URL=http://127.0.0.1:1');
  if (mode === 'driver-exits-first' || mode === 'stubborn-child') process.exit(9);
}, 20);
child.on('exit', () => process.exit(0));
`, { mode: 0o700 });
    const fixture = startEnvironmentFixture({ cwd: dir, env: { ...process.env,
      SOLU_GO_BINARY: go, SOLU_ENVIRONMENT_E2E_DATA: dir, FIXTURE_PROOF_MODE: mode } });
    let output = '';
    const messages = [];
    fixture.child.stdout.on('data', (data) => { output += data; });
    fixture.child.stderr.on('data', (data) => { output += data; });
    fixture.child.on('message', (message) => {
      messages.push(message);
      if (message.type === 'driver-output') output += message.text;
    });
    let cleaned = false;
    try {
      if (mode === 'startup-failure') {
        await waitUntil(() => messages.some((m) => m.type === 'driver-exit' && m.code === 7), 'launcher failure');
      } else {
        await waitUntil(() => output.includes('ENVIRONMENT_E2E_URL='), 'fixture readiness');
      }
      const childFile = path.join(dir, 'child.pid');
      const descendant = fs.existsSync(childFile) ? Number(fs.readFileSync(childFile, 'utf8')) : undefined;
      if (mode === 'driver-exits-first' || mode === 'stubborn-child') {
        await waitUntil(() => messages.some((m) => m.type === 'driver-exit' && m.code === 9), 'driver exit before teardown');
        assert.equal(executing(descendant), true, 'descendant must still exist after driver exit');
      }
      if (mode === 'owner-disconnect') {
        fixture.child.disconnect();
        await waitUntil(() => fixture.child.signalCode === 'SIGKILL', 'disconnected guardian cleanup');
      } else {
        await fixture.stop();
        await fixture.stop(); // repeated afterAll cannot signal a recycled PGID
      }
      cleaned = true;
      assert.equal(executing(fixture.child.pid), false, 'guardian must be reaped');
      if (descendant) await waitUntil(() => !executing(descendant), 'descendant exit');
      assert.equal(fs.existsSync(path.join(dir, '.fixture-owner')), false);
      process.stdout.write(`FIXTURE_CLEANED ${JSON.stringify({ mode, guardian: fixture.child.pid, descendant })}\n`);
    } catch (error) {
      throw new Error(`${error.message}\n${output}\n${JSON.stringify(messages)}`, { cause: error });
    } finally {
      if (!cleaned && fixture.child.connected) await fixture.stop();
      if (cleaned) fs.rmSync(dir, { recursive: true, force: true });
    }
  });
}
