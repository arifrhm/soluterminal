import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '../api';
import { __resetPreviewChannelForTests } from '../lib/previewChannel';
import type { PreviewChannelStatus } from '../types';
import { PreviewChannelSection } from './PreviewChannelSection';

function status(over: Partial<PreviewChannelStatus>): PreviewChannelStatus {
  return { available: true, channel: 'stable', running: '0.0.1168', stable: '0.0.1168', main: {}, newChanges: 0, ahead: 0, ...over };
}

async function renderWith(st: PreviewChannelStatus) {
  vi.spyOn(api, 'getPreviewChannel').mockResolvedValue(st);
  render(<PreviewChannelSection />);
  await act(async () => {});
}

describe('PreviewChannelSection', () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    __resetPreviewChannelForTests();
  });

  it('local preview explains isolation without polling or offering live installation', async () => {
    vi.stubGlobal('__SOLU_LOCAL_PREVIEW__', true);
    const getStatus = vi.spyOn(api, 'getPreviewChannel');
    render(<PreviewChannelSection />);
    await act(async () => {});
    expect(screen.getByLabelText('Local preview')).toBeTruthy();
    expect(screen.getByText(/own workspace/)).toBeTruthy();
    expect(getStatus).not.toHaveBeenCalled();
    expect(screen.queryByRole('button')).toBeNull();
  });

  it('shows nothing where Preview is not available (teammates, phone)', async () => {
    await renderWith({ available: false, main: {}, newChanges: 0, ahead: 0 });
    expect(screen.queryByLabelText('Release channel')).toBeNull();
  });

  it('on Stable makes clear that Preview switches the running app', async () => {
    await renderWith(status({ newChanges: 18, main: { mainVersion: '0.0.1168.18', ci: 'attested' } }));
    expect(screen.getByText(/Stable 0.0.1168 — the version everyone gets/)).toBeTruthy();
    expect(screen.getByText(/18 new changes, tested by GitHub/)).toBeTruthy();
    const install = vi.spyOn(api, 'installPreviewChannel').mockResolvedValue({ started: true });
    expect(screen.getByText(/Local development preview opens separately/)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Switch live app to Preview' }));
    await act(async () => {});
    expect(install).toHaveBeenCalledWith('preview');
    expect(screen.queryByRole('button', { name: /Switch back to Stable/ })).toBeNull();
  });

  it('on Preview shows how far ahead it is, and offers update + back to Stable', async () => {
    await renderWith(status({
      channel: 'preview', running: '0.0.1168.15', ahead: 15, newChanges: 3,
      main: { mainVersion: '0.0.1168.18', ci: 'attested' },
    }));
    expect(screen.getByText(/Preview 0.0.1168.15 — 15 changes ahead of Stable 0.0.1168/)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Update to latest preview' })).toBeTruthy();
    const install = vi.spyOn(api, 'installPreviewChannel').mockResolvedValue({ started: true });
    fireEvent.click(screen.getByRole('button', { name: 'Switch back to Stable' }));
    await act(async () => {});
    expect(install).toHaveBeenCalledWith('stable');
  });

  it('does not offer untested main', async () => {
    await renderWith(status({ channel: 'preview', running: '0.0.1168.15', ahead: 15, main: { mainVersion: '0.0.1168.18', ci: 'pending' } }));
    expect(screen.queryByRole('button', { name: /latest preview/ })).toBeNull();
    expect(screen.getByText(/still being tested by GitHub/)).toBeTruthy();
  });

  it('shows build progress instead of buttons while switching', async () => {
    await renderWith(status({ job: 'building-preview', jobLine: '==> building daemon 0.0.1168.18', newChanges: 3, main: { ci: 'attested' } }));
    expect(screen.getByRole('status').textContent).toContain('Building preview on this PC');
    expect(screen.getByRole('status').textContent).toContain('building daemon 0.0.1168.18');
    expect(screen.queryByRole('button')).toBeNull();
  });

  it('lists unreleased changes with how to try each, and which ones this app has', async () => {
    await renderWith(status({
      channel: 'preview', running: '0.0.1168.1', ahead: 1,
      unreleased: [
        { n: 2, sha: 'b', pr: 12, title: 'Show reviews on panes', howToTry: '1. Run solu-review in a pane\n2. Open the chip', installed: false },
        { n: 1, sha: 'a', pr: 11, title: 'Fix X', installed: true },
      ],
    }));
    expect(screen.getByText(/Not released yet: 2 changes · 1 to try/)).toBeTruthy();
    expect(screen.getByText(/Run solu-review in a pane/)).toBeTruthy();
    expect(screen.getByText('on this app')).toBeTruthy();
    expect(screen.getByText('next preview')).toBeTruthy();
    const opened = vi.fn();
    window.addEventListener('solu:open-inbox', opened);
    fireEvent.click(screen.getByRole('button', { name: 'Report a problem' }));
    expect(opened).toHaveBeenCalled();
    window.removeEventListener('solu:open-inbox', opened);
  });
});
