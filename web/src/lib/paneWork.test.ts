import { beforeEach, describe, expect, it } from 'vitest';
import { clearPaneRunningWork, paneRunningWork, setPaneRunningWork } from './paneWork';

describe('pane running-work signal', () => {
  beforeEach(() => {
    for (const id of ['pane-1', 'a', 'b']) clearPaneRunningWork(id);
  });

  it('tracks a positive count and clears at zero', () => {
    expect(paneRunningWork('pane-1')).toBe(0);
    setPaneRunningWork('pane-1', 2);
    expect(paneRunningWork('pane-1')).toBe(2);
    setPaneRunningWork('pane-1', 1);
    expect(paneRunningWork('pane-1')).toBe(1);
    setPaneRunningWork('pane-1', 0);
    expect(paneRunningWork('pane-1')).toBe(0);
  });

  it('ignores junk counts instead of warning about a bogus task', () => {
    setPaneRunningWork('pane-1', Number.NaN);
    expect(paneRunningWork('pane-1')).toBe(0);
    setPaneRunningWork('pane-1', -3);
    expect(paneRunningWork('pane-1')).toBe(0);
  });

  it('is scoped per pane', () => {
    setPaneRunningWork('a', 1);
    setPaneRunningWork('b', 0);
    expect(paneRunningWork('a')).toBe(1);
    expect(paneRunningWork('b')).toBe(0);
  });
});
