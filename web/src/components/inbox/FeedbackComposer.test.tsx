import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useStore } from '../../store';
import { api } from '../../api';
import { useCloud } from '../../cloud';
import { FeedbackComposer } from './FeedbackComposer';

vi.mock('../../api', async (importOriginal) => ({ ...(await importOriginal<typeof import('../../api')>()),
  api: { submitFeedback: vi.fn(), feedbackStatus: vi.fn() },
}));

const submitFeedback = vi.mocked(api.submitFeedback);
const feedbackStatus = vi.mocked(api.feedbackStatus);

function seed() {
  const toast = vi.fn();
  useStore.setState({ toast, focusedPaneId: 'pane_42' } as never);
  return { toast };
}

beforeEach(() => {
  submitFeedback.mockReset();
  feedbackStatus.mockReset().mockResolvedValue({ available: true });
  localStorage.removeItem('solu.feedbackDraft.v1');
});

afterEach(() => {
  vi.unstubAllGlobals();
});

// A FileReader stand-in whose readAsDataURL does NOT resolve until the test
// flushes it, so we can observe the window where a batch is still encoding.
function installDeferredFileReader() {
  const pending: Array<() => void> = [];
  class DeferredFileReader {
    result: string | null = null;
    error: unknown = null;
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    readAsDataURL(file: File) {
      pending.push(() => {
        this.result = `data:${file.type};base64,QUJD`;
        this.onload?.();
      });
    }
  }
  vi.stubGlobal('FileReader', DeferredFileReader as unknown as typeof FileReader);
  return {
    async flush() {
      await act(async () => {
        for (const fn of pending.splice(0)) fn();
      });
    },
  };
}

