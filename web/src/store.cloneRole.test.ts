import { describe, expect, it } from 'vitest';
import type { PaneInfo } from './types';
import { cloneSpawnOpts } from './store';

function pane(role?: PaneInfo['role']): PaneInfo {
  return {
    id: 'pane-1',
    cwd: '/original',
    cmd: ['claude'],
    title: 'Orchestrator',
    status: 'running',
    createdAt: '2026-08-22T00:00:00Z',
    cols: 80,
    rows: 24,
    pid: 123,
    lastActivityMs: 0,
    state: 'idle',
    hasClaude: true,
    ...(role ? { role } : {}),
  };
}

describe('cloneSpawnOpts role fidelity', () => {
  it('preserves an orchestrator role with cwd, cmd, and title', () => {
    expect(cloneSpawnOpts(pane('orchestrator'), '/live/cwd')).toEqual({
      cwd: '/live/cwd',
      cmd: ['claude'],
      title: 'Orchestrator',
      role: 'orchestrator',
    });
  });

  it('does not add a role key for a plain pane', () => {
    const opts = cloneSpawnOpts(pane(), '/live/cwd');
    expect(opts).toEqual({ cwd: '/live/cwd', cmd: ['claude'], title: 'Orchestrator' });
    expect(opts).not.toHaveProperty('role');
  });

  it('preserves Codex identity and role together', () => {
    const codex = { ...pane('orchestrator'), agent: 'codex' as const, cmd: ['codex'] };
    expect(cloneSpawnOpts(codex, '/live/cwd')).toEqual({
      cwd: '/live/cwd', cmd: ['codex'], title: 'Orchestrator', agent: 'codex', role: 'orchestrator',
    });
  });

  it('forwards a Muse identity without a role', () => {
    const muse = { ...pane(), agent: 'muse' as const, cmd: ['muse'] };
    const opts = cloneSpawnOpts(muse, '/live/cwd');
    expect(opts).toEqual({ cwd: '/live/cwd', cmd: ['muse'], title: 'Orchestrator', agent: 'muse' });
    expect(opts).not.toHaveProperty('role');
  });
});
