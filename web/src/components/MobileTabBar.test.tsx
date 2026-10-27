import { fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { PaneInfo } from '../types';
import { useStore } from '../store';
import { MobileTabBar } from './MobileTabBar';

vi.mock('./VoiceButton', () => ({
  VoiceButton: () => <button type="button">Voice</button>,
  VoiceAutoSendToggle: () => <button type="button">Review</button>,
  VoiceLanguageSelect: () => <select aria-label="Voice language" />,
}));

const pane: PaneInfo = {
  id: 'pane-1',
  cwd: '/home/user/project',
  cmd: ['bash'],
  title: 'Shell',
  status: 'running',
  createdAt: '2026-05-18T00:00:00.000Z',
  cols: 80,
  rows: 24,
  pid: 1234,
  lastActivityMs: 0,
  state: 'idle',
  hasClaude: false,
};

describe('MobileTabBar leaf picker', () => {
  beforeEach(() => {
    useStore.setState({
      layout: {
        type: 'split',
        direction: 'horizontal',
        ratio: 0.5,
        a: { type: 'pane', paneId: 'pane-1' },
        b: {
          type: 'split',
          direction: 'vertical',
          ratio: 0.5,
          a: { type: 'browser', browserId: 'browser-1' },
          b: { type: 'file', fileId: 'file-1' },
        },
      },
      panes: { 'pane-1': pane },
      browsers: {
        'browser-1': {
          id: 'browser-1',
          url: 'https://example.com/docs',
          title: 'Docs',
          createdAt: '2026-05-18T00:00:00.000Z',
        },
      },
      files: {
        'file-1': {
          id: 'file-1',
          path: '/home/user/project/package.json',
          createdAt: '2026-05-18T00:00:00.000Z',
        },
      },
      focusedPaneId: 'pane-1',
      zoomedPaneId: null,
    });
  });

  afterEach(() => {
    useStore.setState({
      layout: null,
      panes: {},
      browsers: {},
      files: {},
      focusedPaneId: null,
      zoomedPaneId: null,
    });
    vi.restoreAllMocks();
  });

  it('lists terminal, browser, and file leaves and can focus a non-terminal leaf', () => {
    render(
      <MobileTabBar
        onOpenDrawer={vi.fn()}
        keybarOpen={false}
        onToggleKeybar={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByTitle('Shell'));

    expect(screen.getByRole('option', { name: /terminal shell/i })).toBeInTheDocument();
    expect(screen.getByRole('option', { name: /browser docs/i })).toBeInTheDocument();
    expect(screen.getByRole('option', { name: /file package\.json/i })).toBeInTheDocument();

    fireEvent.click(screen.getByRole('option', { name: /browser docs/i }));

    expect(useStore.getState().focusedPaneId).toBe('browser-1');
  });
});
