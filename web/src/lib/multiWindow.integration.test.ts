// Headless integration test for multi-monitor (Phase 1a): two windows
// against one shared localStorage.
//
// Same-origin windows share localStorage, and that's precisely what the
// per-window namespacing (windowScopedKey) and the ownership registry
// (workspaceOwnership) rely on. So flipping `window.__SOLU_WINDOW__`
// between operations — with localStorage as the shared backing store —
// faithfully models two real windows. This automates the LOGIC layer of
// the manual two-window test (everything except the actual OS window
// spawn + terminal rendering, which need a real GUI — see
// docs/multi-monitor-manual-test.md).

import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { windowScopedKey } from './windowIdentity';
import { claimWorkspace, checkOwnedByOther, OWNERS_KEY, OWNERSHIP_TTL_MS } from './workspaceOwnership';

// Mirrors store.ts's ACTIVE_WS_KEY (a module-private const there).
const ACTIVE_WS_KEY = 'solu.activeWorkspace';

function inWindow(label: string, role: 'primary' | 'secondary') {
  (window as unknown as { __SOLU_WINDOW__?: unknown }).__SOLU_WINDOW__ = { label, role };
}
// Mirrors the store subscribe (namespaced write) and bootstrap (namespaced read).
function persistActiveWorkspace(name: string) {
  localStorage.setItem(windowScopedKey(ACTIVE_WS_KEY), name);
}
function rememberedActiveWorkspace(): string | null {
  return localStorage.getItem(windowScopedKey(ACTIVE_WS_KEY));
}

beforeEach(() => localStorage.clear());
afterEach(() => {
  delete (window as unknown as { __SOLU_WINDOW__?: unknown }).__SOLU_WINDOW__;
  localStorage.clear();
});

describe('multi-window integration — two windows, shared localStorage', () => {
  it('keeps disjoint active-workspace storage and restores each window across a reload', () => {
    inWindow('main', 'primary');
    persistActiveWorkspace('Alpha');
    inWindow('workspace_2', 'secondary');
    persistActiveWorkspace('Beta');

    // Primary keeps the legacy un-suffixed key; secondary is namespaced.
    expect(localStorage.getItem('solu.activeWorkspace')).toBe('Alpha');
    expect(localStorage.getItem('solu.activeWorkspace.workspace_2')).toBe('Beta');

    // Simulate the auto-update reload: each window reads ITS OWN remembered
    // workspace (the durable label is re-injected before page script).
    inWindow('main', 'primary');
    expect(rememberedActiveWorkspace()).toBe('Alpha');
    inWindow('workspace_2', 'secondary');
    expect(rememberedActiveWorkspace()).toBe('Beta');
  });

  it('blocks a window from switching into a workspace a live other window owns', () => {
    const t = 1_000;
    inWindow('main', 'primary');
    claimWorkspace('Alpha', 'main', t);

    // Window B's loadWorkspace guard would see Alpha owned by main → deny.
    inWindow('workspace_2', 'secondary');
    expect(checkOwnedByOther('Alpha', 'workspace_2', t + 1)).toBe('main');

    // The owner itself is never blocked (restore-after-reload path).
    inWindow('main', 'primary');
    expect(checkOwnedByOther('Alpha', 'main', t + 1)).toBeNull();
  });

  it('frees a workspace when its owner switches away (a window owns at most one)', () => {
    const t = 1_000;
    inWindow('main', 'primary');
    claimWorkspace('Alpha', 'main', t);
    claimWorkspace('Gamma', 'main', t + 10);

    inWindow('workspace_2', 'secondary');
    expect(checkOwnedByOther('Alpha', 'workspace_2', t + 20)).toBeNull(); // Alpha released
    expect(checkOwnedByOther('Gamma', 'workspace_2', t + 20)).toBe('main'); // Gamma now held
  });

  it('reclaims a workspace whose owner went stale (crashed / closed window)', () => {
    const t = 1_000;
    inWindow('workspace_2', 'secondary');
    claimWorkspace('Alpha', 'workspace_2', t);

    inWindow('main', 'primary');
    // Within the TTL the live owner still blocks…
    expect(checkOwnedByOther('Alpha', 'main', t + OWNERSHIP_TTL_MS)).toBe('workspace_2');
    // …past the TTL (no heartbeat) it's reclaimable.
    expect(checkOwnedByOther('Alpha', 'main', t + OWNERSHIP_TTL_MS + 1)).toBeNull();
  });

  it('treats scratch (_current) as shared — never owned, never blocks either window', () => {
    const t = 1_000;
    inWindow('main', 'primary');
    claimWorkspace('_current', 'main', t);
    expect(JSON.parse(localStorage.getItem(OWNERS_KEY) || '{}')).toEqual({});

    inWindow('workspace_2', 'secondary');
    expect(checkOwnedByOther('_current', 'workspace_2', t + 1)).toBeNull();
  });
});
