import { describe, it, expect, afterEach } from 'vitest';
import { getWindowInfo, windowLabel, isPrimaryWindow, windowScopedKey } from './windowIdentity';

function setWin(v: unknown) {
  (window as unknown as { __SOLU_WINDOW__?: unknown }).__SOLU_WINDOW__ = v;
}

afterEach(() => {
  delete (window as unknown as { __SOLU_WINDOW__?: unknown }).__SOLU_WINDOW__;
});

describe('windowIdentity', () => {
  it('defaults to the primary "main" identity when the global is absent', () => {
    expect(getWindowInfo()).toEqual({ label: 'main', role: 'primary' });
    expect(windowLabel()).toBe('main');
    expect(isPrimaryWindow()).toBe(true);
  });

  it('reads an injected secondary identity', () => {
    setWin({ label: 'workspace_2', role: 'secondary' });
    expect(getWindowInfo()).toEqual({ label: 'workspace_2', role: 'secondary' });
    expect(windowLabel()).toBe('workspace_2');
    expect(isPrimaryWindow()).toBe(false);
  });

  it('treats the main label as primary even if role is missing', () => {
    setWin({ label: 'main' });
    expect(isPrimaryWindow()).toBe(true);
  });

  it('falls back to the default identity on a malformed global', () => {
    setWin({ label: 123 });
    expect(windowLabel()).toBe('main');
    setWin({ label: '' });
    expect(windowLabel()).toBe('main');
    setWin(null);
    expect(windowLabel()).toBe('main');
  });

  it('windowScopedKey: primary stays un-suffixed (backward compat)', () => {
    // No global → main
    expect(windowScopedKey('solu.activeWorkspace')).toBe('solu.activeWorkspace');
    setWin({ label: 'main', role: 'primary' });
    expect(windowScopedKey('solu.activeWorkspace')).toBe('solu.activeWorkspace');
  });

  it('windowScopedKey: secondary windows get a per-label suffix', () => {
    setWin({ label: 'workspace_3', role: 'secondary' });
    expect(windowScopedKey('solu.activeWorkspace')).toBe('solu.activeWorkspace.workspace_3');
  });
});
