import { afterEach, describe, expect, it, vi } from 'vitest';

const confirmDialog = vi.fn();
vi.mock('./lib/dialog', () => ({ confirmDialog: (...args: unknown[]) => confirmDialog(...args) }));

import { useStore } from './store';
import { canSetUpWslHere, requestWslSetup, wslSetupOutcomeMessage } from './wslSetup';

type W = { __SOLU_BACKEND__?: unknown; __SOLU_TAURI__?: boolean; __TAURI_INTERNALS__?: unknown };

function desktop(invoke: (cmd: string) => Promise<unknown>, backend: Record<string, unknown> = { hostPlatform: 'windows', launchMode: 'local', actual: 'host' }) {
  const w = window as unknown as W;
  w.__SOLU_BACKEND__ = backend;
  w.__SOLU_TAURI__ = true;
  w.__TAURI_INTERNALS__ = { invoke };
}

afterEach(() => {
  const w = window as unknown as W;
  delete w.__SOLU_BACKEND__;
  delete w.__SOLU_TAURI__;
  delete w.__TAURI_INTERNALS__;
  confirmDialog.mockReset();
  vi.restoreAllMocks();
});

describe('canSetUpWslHere', () => {
  it('is false in a browser tab and on non-Windows or cloud desktops', () => {
    expect(canSetUpWslHere()).toBe(false);
    desktop(async () => 'x', { hostPlatform: 'linux', launchMode: 'local' });
    expect(canSetUpWslHere()).toBe(false);
    desktop(async () => 'x', { hostPlatform: 'windows', launchMode: 'cloud' });
    expect(canSetUpWslHere()).toBe(false);
    desktop(async () => 'x');
    expect(canSetUpWslHere()).toBe(true);
  });
});

describe('requestWslSetup', () => {
  it('asks first and does nothing when the user says not now', async () => {
    const invoke = vi.fn(async () => 'switching');
    desktop(invoke);
    confirmDialog.mockResolvedValue(false);
    await requestWslSetup();
    expect(confirmDialog).toHaveBeenCalledOnce();
    expect(confirmDialog.mock.calls[0][0].message).toContain('open panes will close');
    expect(invoke).not.toHaveBeenCalled();
  });

  it('runs the desktop command after confirming and reports the outcome', async () => {
    const invoke = vi.fn(async () => 'restart-pc');
    desktop(invoke);
    confirmDialog.mockResolvedValue(true);
    const toast = vi.spyOn(useStore.getState(), 'toast');
    await requestWslSetup();
    expect(invoke).toHaveBeenCalledWith('desktop_set_up_wsl');
    expect(toast).toHaveBeenCalledWith(expect.stringContaining('Restart your PC'), expect.anything());
  });

  it('points older desktop apps at the tray', async () => {
    desktop(async () => { throw new Error('unknown command'); });
    confirmDialog.mockResolvedValue(true);
    const toast = vi.spyOn(useStore.getState(), 'toast');
    await requestWslSetup();
    expect(toast).toHaveBeenCalledWith(expect.stringContaining('Repair setup'), expect.anything());
  });
});

describe('wslSetupOutcomeMessage', () => {
  it('stays quiet for outcomes the user already saw', () => {
    expect(wslSetupOutcomeMessage('declined')).toBeNull();
    expect(wslSetupOutcomeMessage('failed')).toBeNull();
    expect(wslSetupOutcomeMessage('low-disk')).toContain('disk space');
    expect(wslSetupOutcomeMessage('busy')).toContain('already running');
  });
});
