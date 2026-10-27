import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const apiMock = vi.hoisted(() => ({
  readFile: vi.fn(),
}));
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>();
  return { ...actual, api: { ...actual.api, ...apiMock } };
});

import { useStore } from '../store';
import type { FileTileInfo, Layout } from '../types';
import { FileTile } from './FileTile';

const info: FileTileInfo = {
  id: 'file_1',
  path: '/tmp/a.ts',
  createdAt: '2026-08-21T00:00:00.000Z',
};
const splitLayout: Layout = {
  type: 'split',
  direction: 'horizontal',
  ratio: 0.5,
  a: { type: 'pane', paneId: 'p1' },
  b: { type: 'file', fileId: 'file_1' },
};

describe('FileTile zoom', () => {
  beforeEach(() => {
    apiMock.readFile.mockResolvedValue({
      path: info.path,
      lines: ['line one', 'line two'],
      mtime: 1,
      size: 20,
      isEnv: false,
      trailingNewline: true,
    });
    useStore.setState({
      files: { file_1: info },
      layout: splitLayout,
      focusedPaneId: 'file_1',
      zoomedPaneId: null,
    });
  });

  afterEach(() => {
    useStore.setState({ files: {}, layout: null, focusedPaneId: null, zoomedPaneId: null });
    vi.restoreAllMocks();
  });

  it('shows a maximise affordance when the layout has multiple leaves', async () => {
    render(<FileTile info={info} />);
    expect(screen.getByRole('button', { name: 'Maximise pane' })).toBeInTheDocument();
    await waitFor(() => expect(apiMock.readFile).toHaveBeenCalledWith(info.path));
  });

  it('maximises and restores the file tile', () => {
    render(<FileTile info={info} />);

    fireEvent.click(screen.getByRole('button', { name: 'Maximise pane' }));
    expect(useStore.getState().zoomedPaneId).toBe('file_1');
    fireEvent.click(screen.getByRole('button', { name: 'Restore pane' }));
    expect(useStore.getState().zoomedPaneId).toBeNull();
  });

  it('toggles zoom from empty toolbar space without hijacking the path editor', () => {
    const { container } = render(<FileTile info={info} />);
    const toolbar = container.querySelector('.file-toolbar');
    const filePath = container.querySelector('.file-path');
    if (!toolbar || !filePath) throw new Error('file toolbar did not render');

    fireEvent.doubleClick(toolbar);
    expect(useStore.getState().zoomedPaneId).toBe('file_1');
    fireEvent.doubleClick(filePath);
    expect(useStore.getState().zoomedPaneId).toBe('file_1');
    expect(container.querySelector('.file-path-input')).toBeInTheDocument();
  });

  it('omits the zoom affordance for a single file leaf', () => {
    useStore.setState({ layout: { type: 'file', fileId: 'file_1' } });
    render(<FileTile info={info} />);
    expect(screen.queryByRole('button', { name: 'Maximise pane' })).not.toBeInTheDocument();
  });

  it('clears stale zoom when the zoomed file closes', () => {
    useStore.setState({ zoomedPaneId: 'file_1' });
    useStore.getState().closeFileInLayout('file_1');
    expect(useStore.getState().zoomedPaneId).toBeNull();
  });

  it('does not zoom a single layout leaf', () => {
    useStore.setState({ layout: { type: 'file', fileId: 'file_1' }, zoomedPaneId: null });
    useStore.getState().toggleZoom('file_1');
    expect(useStore.getState().zoomedPaneId).toBeNull();
  });
});
