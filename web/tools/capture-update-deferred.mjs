#!/usr/bin/env node

import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath, pathToFileURL } from 'node:url';

// Dedicated real-bundle evidence entry point for the lossless updater state.
// capture-screens owns the shared daemon fixture and browser setup.
const toolsDir = path.dirname(fileURLToPath(import.meta.url));
const defaultWebDir = path.resolve(toolsDir, '..');
const defaultRepoDir = path.resolve(defaultWebDir, '..');
const defaultCaptureScript = path.join(toolsDir, 'capture-screens.mjs');

function hasArg(argv, name) {
  return argv.includes(name);
}

export function applyUpdateDeferredCaptureDefaults(
  argv,
  { webDir = defaultWebDir, repoDir = defaultRepoDir } = {},
) {
  if (!hasArg(argv, '--dist')) argv.push('--dist', path.join(webDir, 'dist'));
  if (!hasArg(argv, '--out')) argv.push('--out', path.join(repoDir, 'docs', 'evidence', 'update-deferred'));
  if (!hasArg(argv, '--scenario')) argv.push('--scenario', 'update-deferred');
  return argv;
}

// One declared capture invocation proves determinism by launching the shared
// capture tool twice in fresh Node/browser processes. Keep this synchronous so
// the second pass cannot overlap the first pass's browser shutdown.
export function runUpdateDeferredCapture(
  argv,
  {
    spawn = spawnSync,
    runtime = process,
    webDir = defaultWebDir,
    repoDir = defaultRepoDir,
    captureScript = defaultCaptureScript,
  } = {},
) {
  const normalized = applyUpdateDeferredCaptureDefaults([...argv], { webDir, repoDir });
  const captureArgs = normalized.slice(2);
  for (let pass = 0; pass < 2; pass += 1) {
    const result = spawn(runtime.execPath, [captureScript, ...captureArgs], {
      cwd: webDir,
      env: runtime.env,
      stdio: 'inherit',
    });
    if (result.error) throw result.error;
    if (result.signal) {
      runtime.kill(runtime.pid, result.signal);
      return result;
    }
    if (result.status !== 0) {
      runtime.exitCode = Number.isInteger(result.status) ? result.status : 1;
      return result;
    }
  }
  return { status: 0, signal: null };
}

export function main() {
  return runUpdateDeferredCapture(process.argv);
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) {
  main();
}
