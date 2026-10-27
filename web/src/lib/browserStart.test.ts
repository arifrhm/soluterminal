import { describe, expect, it } from 'vitest';
import type { Port } from '../types';
import { defaultBrowserUrl } from './browserStart';

function port(n: number, paneId?: string): Port {
  return { port: n, addr: '127.0.0.1', pid: 1, process: 'node', cmdline: '', cwd: '', paneId, firstAt: '', proto: 'tcp' };
}

describe('defaultBrowserUrl', () => {
  it('opens the service chooser instead of guessing from a numeric port', () => {
    expect(defaultBrowserUrl([port(9000), port(5173, 'p2'), port(3000, 'p1')])).toBe('');
  });

  it('falls back to the start page when no pane owns a port', () => {
    expect(defaultBrowserUrl([port(9000)])).toBe('');
    expect(defaultBrowserUrl([])).toBe('');
  });
});
