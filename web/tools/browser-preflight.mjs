#!/usr/bin/env node

import fs from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const blockedMessage = "Chromium cannot start inside this process's network-restricted seccomp sandbox (socket(AF_INET) -> EPERM; Seccomp: 2): chrome-headless-shell dies at startup with 'FATAL:content/browser/sandbox_host_linux.cc:41] Check failed: . shutdown: Operation not permitted (1)' (SIGTRAP) and on WSL every attempt is piped to %TEMP%\\wsl-crashes as a multi-GB dump. This is expected for Codex workspace-write workers: do not retry, do not LD_PRELOAD or --single-process around it, do not switch browsers. Screenshots and preview acceptance are produced by the workflow engine's verify stage outside the sandbox. See docs/preview-acceptance.md#chromium-cannot-start-inside-network-restricted-agent-sandboxes.";

function errorCode(error) {
  return error && typeof error === 'object' && typeof error.code === 'string' ? error.code : 'UNKNOWN';
}

async function defaultListenTcp() {
  const server = net.createServer();
  try {
    await new Promise((resolve, reject) => {
      server.once('error', reject);
      server.listen(0, '127.0.0.1', resolve);
    });
  } finally {
    await new Promise((resolve) => server.close(() => resolve())).catch(() => {});
  }
}

async function defaultListenUnix() {
  const socketPath = path.join(os.tmpdir(), `solu-preflight-${process.pid}.sock`);
  const server = net.createServer();
  try {
    await new Promise((resolve, reject) => {
      server.once('error', reject);
      server.listen(socketPath, resolve);
    });
  } finally {
    await new Promise((resolve) => server.close(() => resolve())).catch(() => {});
    try { fs.unlinkSync(socketPath); } catch (error) { if (error?.code !== 'ENOENT') throw error; }
  }
}

function defaultReadStatus() {
  const match = fs.readFileSync('/proc/self/status', 'utf8').match(/^Seccomp:\s*(\S+)/m);
  return match?.[1] ?? null;
}

export async function probeNetworkSandbox({
  listenTcp = defaultListenTcp,
  listenUnix = defaultListenUnix,
  readStatus = defaultReadStatus,
} = {}) {
  let tcp = 'ok';
  let unix = 'ok';
  let seccomp = null;
  try { await listenTcp(); } catch (error) { tcp = errorCode(error); }
  try { await listenUnix(); } catch (error) { unix = errorCode(error); }
  try { seccomp = await readStatus(); } catch { /* evidence only */ }
  const blocked = tcp === 'EPERM' || unix === 'EPERM';
  const denied = tcp === 'EPERM' ? 'socket(AF_INET)' : unix === 'EPERM' ? 'socket(AF_UNIX)' : '';
  return {
    blocked,
    reason: blocked ? `${denied} returned EPERM inside a network-restricted sandbox` : 'network socket probes were not denied by seccomp',
    tcp,
    unix,
    seccomp: seccomp == null ? null : String(seccomp),
  };
}

export function networkSandboxMessage(_result) {
  return blockedMessage;
}

export async function ensureBrowserCanStart({ exit = process.exit, stderr = process.stderr.fd } = {}) {
  if (process.env.SOLU_BROWSER_PREFLIGHT === 'skip') {
    fs.writeSync(stderr, 'browser-preflight: skipped by SOLU_BROWSER_PREFLIGHT=skip\n');
    return;
  }
  const result = await probeNetworkSandbox();
  if (!result.blocked) return;
  fs.writeSync(stderr, `${networkSandboxMessage(result)}\nbrowser-preflight: ${JSON.stringify(result)}\n`);
  exit(79);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const result = await probeNetworkSandbox();
  process.stdout.write(`${JSON.stringify(result)}\n`);
  if (result.blocked) {
    process.stderr.write(`${networkSandboxMessage(result)}\nbrowser-preflight: ${JSON.stringify(result)}\n`);
    process.exitCode = 79;
  }
}
