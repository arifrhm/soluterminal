import { describe, expect, it } from 'vitest';
import type { PaneInfo } from '../types';
import { closePaneConfirmMessage } from './usePaneContextMenu';

const pane = {
  id: 'pane-1',
  title: 'DeepSeek Harness',
  status: 'running',
  cmd: ['node', 'launch.mjs', 'deepseek'],
} as PaneInfo;

describe('close pane confirmation', () => {
  it('is unchanged when no work is running', () => {
    expect(closePaneConfirmMessage(pane)).toBe('Close "DeepSeek Harness"?');
  });

  it('names running work so closing cannot silently drop it', () => {
    expect(closePaneConfirmMessage(pane, 1)).toBe(
      'Close "DeepSeek Harness"? 1 task in this session is still running.',
    );
    expect(closePaneConfirmMessage(pane, 3)).toBe(
      'Close "DeepSeek Harness"? 3 tasks in this session are still running.',
    );
  });
});
