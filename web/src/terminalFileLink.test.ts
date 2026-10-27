import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { __recomputeEmbedForTests, installEmbedBridge, openEmbedFile } from './embed';
import { openTerminalFileLink } from './terminalFileLink';

const origin = 'http://127.0.0.1:4942';
const active = {
  framed: true,
  search: '?embed=shinoque',
  saved: null,
  ancestorOrigin: origin,
  referrer: '',
};
let stop = () => {};
const initialize = (capabilities: unknown = ['open-file'], from = origin, source = window.parent) => {
  window.dispatchEvent(new MessageEvent('message', {
    origin: from,
    source,
    data: { source: 'shinoque', v: 1, kind: 'init', theme: 'dark', capabilities },
  }));
};

beforeEach(() => {
  __recomputeEmbedForTests(active);
  stop = installEmbedBridge(() => {});
});
afterEach(() => {
  stop();
  vi.restoreAllMocks();
  __recomputeEmbedForTests({ ...active, framed: false });
});

describe('terminal file links', () => {
  it('resolves the clicked pane path and preserves line and column for the opted-in host', () => {
    initialize();
    const post = vi.spyOn(window.parent, 'postMessage').mockImplementation(() => {});
    const resolve = vi.fn(() => '/workspace/src/文.ts');
    const local = vi.fn();
    openTerminalFileLink('./src/文.ts:42:7', resolve, local);
    expect(resolve).toHaveBeenCalledWith('./src/文.ts');
    expect(post).toHaveBeenCalledExactlyOnceWith({
      source: 'soluterminal', v: 1, kind: 'open-file',
      path: '/workspace/src/文.ts', line: 42, col: 7,
    }, origin);
    expect(local).not.toHaveBeenCalled();
  });

  it('keeps the preview fallback for old hosts, standalone windows and unresolved paths', () => {
    const post = vi.spyOn(window.parent, 'postMessage').mockImplementation(() => {});
    const local = vi.fn();
    window.dispatchEvent(new MessageEvent('message', {
      origin, source: window.parent,
      data: { source: 'shinoque', v: 1, kind: 'init', theme: 'dark' },
    }));
    openTerminalFileLink('/tmp/a.md:12', (path) => path, local);
    expect(local).toHaveBeenLastCalledWith('/tmp/a.md');
    initialize();
    __recomputeEmbedForTests({ ...active, framed: false });
    openTerminalFileLink('~/b.md:3:2', (path) => path, local);
    expect(local).toHaveBeenLastCalledWith('~/b.md');
    openTerminalFileLink('./missing', () => null, local);
    expect(local).toHaveBeenCalledTimes(2);
    expect(post).not.toHaveBeenCalled();
  });

  it('accepts home, absolute WSL and Windows paths without changing spaces or Unicode', () => {
    initialize();
    const post = vi.spyOn(window.parent, 'postMessage').mockImplementation(() => {});
    for (const path of ['~/文 notes.md', '/tmp/a b.txt', 'C:\\Users\\me\\文.txt']) {
      expect(openEmbedFile(path, 2)).toBe(true);
      expect(post).toHaveBeenLastCalledWith({
        source: 'soluterminal', v: 1, kind: 'open-file', path, line: 2,
      }, origin);
    }
  });

  it('requires exact-parent negotiation and clears capabilities on replacement or disposal', () => {
    const post = vi.spyOn(window.parent, 'postMessage').mockImplementation(() => {});
    initialize(['open-file'], 'https://evil.example');
    expect(openEmbedFile('/tmp/a')).toBe(false);
    initialize(['open-file'], origin, {} as Window);
    expect(openEmbedFile('/tmp/a')).toBe(false);
    initialize('open-file');
    expect(openEmbedFile('/tmp/a')).toBe(false);
    initialize();
    expect(openEmbedFile('/tmp/a')).toBe(true);
    initialize([]);
    expect(openEmbedFile('/tmp/a')).toBe(false);
    initialize();
    stop();
    expect(openEmbedFile('/tmp/a')).toBe(false);
    expect(post).toHaveBeenCalledTimes(1);
  });

  it('refuses malformed paths and coordinates without posting', () => {
    initialize();
    const post = vi.spyOn(window.parent, 'postMessage').mockImplementation(() => {});
    for (const path of ['', 'relative.md', 'https://example.com/a', '/tmp/a\nfile', '/tmp/\0a', `/${'x'.repeat(32768)}`]) {
      expect(openEmbedFile(path)).toBe(false);
    }
    for (const line of [0, -1, 1.5, Infinity, Number.MAX_SAFE_INTEGER + 1]) {
      expect(openEmbedFile('/tmp/a', line)).toBe(false);
      expect(openEmbedFile('/tmp/a', 1, line)).toBe(false);
    }
    expect(openEmbedFile('/tmp/a', undefined, 2)).toBe(false);
    expect(post).not.toHaveBeenCalled();
  });
});
