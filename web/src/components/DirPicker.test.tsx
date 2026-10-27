import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createElement } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api, type DirCandidate } from '../api';
import { DirPicker } from './DirPicker';

const candidate = (path: string, source: DirCandidate['source'] = 'recent'): DirCandidate => ({
  path,
  source,
  label: path,
});

beforeEach(() => {
  vi.spyOn(api, 'listDirs').mockResolvedValue([]);
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('DirPicker repository suggestions', () => {
  it('shows the full pinned and fetched lists when an untouched default is first opened', async () => {
    vi.mocked(api.listDirs).mockResolvedValue([candidate('/api/repo', 'discovered')]);
    render(createElement(DirPicker, {
      value: '/work/repo',
      onChange: () => {},
      pinned: [candidate('/work/repo'), candidate('/work/other')],
    }));

    fireEvent.click(screen.getByRole('textbox'));

    expect(screen.getByText('/work/repo')).toBeInTheDocument();
    expect(screen.getByText('/work/other')).toBeInTheDocument();
    await waitFor(() => expect(api.listDirs).toHaveBeenCalledWith(''));
    expect(await screen.findByText('/api/repo')).toBeInTheDocument();
  });

  it('survives a daemon that answers null for an empty home', async () => {
    vi.mocked(api.listDirs).mockResolvedValue(null as unknown as DirCandidate[]);
    render(createElement(DirPicker, { value: '', onChange: () => {} }));
    fireEvent.click(screen.getByRole('textbox'));
    await waitFor(() => expect(api.listDirs).toHaveBeenCalled());
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.getByRole('textbox')).toBeInTheDocument();
  });

  it('filters pinned paths case-insensitively after editing', () => {
    const onChange = vi.fn();
    const { rerender } = render(createElement(DirPicker, {
      value: '',
      onChange,
      pinned: [candidate('/Work/Repo'), candidate('/work/other')],
    }));
    const input = screen.getByRole('textbox');

    fireEvent.change(input, { target: { value: 'REPO' } });
    expect(onChange).toHaveBeenCalledWith('REPO');
    rerender(createElement(DirPicker, {
      value: 'REPO',
      onChange,
      pinned: [candidate('/Work/Repo'), candidate('/work/other')],
    }));

    expect(screen.getByText('/Work/Repo')).toBeInTheDocument();
    expect(screen.queryByText('/work/other')).toBeNull();
  });

  it('deduplicates pinned and fetched paths and navigates the merged list', async () => {
    vi.mocked(api.listDirs).mockResolvedValue([
      candidate('/work/repo', 'discovered'),
      candidate('/work/other', 'discovered'),
    ]);
    const onChange = vi.fn();
    const { container } = render(createElement(DirPicker, {
      value: '',
      onChange,
      pinned: [candidate('/Work/Repo')],
    }));
    const input = screen.getByRole('textbox');
    fireEvent.click(input);
    await screen.findByText('/work/other');

    expect(container.ownerDocument.querySelectorAll('.dirpicker-item')).toHaveLength(2);
    fireEvent.keyDown(input, { key: 'ArrowDown' });
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(onChange).toHaveBeenCalledWith('/work/other');
  });

  it('resets a stale highlight when pinned suggestions change', () => {
    const onChange = vi.fn();
    const { rerender } = render(createElement(DirPicker, {
      value: '',
      onChange,
      pinned: [candidate('/work/a'), candidate('/work/b')],
    }));
    const input = screen.getByRole('textbox');
    fireEvent.click(input);
    fireEvent.keyDown(input, { key: 'ArrowDown' });

    rerender(createElement(DirPicker, {
      value: '',
      onChange,
      pinned: [candidate('/work/c')],
    }));
    fireEvent.keyDown(input, { key: 'Enter' });

    expect(onChange).toHaveBeenCalledWith('/work/c');
  });

  it('preserves fetched-only behavior and does not add an input class by default', async () => {
    vi.mocked(api.listDirs).mockResolvedValue([
      candidate('/work/a'),
      candidate('/work/b', 'discovered'),
    ]);
    const onChange = vi.fn();
    render(createElement(DirPicker, { value: '', onChange }));
    const input = screen.getByRole('textbox');
    expect(input).not.toHaveAttribute('class');
    fireEvent.click(input);
    await screen.findByText('/work/b');

    const labels = Array.from(document.querySelectorAll('.dirpicker-label')).map((node) => node.textContent);
    expect(labels).toEqual(['/work/a', '/work/b']);
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(onChange).toHaveBeenCalledWith('/work/a');
  });
});
