// @vitest-environment node

import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, describe, expect, test, vi } from 'vitest';
import { ensureBrowserCanStart, probeNetworkSandbox } from './browser-preflight.mjs';

const originalPreflight = process.env.SOLU_BROWSER_PREFLIGHT;

afterEach(() => {
  if (originalPreflight === undefined) delete process.env.SOLU_BROWSER_PREFLIGHT;
  else process.env.SOLU_BROWSER_PREFLIGHT = originalPreflight;
  vi.restoreAllMocks();
});

const fail = (code) => async () => { throw Object.assign(new Error(code), { code }); };
const ok = async () => {};

describe('browser network sandbox preflight', () => {
  test('only EPERM socket denials classify the sandbox as blocked', async () => {
    const tcp = await probeNetworkSandbox({ listenTcp: fail('EPERM'), listenUnix: ok, readStatus: () => '2' });
    expect(tcp).toMatchObject({ blocked: true, tcp: 'EPERM', unix: 'ok', seccomp: '2' });
    expect(tcp.reason).toContain('socket(AF_INET)');

    const unix = await probeNetworkSandbox({ listenTcp: ok, listenUnix: fail('EPERM'), readStatus: () => null });
    expect(unix.blocked).toBe(true);
    expect(unix.reason).toContain('socket(AF_UNIX)');

    for (const code of ['EACCES', 'EADDRINUSE', 'EADDRNOTAVAIL', 'ENOENT']) {
      expect((await probeNetworkSandbox({ listenTcp: fail(code), listenUnix: ok })).blocked).toBe(false);
    }
    expect((await probeNetworkSandbox({ listenTcp: ok, listenUnix: ok })).blocked).toBe(false);
  });

  test('ensureBrowserCanStart exits 79 with the actionable fatal signature', async () => {
    const written = [];
    vi.spyOn(fs, 'writeSync').mockImplementation((_fd, value) => { written.push(String(value)); return String(value).length; });
    const net = (await import('node:net')).default;
    vi.spyOn(net, 'createServer').mockImplementation(() => ({
      once(_event, reject) { this.reject = reject; return this; },
      listen() { this.reject(Object.assign(new Error('blocked'), { code: 'EPERM' })); },
      close(cb) { cb(); },
    }));
    const exits = [];
    await ensureBrowserCanStart({ exit: (code) => exits.push(code), stderr: 2 });
    expect(exits).toEqual([79]);
    expect(written.join('')).toContain('sandbox_host_linux.cc:41');
    expect(written.join('')).toContain('do not retry');
  });

  test('real probes do not leave their unix socket behind', async () => {
    const socket = path.join(os.tmpdir(), `solu-preflight-${process.pid}.sock`);
    try { fs.unlinkSync(socket); } catch {}
    const result = await probeNetworkSandbox();
    if (result.blocked) {
      expect([result.tcp, result.unix]).toContain('EPERM');
      expect(result.seccomp).not.toBeNull();
    } else {
      expect(result).toMatchObject({ blocked: false });
    }
    expect(fs.existsSync(socket)).toBe(false);
  });
});
