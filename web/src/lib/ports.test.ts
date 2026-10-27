import { describe, expect, it } from 'vitest';
import { bindSavedBrowser, portKey, portUrl, workspacePorts } from './ports';
import type { Port, Workspace } from '../types';
const port = {
  port: 5173,
  addr: '::',
  pid: 1,
  proto: 'tcp6',
  firstAt: 'now',
  process: 'node',
  cwd: '',
  cmdline: '',
  paneId: 'p',
  workspace: 'Saved copy',
  backend: 'wsl',
  scheme: 'https',
} as Port;
describe('service identity and workspace membership', () => {
  it('binds a saved workspace tile to the daemon it was fetched from without changing the saved value', () => {
    const browser = {
      id: 'b',
      url: 'http://127.0.0.1:5173',
      createdAt: 'now',
      service: { url: 'http://127.0.0.1:5173', machinePrefix: '' },
    };
    expect(bindSavedBrowser(browser, '/m/host').service?.machinePrefix).toBe('/m/host');
    expect(browser.service.machinePrefix).toBe('');
  });
  it('uses exact IPv6 and HTTPS addresses', () => {
    expect(portUrl(port)).toBe('https://[::1]:5173');
  });
  it('keeps backend and process lifetime in row identity', () => {
    expect(portKey(port)).not.toBe(portKey({ ...port, backend: 'win' }));
    expect(portKey(port)).not.toBe(portKey({ ...port, pid: 2 }));
  });
  it('trusts current workspace membership even when the same pane is in a saved snapshot', () => {
    const workspace = { name: 'Current', paneIds: ['p'], backend: 'wsl' } as Workspace;
    expect(workspacePorts([port], 'Current', 'wsl', [workspace])).toEqual([port]);
    expect(workspacePorts([{ ...port, backend: 'win' }], 'Current', 'wsl', [workspace])).toEqual(
      [],
    );
  });
});
