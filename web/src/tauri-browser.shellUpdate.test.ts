import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  applyShellUpdateNative,
  applyShellUpdateSilentNative,
  exitForUpdateNative,
  revealShellInstallerNative,
} from './tauri-browser';

function setInvoke(invoke: (cmd: string, args?: Record<string, unknown>) => Promise<unknown>) {
  Object.defineProperty(window, '__SOLU_TAURI__', { value: true, configurable: true });
  Object.defineProperty(window, '__TAURI_INTERNALS__', {
    value: { invoke },
    configurable: true,
  });
}

afterEach(() => {
  delete (window as unknown as { __SOLU_TAURI__?: unknown }).__SOLU_TAURI__;
  delete (window as unknown as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__;
});

describe('native shell update bridge', () => {
  it('accepts the legacy plain-string success returned by older shells', async () => {
    const invoke = vi.fn().mockResolvedValue('launched C:\\setup.exe');
    setInvoke(invoke);
    await expect(applyShellUpdateNative()).resolves.toEqual({ ok: true });
    expect(invoke).toHaveBeenCalledWith('apply_shell_update', { forceDowngrade: false });
  });

  it('preserves the staged path from a new unconfirmed launch outcome', async () => {
    const staged = 'C:\\Users\\x\\shell-update\\setup.exe';
    setInvoke(vi.fn().mockResolvedValue({ launched: false, path: staged, error: 'not observed' }));
    await expect(applyShellUpdateNative()).resolves.toEqual({
      ok: false,
      staged,
      error: 'not observed',
      downgradeRefused: false,
    });
  });

  it('passes force only when explicitly requested and preserves downgrade metadata', async () => {
    const invoke = vi.fn().mockResolvedValue({
      launched: false,
      path: 'C:\\setup.exe',
      error: 'refusing 0.0.932 to 0.0.916',
      downgradeRefused: true,
      installedVersion: '0.0.932',
      installerVersion: '0.0.916',
    });
    setInvoke(invoke);
    await expect(applyShellUpdateNative(true)).resolves.toEqual({
      ok: false,
      staged: 'C:\\setup.exe',
      error: 'refusing 0.0.932 to 0.0.916',
      downgradeRefused: true,
      installedVersion: '0.0.932',
      installerVersion: '0.0.916',
    });
    expect(invoke).toHaveBeenCalledWith('apply_shell_update', { forceDowngrade: true });
  });

  it('uses the native reveal command and degrades to false on older shells', async () => {
    const invoke = vi.fn().mockResolvedValue(undefined);
    setInvoke(invoke);
    await expect(revealShellInstallerNative('C:\\setup.exe')).resolves.toBe(true);
    expect(invoke).toHaveBeenCalledWith('reveal_shell_installer', { path: 'C:\\setup.exe' });

    setInvoke(vi.fn().mockRejectedValue(new Error('unknown command')));
    await expect(revealShellInstallerNative('C:\\setup.exe')).resolves.toBe(false);
  });

  it('feature-detects the new silent command without falling into the attended path', async () => {
    const invoke = vi.fn().mockRejectedValue(new Error('unknown command'));
    setInvoke(invoke);
    await expect(applyShellUpdateSilentNative()).resolves.toEqual({
      ok: false,
      error: 'Error: unknown command',
    });
    expect(invoke).toHaveBeenCalledWith('apply_shell_update_silent', { forceDowngrade: false });
    expect(invoke).not.toHaveBeenCalledWith('apply_shell_update');
  });

	it('preserves rollback-hold refusals on attended and silent native paths', async () => {
		const invoke = vi.fn().mockResolvedValue({
			launched: false,
			path: 'C:\\setup-2.0.0.exe',
			error: 'installer version 2.0.0 is held after rollback',
			updateHeld: true,
			installerVersion: '2.0.0',
		});
		setInvoke(invoke);
		const expected = {
			ok: false,
			staged: 'C:\\setup-2.0.0.exe',
			error: 'installer version 2.0.0 is held after rollback',
			downgradeRefused: false,
			updateHeld: true,
			installerVersion: '2.0.0',
		};
		await expect(applyShellUpdateNative()).resolves.toEqual(expected);
		await expect(applyShellUpdateSilentNative()).resolves.toEqual(expected);
		expect(invoke).toHaveBeenCalledWith('apply_shell_update', { forceDowngrade: false });
		expect(invoke).toHaveBeenCalledWith('apply_shell_update_silent', { forceDowngrade: false });
	});

  it('exits through the geometry-flushing update command when available', async () => {
    const invoke = vi.fn().mockResolvedValue(undefined);
    setInvoke(invoke);
    await expect(exitForUpdateNative()).resolves.toBe(true);
    expect(invoke).toHaveBeenCalledWith('exit_for_update');
  });
});