describe('FeedbackComposer', () => {
  it('submits type, text, snapshot intent and active pane id, then closes', async () => {
    const { toast } = seed();
    submitFeedback.mockResolvedValue({ ok: true, number: 99, url: 'https://x/issues/99' });
    const onClose = vi.fn();
    render(<FeedbackComposer onDone={onClose} onCancel={() => {}} />);

    fireEvent.click(screen.getByRole('button', { name: 'Idea' }));
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'Add dark mode' } });
    fireEvent.change(screen.getByLabelText('Details'), { target: { value: 'It would be nice' } });
    fireEvent.click(screen.getByLabelText(/Attach redacted active-pane text/));
    fireEvent.click(screen.getByRole('button', { name: 'Send feedback' }));

    await waitFor(() => expect(submitFeedback).toHaveBeenCalledTimes(1));
    expect(submitFeedback.mock.calls[0][0]).toMatchObject({
      type: 'idea',
      title: 'Add dark mode',
      body: 'It would be nice',
      includeSnapshot: true,
      paneId: 'pane_42',
    });
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(toast).toHaveBeenCalledWith(
      expect.stringContaining('#99'),
      expect.objectContaining({ kind: 'success' }),
    );
  });

  it('tells the reporter a queued report will be filed shortly', async () => {
    const { toast } = seed();
    submitFeedback.mockResolvedValue({ ok: true, number: 0, url: '', queued: true });
    render(<FeedbackComposer onDone={() => {}} onCancel={() => {}} />);
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'Broken' } });
    fireEvent.change(screen.getByLabelText('Details'), { target: { value: 'It broke' } });
    fireEvent.click(screen.getByRole('button', { name: 'Send feedback' }));
    await waitFor(() => expect(toast).toHaveBeenCalledWith(
      'Feedback received — it will be filed shortly',
      expect.objectContaining({ kind: 'success' }),
    ));
  });

  it('blocks submit until title and details are filled', async () => {
    seed();
    render(<FeedbackComposer onDone={() => {}} onCancel={() => {}} />);
    fireEvent.click(screen.getByRole('button', { name: 'Send feedback' }));
    expect(await screen.findByText('Add a short title.')).toBeTruthy();
    expect(submitFeedback).not.toHaveBeenCalled();
  });

  it('keeps the dialog open and surfaces the error when the API fails', async () => {
    seed();
    submitFeedback.mockRejectedValue(new Error('network boom'));
    const onClose = vi.fn();
    render(<FeedbackComposer onDone={onClose} onCancel={() => {}} />);
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'x' } });
    fireEvent.change(screen.getByLabelText('Details'), { target: { value: 'y' } });
    fireEvent.click(screen.getByRole('button', { name: 'Send feedback' }));
    expect(await screen.findByText('network boom')).toBeTruthy();
    expect(onClose).not.toHaveBeenCalled();
  });

  it('omits paneId when the snapshot checkbox is left unchecked (the default)', async () => {
    seed();
    submitFeedback.mockResolvedValue({ ok: true, number: 1, url: 'u' });
    render(<FeedbackComposer onDone={() => {}} onCancel={() => {}} />);
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'x' } });
    fireEvent.change(screen.getByLabelText('Details'), { target: { value: 'y' } });
    fireEvent.click(screen.getByRole('button', { name: 'Send feedback' }));
    await waitFor(() => expect(submitFeedback).toHaveBeenCalled());
    const arg = submitFeedback.mock.calls[0][0];
    expect(arg.includeSnapshot).toBe(false);
    expect(arg.paneId).toBeUndefined();
  });

  // ── Screenshot attachments ────────────────────────────────────────────────

  // The hidden <input type="file"> the "Add screenshots" button drives.
  function fileInput(container: HTMLElement): HTMLInputElement {
    const el = container.querySelector('input[type="file"]') as HTMLInputElement | null;
    if (!el) throw new Error('file input not found');
    return el;
  }

  // A File whose .size/.type we control; the component reads file.size for the
  // oversize gate and file.type for the MIME gate.
  function fakeFile(name: string, type: string, size: number, body = 'IMG'): File {
    const f = new File([body], name, { type });
    Object.defineProperty(f, 'size', { value: size });
    return f;
  }

  it('rejects a file larger than 5 MB', async () => {
    seed();
    const { container } = render(<FeedbackComposer onDone={() => {}} onCancel={() => {}} />);
    const big = fakeFile('huge.png', 'image/png', 6 * 1024 * 1024);
    fireEvent.change(fileInput(container), { target: { files: [big] } });
    expect(await screen.findByText(/too large/i)).toBeTruthy();
    expect(container.querySelectorAll('img').length).toBe(0);
  });

  it('rejects a disallowed MIME type', async () => {
    seed();
    const { container } = render(<FeedbackComposer onDone={() => {}} onCancel={() => {}} />);
    const svg = fakeFile('vector.svg', 'image/svg+xml', 1000);
    fireEvent.change(fileInput(container), { target: { files: [svg] } });
    expect(await screen.findByText(/unsupported type/i)).toBeTruthy();
    expect(container.querySelectorAll('img').length).toBe(0);
  });

  it('says screenshots are not stored once one is attached', async () => {
    seed();
    const { container } = render(<FeedbackComposer onDone={() => {}} onCancel={() => {}} />);
    expect(screen.queryByText(/Screenshots aren't stored yet/)).toBeNull();
    fireEvent.change(fileInput(container), { target: { files: [fakeFile('s.png', 'image/png', 1000)] } });
    expect(await screen.findByText("Screenshots aren't stored yet; please describe what you saw.")).toBeTruthy();
  });

  it('caps attachments at 3 files', async () => {
    seed();
    const { container } = render(<FeedbackComposer onDone={() => {}} onCancel={() => {}} />);
    const files = [1, 2, 3, 4].map((n) => fakeFile(`s${n}.png`, 'image/png', 1000));
    fireEvent.change(fileInput(container), { target: { files } });
    await waitFor(() => expect(container.querySelectorAll('img').length).toBe(3));
    expect(await screen.findByText(/at most 3 screenshots/i)).toBeTruthy();
  });

  it('encodes an accepted file to {name,mime,data} with no data: prefix and submits attachments', async () => {
    seed();
    submitFeedback.mockResolvedValue({ ok: true, number: 7, url: 'u' });
    const { container } = render(<FeedbackComposer onDone={() => {}} onCancel={() => {}} />);
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'shot' } });
    fireEvent.change(screen.getByLabelText('Details'), { target: { value: 'see image' } });
    const png = fakeFile('shot.png', 'image/png', 1234, 'hello-bytes');
    fireEvent.change(fileInput(container), { target: { files: [png] } });
    await waitFor(() => expect(container.querySelectorAll('img').length).toBe(1));

    fireEvent.click(screen.getByRole('button', { name: 'Send feedback' }));
    await waitFor(() => expect(submitFeedback).toHaveBeenCalled());
    const arg = submitFeedback.mock.calls[0][0];
    expect(Array.isArray(arg.attachments)).toBe(true);
    expect(arg.attachments).toHaveLength(1);
    const att = arg.attachments![0];
    expect(att.name).toBe('shot.png');
    expect(att.mime).toBe('image/png');
    expect(att.data.length).toBeGreaterThan(0);
    expect(att.data.startsWith('data:')).toBe(false);
    expect(att.data).not.toContain(',');
  });

  it('blocks send while images are still encoding, then includes them once ready', async () => {
    seed();
    submitFeedback.mockResolvedValue({ ok: true, number: 11, url: 'u' });
    const reader = installDeferredFileReader();
    const { container } = render(<FeedbackComposer onDone={() => {}} onCancel={() => {}} />);
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'shot' } });
    fireEvent.change(screen.getByLabelText('Details'), { target: { value: 'see image' } });

    const png = fakeFile('shot.png', 'image/png', 1234);
    fireEvent.change(fileInput(container), { target: { files: [png] } });

    // Encode is in flight: the Send button must be disabled and a click must
    // not file the report (which would drop the screenshot).
    const send = screen.getByRole('button', { name: 'Send feedback' }) as HTMLButtonElement;
    await waitFor(() => expect(send.disabled).toBe(true));
    fireEvent.click(send);
    expect(submitFeedback).not.toHaveBeenCalled();

    // Now let the encode resolve; the screenshot lands and send works.
    await reader.flush();
    await waitFor(() => expect(container.querySelectorAll('img').length).toBe(1));
    await waitFor(() => expect(send.disabled).toBe(false));

    fireEvent.click(screen.getByRole('button', { name: 'Send feedback' }));
    await waitFor(() => expect(submitFeedback).toHaveBeenCalled());
    const arg = submitFeedback.mock.calls[0][0];
    expect(arg.attachments).toHaveLength(1);
    expect(arg.attachments![0].name).toBe('shot.png');
  });

  it('caps at 3 across two concurrent adds before the first commits', async () => {
    seed();
    const reader = installDeferredFileReader();
    const { container } = render(<FeedbackComposer onDone={() => {}} onCancel={() => {}} />);
    // Two near-simultaneous batches, neither committed yet — the stale closure
    // would let both pass the running-cap check and push 4 total.
    const batchA = [1, 2].map((n) => fakeFile(`a${n}.png`, 'image/png', 1000));
    const batchB = [3, 4].map((n) => fakeFile(`b${n}.png`, 'image/png', 1000));
    fireEvent.change(fileInput(container), { target: { files: batchA } });
    fireEvent.change(fileInput(container), { target: { files: batchB } });
    await reader.flush();
    await waitFor(() => expect(container.querySelectorAll('img').length).toBe(3));
  });

  it('persists a draft across unmount and clears it on send', async () => {
    seed();
    submitFeedback.mockResolvedValue({ ok: true, number: 5, url: 'u' });
    const { unmount } = render(<FeedbackComposer onDone={() => {}} onCancel={() => {}} />);
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'half-written' } });
    fireEvent.change(screen.getByLabelText('Details'), { target: { value: 'came back later' } });
    unmount();

    // Remount: the draft restores.
    render(<FeedbackComposer onDone={() => {}} onCancel={() => {}} />);
    expect((screen.getByLabelText('Title') as HTMLInputElement).value).toBe('half-written');
    fireEvent.click(screen.getByRole('button', { name: 'Send feedback' }));
    await waitFor(() => expect(submitFeedback).toHaveBeenCalled());
    expect(localStorage.getItem('solu.feedbackDraft.v1')).toBeNull();
  });

  it('omits attachments from the payload when none are added', async () => {
    seed();
    submitFeedback.mockResolvedValue({ ok: true, number: 8, url: 'u' });
    render(<FeedbackComposer onDone={() => {}} onCancel={() => {}} />);
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'x' } });
    fireEvent.change(screen.getByLabelText('Details'), { target: { value: 'y' } });
    fireEvent.click(screen.getByRole('button', { name: 'Send feedback' }));
    await waitFor(() => expect(submitFeedback).toHaveBeenCalled());
    expect(submitFeedback.mock.calls[0][0].attachments).toBeUndefined();
  });
});

