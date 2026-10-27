import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ setPaneComposerState: vi.fn(() => Promise.resolve(new Response())) }));
vi.mock('../api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../api')>();
  return { ...real, api: { ...real.api, setPaneComposerState: mocks.setPaneComposerState } };
});

import { reportComposerPresence, resetComposerPresenceForTests } from './composerPresence';

beforeEach(() => {
  vi.useFakeTimers();
  mocks.setPaneComposerState.mockClear();
  resetComposerPresenceForTests();
});
afterEach(() => {
  resetComposerPresenceForTests();
  vi.useRealTimers();
});

describe('composer presence', () => {
  it('reports the first non-empty edge after the debounce', async () => {
    reportComposerPresence('p1', true);
    await vi.advanceTimersByTimeAsync(249);
    expect(mocks.setPaneComposerState).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(mocks.setPaneComposerState).toHaveBeenCalledWith('p1', true);
  });

  it('does not repost for keystrokes that stay non-empty', async () => {
    reportComposerPresence('p1', true);
    reportComposerPresence('p1', true);
    await vi.advanceTimersByTimeAsync(250);
    expect(mocks.setPaneComposerState).toHaveBeenCalledTimes(1);
  });

  it('reports clearing and collapses a rapid non-empty/empty pair', async () => {
    reportComposerPresence('p1', true);
    await vi.advanceTimersByTimeAsync(250);
    reportComposerPresence('p1', false);
    await vi.advanceTimersByTimeAsync(250);
    expect(mocks.setPaneComposerState).toHaveBeenLastCalledWith('p1', false);

    resetComposerPresenceForTests();
    mocks.setPaneComposerState.mockClear();
    reportComposerPresence('p2', true);
    reportComposerPresence('p2', false);
    await vi.advanceTimersByTimeAsync(250);
    expect(mocks.setPaneComposerState.mock.calls).toEqual([['p2', false]]);
  });

  it('heartbeats true while non-empty and stops after clearing', async () => {
    reportComposerPresence('p1', true);
    await vi.advanceTimersByTimeAsync(250);
    mocks.setPaneComposerState.mockClear();
    await vi.advanceTimersByTimeAsync(30_000);
    expect(mocks.setPaneComposerState).toHaveBeenCalledWith('p1', true);
    reportComposerPresence('p1', false);
    await vi.advanceTimersByTimeAsync(250);
    mocks.setPaneComposerState.mockClear();
    await vi.advanceTimersByTimeAsync(30_000);
    expect(mocks.setPaneComposerState).not.toHaveBeenCalled();
  });
});
