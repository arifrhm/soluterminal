import { describe, expect, it } from 'vitest';
import { agentFromCommand, agentLabel, effectivePaneAgent } from './agentKind';

describe('agentKind helpers', () => {
  it('recognizes direct commands, paths, and Windows executables', () => {
    expect(agentFromCommand('codex --resume')).toBe('codex');
    expect(agentFromCommand(['/usr/local/bin/claude', '--resume'])).toBe('claude');
    expect(agentFromCommand(['C:\\tools\\codex.exe'])).toBe('codex');
    expect(agentFromCommand(['bash', '-lc', 'codex'])).toBeNull();
  });

  it('prefers explicit identity and preserves legacy native Claude panes', () => {
    expect(effectivePaneAgent({ agent: 'codex', cmd: ['claude'], kind: 'native' })).toBe('codex');
    expect(effectivePaneAgent({ agent: 'claude', cmd: ['bash'], codexThreadId: 'stale-thread' })).toBe('claude');
    expect(effectivePaneAgent({ cmd: [], kind: 'native' })).toBe('claude');
    expect(effectivePaneAgent({ cmd: ['bash'] })).toBeNull();
  });

  it('identifies a shell-launched Codex pane once its thread id is discovered', () => {
    expect(effectivePaneAgent({ cmd: ['bash'], codexThreadId: 'thread-123' })).toBe('codex');
  });

  it('lets an unambiguous Claude command beat a stale Codex thread id', () => {
    expect(effectivePaneAgent({ cmd: ['claude'], codexThreadId: 'stale' })).toBe('claude');
    expect(effectivePaneAgent({
      cmd: ['/bin/bash', '-lc', "init; 'claude' '--resume' 'session'; exec /bin/bash -i"],
      codexThreadId: 'stale',
    })).toBe('claude');
  });

  it('recognizes daemon login-shell wrappers without classifying shell prose', () => {
    expect(agentFromCommand(['/bin/bash', '-lc', "init; 'codex' 'resume' 'thread'; exec /bin/bash -i"])).toBe('codex');
    expect(agentFromCommand(['/bin/bash', '-lc', "printf 'codex docs'"])).toBeNull();
  });

  it('provides user-facing labels', () => {
    expect(agentLabel('claude')).toBe('Claude');
    expect(agentLabel('codex')).toBe('Codex');
    expect(agentLabel(null)).toBe('agent');
  });
});
