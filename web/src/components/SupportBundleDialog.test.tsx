import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import fixtureJSON from '../../testdata/support-bundle-2026-09-06.json';
import { api } from '../api';
import {
  SupportBundleRemoteError,
  formatBundleBytes,
  renderSupportBundle,
  type SupportBundleResponse,
} from '../lib/supportBundle';
import { useStore } from '../store';
import { SupportBundleDialog } from './SupportBundleDialog';

vi.mock('../api', async (importOriginal) => ({ ...(await importOriginal<typeof import('../api')>()),
  api: { supportBundle: vi.fn() },
}));

const supportBundle = vi.mocked(api.supportBundle);
const fixture = fixtureJSON as SupportBundleResponse;

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (cause: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

describe('SupportBundleDialog', () => {
  let createObjectURL: ReturnType<typeof vi.fn>;
  let revokeObjectURL: ReturnType<typeof vi.fn>;
  let anchorClick: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    supportBundle.mockReset();
    useStore.setState({ focusedPaneId: 'pane_fixture', toast: vi.fn() } as never);
    createObjectURL = vi.fn(() => 'blob:support-bundle');
    revokeObjectURL = vi.fn();
    vi.stubGlobal('URL', { ...URL, createObjectURL, revokeObjectURL });
    anchorClick = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('previews the server bytes and saves only the current selection', async () => {
    supportBundle.mockResolvedValue(fixture);
    const onClose = vi.fn();
    render(<SupportBundleDialog open onClose={onClose} />);

    for (const section of fixture.sections) {
      const title = await screen.findByText(section.title);
      expect(title).toBeInTheDocument();
      expect(within(title.closest('section')!).getByText(new RegExp(`^${formatBundleBytes(section.bytes)} ·`))).toBeInTheDocument();
    }
    expect(supportBundle).toHaveBeenCalledWith('pane_fixture');

    const versionsSection = screen.getByText('Versions and runtime').closest('section');
    expect(versionsSection).not.toBeNull();
    fireEvent.click(within(versionsSection!).getByRole('button', { name: 'Preview' }));
    expect(versionsSection!.querySelector('pre')?.textContent).toBe(fixture.sections[0].text);

    const logsSection = screen.getByText('Recent daemon logs').closest('section');
    expect(logsSection).not.toBeNull();
    fireEvent.click(within(logsSection!).getByRole('checkbox'));

    const paneCheckbox = within(screen.getByText('Focused pane tail').closest('section')!).getByRole('checkbox');
    expect(paneCheckbox).not.toBeChecked();
    fireEvent.click(screen.getByRole('button', { name: 'Save bundle' }));

    expect(anchorClick).toHaveBeenCalledTimes(1);
    const anchor = anchorClick.mock.instances[0] as HTMLAnchorElement;
    expect(anchor.download).toBe(fixture.filename);
    const blob = createObjectURL.mock.calls[0][0] as Blob;
    const expectedSelection = new Set(fixture.sections
      .filter((section) => section.selectedByDefault && section.id !== 'logs')
      .map((section) => section.id));
    expect(await blob.text()).toBe(renderSupportBundle(fixture, expectedSelection));
    expect(await blob.text()).not.toContain(fixture.sections.find((section) => section.id === 'paneText')!.text);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('shows a failure and retries collection without exposing Save', async () => {
    supportBundle.mockRejectedValueOnce(new Error('fixture collection failed')).mockResolvedValueOnce(fixture);
    render(<SupportBundleDialog open onClose={() => {}} />);
    expect(await screen.findByText('fixture collection failed')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Save bundle' })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    await screen.findByText('Versions and runtime');
    expect(supportBundle).toHaveBeenCalledTimes(2);
  });

  it('explains the local-only boundary for relay callers and has no Save action', async () => {
    supportBundle.mockRejectedValue(new SupportBundleRemoteError());
    render(<SupportBundleDialog open onClose={() => {}} />);
    expect(await screen.findByText(/Diagnostics never travel over the cloud relay/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Save bundle' })).not.toBeInTheDocument();
  });

  it('disables saving when every source is empty', async () => {
    const empty: SupportBundleResponse = {
      ...fixture,
      sections: fixture.sections.map((section) => ({ ...section, text: '', bytes: 0, lines: 0, empty: true, note: 'No fixture data.' })),
    };
    supportBundle.mockResolvedValue(empty);
    render(<SupportBundleDialog open onClose={() => {}} />);
    expect(await screen.findByText(/No diagnostic data is available yet/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Save bundle' })).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: 'Save bundle' }));
    expect(anchorClick).not.toHaveBeenCalled();
  });

  it('opts pane text in only after the user selects it', async () => {
    supportBundle.mockResolvedValue(fixture);
    render(<SupportBundleDialog open onClose={() => {}} />);
    const paneSection = (await screen.findByText('Focused pane tail')).closest('section')!;
    const checkbox = within(paneSection).getByRole('checkbox');
    expect(checkbox).not.toBeChecked();
    fireEvent.click(checkbox);
    fireEvent.click(screen.getByRole('button', { name: 'Save bundle' }));
    const blob = createObjectURL.mock.calls[0][0] as Blob;
    expect(await blob.text()).toContain(fixture.sections.find((section) => section.id === 'paneText')!.text);
  });

  it('cancels by button without downloading or recollecting', async () => {
    supportBundle.mockResolvedValue(fixture);
    const onClose = vi.fn();
    render(<SupportBundleDialog open onClose={onClose} />);
    await screen.findByText('Versions and runtime');
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(anchorClick).not.toHaveBeenCalled();
    expect(supportBundle).toHaveBeenCalledTimes(1);
  });

  it('cancels with Escape without downloading or recollecting', async () => {
    supportBundle.mockResolvedValue(fixture);
    const onClose = vi.fn();
    render(<SupportBundleDialog open onClose={onClose} />);
    await screen.findByText('Versions and runtime');
    act(() => window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })));
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(anchorClick).not.toHaveBeenCalled();
    expect(supportBundle).toHaveBeenCalledTimes(1);
  });

  it('initializes focus inside the modal and wraps Tab in both directions', async () => {
    supportBundle.mockResolvedValue(fixture);
    const offsetParent = vi.spyOn(HTMLElement.prototype, 'offsetParent', 'get')
      .mockImplementation(function visibleElement(this: HTMLElement) {
        return this.parentElement;
      });
    render(
      <>
        <button type="button">Background action</button>
        <SupportBundleDialog open onClose={() => {}} />
      </>,
    );
    await screen.findByText('Versions and runtime');
    const close = screen.getByRole('button', { name: 'Close' });
    const save = screen.getByRole('button', { name: 'Save bundle' });

    expect(close).toHaveFocus();
    save.focus();
    fireEvent.keyDown(document, { key: 'Tab' });
    expect(close).toHaveFocus();
    fireEvent.keyDown(document, { key: 'Tab', shiftKey: true });
    expect(save).toHaveFocus();
    offsetParent.mockRestore();
  });

  it('ignores an earlier success that resolves after a reopened collection', async () => {
    const first = deferred<SupportBundleResponse>();
    const second = deferred<SupportBundleResponse>();
    const firstFixture = {
      ...fixture,
      sections: fixture.sections.map((section, index) => index === 0 ? { ...section, title: 'Stale versions' } : section),
    };
    const secondFixture = {
      ...fixture,
      sections: fixture.sections.map((section, index) => index === 0 ? { ...section, title: 'Current versions' } : section),
    };
    supportBundle.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    const view = render(<SupportBundleDialog open onClose={() => {}} />);

    view.rerender(<SupportBundleDialog open={false} onClose={() => {}} />);
    useStore.setState({ focusedPaneId: 'pane_reopened' });
    view.rerender(<SupportBundleDialog open onClose={() => {}} />);
    second.resolve(secondFixture);
    expect(await screen.findByText('Current versions')).toBeInTheDocument();
    first.resolve(firstFixture);
    await act(async () => { await first.promise; });

    expect(screen.queryByText('Stale versions')).not.toBeInTheDocument();
    expect(screen.getByText('Current versions')).toBeInTheDocument();
    expect(supportBundle).toHaveBeenNthCalledWith(1, 'pane_fixture');
    expect(supportBundle).toHaveBeenNthCalledWith(2, 'pane_reopened');
  });

  it('ignores an earlier failure that rejects after a reopened collection', async () => {
    const first = deferred<SupportBundleResponse>();
    const second = deferred<SupportBundleResponse>();
    supportBundle.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    const view = render(<SupportBundleDialog open onClose={() => {}} />);

    view.rerender(<SupportBundleDialog open={false} onClose={() => {}} />);
    view.rerender(<SupportBundleDialog open onClose={() => {}} />);
    second.resolve(fixture);
    expect(await screen.findByText('Versions and runtime')).toBeInTheDocument();
    first.reject(new Error('stale collection failed'));
    await act(async () => {
      try {
        await first.promise;
      } catch {
        // The component must ignore this stale failure.
      }
    });

    expect(screen.queryByText('stale collection failed')).not.toBeInTheDocument();
    expect(screen.getByText('Versions and runtime')).toBeInTheDocument();
  });

  it('keeps the open-time pane and current selections when focus changes', async () => {
    supportBundle.mockResolvedValue(fixture);
    render(<SupportBundleDialog open onClose={() => {}} />);
    const logsSection = (await screen.findByText('Recent daemon logs')).closest('section')!;
    const logsCheckbox = within(logsSection).getByRole('checkbox');
    fireEvent.click(logsCheckbox);
    expect(logsCheckbox).not.toBeChecked();

    act(() => useStore.setState({ focusedPaneId: 'pane_changed_while_open' }));

    expect(supportBundle).toHaveBeenCalledTimes(1);
    expect(supportBundle).toHaveBeenCalledWith('pane_fixture');
    expect(logsCheckbox).not.toBeChecked();
  });
});
