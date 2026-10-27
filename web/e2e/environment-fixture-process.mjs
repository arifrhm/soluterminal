// The Go command is a launcher, not the HTTP fixture process. Keep a live
// guardian as the group owner even if that launcher exits first.
import { execFile, spawn } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const filename = fileURLToPath(import.meta.url);
const guardianArg = '--environment-fixture-guardian';

export function startEnvironmentFixture({ cwd, env }) {
  const ownerFile = path.join(env.SOLU_ENVIRONMENT_E2E_DATA, '.fixture-owner');
  const ownerToken = randomUUID();
  fs.writeFileSync(ownerFile, ownerToken, { flag: 'wx', mode: 0o600 });
  const child = spawn(process.execPath, [filename, guardianArg], {
    cwd,
    env: { ...env, SOLU_ENVIRONMENT_E2E_OWNER: ownerToken },
    detached: process.platform !== 'win32',
    stdio: ['ignore', 'pipe', 'pipe', 'ipc'],
  });
  let closed = false;
  let stopping;
  let cleanupConfirmed = false;
  let spawnError;
  const close = new Promise((resolve) => {
    child.once('error', (error) => { spawnError = error; });
    child.once('close', (code, signal) => {
      closed = true;
      resolve({ code, signal });
    });
  });
  child.on('message', (message) => {
    if (message?.type === 'cleanup-armed') cleanupConfirmed = true;
  });
  return {
    child,
    stop() {
      // Teardown can run both after failed startup and in afterAll.
      stopping ??= (async () => {
        // Revoke this exact private lease even if the guardian failed. It lets
        // the real Go fixture exit without signalling an unowned/recycled PID.
        try {
          if (fs.readFileSync(ownerFile, 'utf8') !== ownerToken) throw new Error('environment fixture owner lease changed');
          fs.unlinkSync(ownerFile);
        } catch (error) {
          if (error.code !== 'ENOENT') throw error;
        }
        if (closed) {
          if (spawnError) return;
          throw new Error('environment fixture guardian exited before cleanup; ownership lost');
        }
        if (!child.connected) throw new Error('environment fixture guardian IPC lost before cleanup');
        await new Promise((resolve, reject) => child.send({ type: 'stop' }, (error) => error ? reject(error) : resolve()));
        let timeout;
        try {
          if (process.platform === 'win32') {
            // Run taskkill outside the tree it terminates, so its actual exit
            // status can confirm success. The guardian stays alive to pin PID.
            await new Promise((resolve) => setTimeout(resolve, 1_000));
            if (closed) throw new Error('environment fixture guardian exited before tree cleanup');
            await new Promise((resolve, reject) => {
              execFile('taskkill', ['/PID', String(child.pid), '/T', '/F'], { timeout: 3_000, windowsHide: true }, (error) => error ? reject(error) : resolve());
            });
          }
          const result = await Promise.race([
            close,
            new Promise((_, reject) => {
              timeout = setTimeout(() => reject(new Error('environment fixture cleanup timed out; data retained')), 6_000);
            }),
          ]);
          if (!cleanupConfirmed || (process.platform !== 'win32' && result.signal !== 'SIGKILL')) {
            throw new Error(`environment fixture cleanup was not confirmed (${result.code ?? result.signal})`);
          }
        } finally {
          clearTimeout(timeout);
        }
      })();
      return stopping;
    },
  };
}

if (process.argv[2] === guardianArg) {
  // Only this detached guardian sends group signals. Its own live PID pins the
  // PGID until the final atomic kill, so a dead launcher's PID is never reused.
  const ownerFile = path.join(process.env.SOLU_ENVIRONMENT_E2E_DATA, '.fixture-owner');
  let stopping = false;
  process.on('SIGTERM', () => {});
  process.on('SIGINT', () => {});
  const report = (message) => {
    if (process.connected) process.send(message, () => {});
  };
  const stop = () => {
    if (stopping) return;
    stopping = true;
    try {
      if (fs.readFileSync(ownerFile, 'utf8') !== process.env.SOLU_ENVIRONMENT_E2E_OWNER) {
        throw new Error('environment fixture owner lease changed');
      }
      fs.unlinkSync(ownerFile);
    } catch (error) {
      if (error.code !== 'ENOENT') process.stderr.write(`${error.stack}\n`);
    }
    report({ type: 'cleanup-armed' });
    // Closing the lease lets the real fixture return and Go reap it normally.
    // The group owner remains alive throughout the TERM grace period, even
    // when the launcher has failed or a descendant ignores termination.
    setTimeout(() => {
      if (process.platform === 'win32') {
        if (process.connected) return; // The live owner confirms taskkill.
        const killer = spawn('taskkill', ['/PID', String(process.pid), '/T', '/F'], { stdio: 'inherit', windowsHide: true });
        killer.once('error', (error) => { process.stderr.write(`${error.stack}\n`); process.exit(1); });
        killer.once('exit', (code) => {
          // Successful taskkill terminates this guardian too. Reaching this
          // path with a failure must never masquerade as confirmed cleanup.
          if (code !== 0) process.exit(1);
        });
      } else {
        process.kill(-process.pid, 'SIGTERM');
        setTimeout(() => process.kill(-process.pid, 'SIGKILL'), 1_000);
      }
    }, 1_000);
  };
  process.on('message', (message) => { if (message?.type === 'stop') stop(); });
  process.on('disconnect', stop);
  const driver = spawn(process.env.SOLU_GO_BINARY || 'go', ['test', '-tags=workflow_engine', '-run', '^TestWorkflowEnvironmentE2EServer$', '-count=1', '-v'], {
    cwd: process.cwd(), env: process.env, stdio: ['ignore', 'pipe', 'pipe'],
  });
  driver.stdout.on('data', (chunk) => report({ type: 'driver-output', text: chunk.toString() }));
  driver.stderr.on('data', (chunk) => report({ type: 'driver-output', text: chunk.toString() }));
  driver.once('error', (error) => report({ type: 'driver-error', error: error.message }));
  driver.once('exit', (code, signal) => report({ type: 'driver-exit', code, signal }));
}