describe('FeedbackComposer without a connected account', () => {
  it('leaves pane text and logs unattached by default', async () => {
    seed();
    render(<FeedbackComposer onDone={() => {}} onCancel={() => {}} />);
    expect(screen.getByLabelText(/Attach redacted active-pane text/)).not.toBeChecked();
  });

  it('shows the email fallback instead of the form when signed out', async () => {
    seed();
    feedbackStatus.mockResolvedValue({ available: false, reason: 'signed_out' });
    render(<FeedbackComposer onDone={() => {}} onCancel={() => {}} />);
    expect(await screen.findByText('Sign in to send feedback')).toBeInTheDocument();
    expect(screen.getByText('hello@soluterminal.com')).toBeInTheDocument();
    expect(screen.queryByLabelText('Title')).toBeNull();
  });

  it('offers Sign in next to the email fallback on a local install', async () => {
    seed();
    const requestSignIn = vi.fn();
    const initial = useCloud.getState();
    useCloud.setState({ mode: 'cloud', isLocal: true, accountOptional: true, withoutAccount: true, session: null, requestSignIn });
    feedbackStatus.mockResolvedValue({ available: false, reason: 'signed_out' });
    const onDone = vi.fn();
    try {
      render(<FeedbackComposer onDone={onDone} onCancel={() => {}} />);
      const signIn = await screen.findByRole('button', { name: 'Sign in' });
      expect(screen.getByRole('button', { name: 'Copy email address' })).toBeInTheDocument();
      fireEvent.click(signIn);
      expect(onDone).toHaveBeenCalled();
      expect(requestSignIn).toHaveBeenCalled();
    } finally {
      useCloud.setState(initial, true);
    }
  });

  it('has no Sign in where this install has no sign-in screen', async () => {
    seed();
    const initial = useCloud.getState();
    useCloud.setState({ mode: 'local', isLocal: true, accountOptional: false });
    feedbackStatus.mockResolvedValue({ available: false, reason: 'signed_out' });
    try {
      render(<FeedbackComposer onDone={() => {}} onCancel={() => {}} />);
      expect(await screen.findByRole('button', { name: 'Copy email address' })).toBeInTheDocument();
      expect(screen.queryByRole('button', { name: 'Sign in' })).toBeNull();
    } finally {
      useCloud.setState(initial, true);
    }
  });

  it('explains an unconnected computer and copies the address', async () => {
    const { toast } = seed();
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } });
    feedbackStatus.mockResolvedValue({ available: false, reason: 'unpaired' });
    render(<FeedbackComposer onDone={() => {}} onCancel={() => {}} />);
    expect(await screen.findByText('Connect this computer to send feedback')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Sign in' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Copy email address' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('hello@soluterminal.com'));
    expect(toast).toHaveBeenCalledWith('Email address copied', expect.objectContaining({ kind: 'success' }));
  });
});
