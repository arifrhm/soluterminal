import { createRef } from 'react';
import { fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Composer } from './Composer';
import { useEntitlements } from '../../entitlements';
import { setExtensionsForTests, type ComposerVoiceState } from '../../extensions';

// Isolate the Composer's PRESENTATIONAL state machine (send / stop / attach /
// placeholder / raw-then-patch gate) from the gesture engine, which owns
// MediaRecorder + pointer timing and can't meaningfully run in jsdom. The
// engine is lifted verbatim from ARIA's proven composer; here we pin the
// contract both surfaces depend on, through the composerVoice slot. `mockVoice`
// is mutable so a test can put the hook into the "polishing" state and assert
// the send gate.
const baseVoice: ComposerVoiceState = {
  phase: 'idle',
  recording: false,
  transcribing: false,
  handsFree: false,
  slide: 'none' as const,
  elapsedMs: 0,
  handlers: {} as ComposerVoiceState['handlers'],
  stopAndSend: () => {},
  cancel: () => {},
  polishing: false,
  submitBlocked: false,
  onComposerInput: vi.fn(),
};
let mockVoice = { ...baseVoice };
const fakeEngine = { useComposerVoice: () => mockVoice, Recording: () => <div>RECORDING</div> };

function setup(props: Partial<React.ComponentProps<typeof Composer>> = {}) {
  const onSend = vi.fn();
  const onDraftChange = vi.fn();
  const rendered = render(
    <Composer
      inputRef={createRef<HTMLTextAreaElement>()}
      draft=""
      placeholder="Reply…"
      onDraftChange={onDraftChange}
      onSend={onSend}
      voice={{}}
      {...props}
    />,
  );
  return { onSend, onDraftChange, ...rendered };
}

beforeEach(() => {
  setExtensionsForTests([{ composerVoice: fakeEngine }]);
});

afterEach(() => {
  setExtensionsForTests(null);
  vi.clearAllMocks();
  mockVoice = { ...baseVoice, onComposerInput: vi.fn() };
});

describe('Composer (voice-first)', () => {
  beforeEach(() => {
    useEntitlements.setState({ loaded: true, team: true, operator: true });
  });

  it('is a plain text composer when the build has no dictation engine', () => {
    useEntitlements.setState({ loaded: true, team: false, operator: false, account: true });
    setExtensionsForTests([]);
    try {
      setup();
      expect(screen.getByRole('button', { name: 'Send' })).toBeInTheDocument();
      expect(screen.getByPlaceholderText('Reply…')).toBeInTheDocument();
    } finally {
      useEntitlements.setState({ account: false });
    }
  });

  it('shows the engine overlay while recording', () => {
    mockVoice = { ...baseVoice, recording: true };
    setup();
    expect(screen.getByText('RECORDING')).toBeInTheDocument();
  });

  it('is a plain text composer with a Send button on a public install', () => {
    useEntitlements.setState({ loaded: true, team: false, operator: false });
    const { container } = setup({ voiceHint: 'Hold to talk…', placeholder: 'Reply…' });
    expect(screen.getByRole('button', { name: /^send$/i })).toBeInTheDocument();
    expect(screen.queryByPlaceholderText('Hold to talk…')).toBeNull();
    expect(container.textContent).not.toMatch(/hold to talk/i);
  });

  it('offers voice to a free account', () => {
    useEntitlements.setState({ loaded: true, team: false, operator: false, account: true });
    setup({ voiceHint: 'Hold to talk…', placeholder: 'Reply…' });
    expect(screen.getByPlaceholderText('Hold to talk…')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^send$/i })).toBeNull();
  });

  it('empty voice composer is the recorder: no send/stop button, voiceHint placeholder', () => {
    const { container } = setup({ voiceHint: 'Hold to talk…' });
    expect(screen.queryByRole('button', { name: /^send$/i })).toBeNull();
    expect(screen.queryByRole('button', { name: /interrupt/i })).toBeNull();
    expect(screen.getByPlaceholderText('Hold to talk…')).toBeInTheDocument();
    expect(container.querySelector('.composer-primary-slot')).toBeInTheDocument();
  });

  it('typed content reveals the send button and the normal placeholder', () => {
    const { container } = setup({ draft: 'fix the bug', placeholder: 'Reply…', voiceHint: 'Hold to talk…' });
    const send = screen.getByRole('button', { name: /^send$/i });
    expect(send).toBeInTheDocument();
    expect(container.querySelector('.composer-primary-slot')).toContainElement(send);
    expect(screen.getByPlaceholderText('Reply…')).toBeInTheDocument();
  });

  it('clicking send fires onSend', () => {
    const { onSend } = setup({ draft: 'hello' });
    fireEvent.click(screen.getByRole('button', { name: /^send$/i }));
    expect(onSend).toHaveBeenCalledTimes(1);
  });

  it('inFlight shows Stop (interrupt) instead of Send and fires onStop', () => {
    const onStop = vi.fn();
    const { container } = setup({ draft: 'queued', inFlight: true, onStop });
    expect(screen.queryByRole('button', { name: /^send$/i })).toBeNull();
    const stop = screen.getByRole('button', { name: /interrupt/i });
    expect(container.querySelector('.composer-primary-slot')).toContainElement(stop);
    fireEvent.click(stop);
    expect(onStop).toHaveBeenCalledTimes(1);
  });

  it('shows suggestion ghost text only while the draft is empty', () => {
    const { container, rerender } = render(
      <Composer
        inputRef={createRef<HTMLTextAreaElement>()}
        draft=""
        placeholder="Reply…"
        suggestion="Continue by checking the failing test"
        onDraftChange={() => {}}
        onSend={() => {}}
        voice={{}}
      />,
    );
    expect(container.querySelector('.composer-ghost')).toHaveTextContent('Continue by checking the failing test');

    rerender(
      <Composer
        inputRef={createRef<HTMLTextAreaElement>()}
        draft="typed"
        placeholder="Reply…"
        suggestion="Continue by checking the failing test"
        onDraftChange={() => {}}
        onSend={() => {}}
        voice={{}}
      />,
    );
    expect(container.querySelector('.composer-ghost')).toBeNull();
  });

  it('non-voice composer always shows send, even when empty', () => {
    setup({ voice: undefined });
    expect(screen.getByRole('button', { name: /^send$/i })).toBeInTheDocument();
  });

  it('attach button forwards the picked file to onPickFile', () => {
    const onPickFile = vi.fn();
    setup({ onPickFile });
    const attach = screen.getByRole('button', { name: /attach image/i });
    expect(attach).toBeInTheDocument();
    const file = new File(['x'], 'shot.png', { type: 'image/png' });
    // The hidden <input type=file> is the sibling that the button click()s.
    const input = document.querySelector('input[type="file"]') as HTMLInputElement;
    fireEvent.change(input, { target: { files: [file] } });
    expect(onPickFile).toHaveBeenCalledWith(file);
  });

  it('omitting onPickFile hides the attach button', () => {
    setup({ onPickFile: undefined });
    expect(screen.queryByRole('button', { name: /attach image/i })).toBeNull();
  });

  // Raw-then-patch (voiceDeferCleanup) send gate — the whole point of the
  // feature: while the polish call is in flight (capped), raw dictation
  // can't be fired early.
  describe('raw-then-patch send gate', () => {
    it('while submitBlocked: bare Enter is swallowed and Send is disabled', () => {
      mockVoice = { ...baseVoice, submitBlocked: true, polishing: true, onComposerInput: vi.fn() };
      const { onSend } = setup({ draft: 'refactor the use effect hook please' });
      const input = screen.getByRole('textbox');
      fireEvent.keyDown(input, { key: 'Enter' });
      expect(onSend).not.toHaveBeenCalled(); // Enter did not submit
      const send = screen.getByRole('button', { name: /polishing/i });
      expect(send).toBeDisabled();
      fireEvent.click(send);
      expect(onSend).not.toHaveBeenCalled(); // click also gated
    });

    it('Shift+Enter still passes through the gate (newline, never submits)', () => {
      const onKeyDown = vi.fn();
      mockVoice = { ...baseVoice, submitBlocked: true, onComposerInput: vi.fn() };
      setup({ draft: 'multi line dictation here', onKeyDown });
      fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Enter', shiftKey: true });
      expect(onKeyDown).toHaveBeenCalledTimes(1); // forwarded, not swallowed
    });

    it('once unblocked: Enter submits and Send is enabled again', () => {
      mockVoice = { ...baseVoice, submitBlocked: false, onComposerInput: vi.fn() };
      const onKeyDown = vi.fn();
      const { onSend } = setup({ draft: 'ship it', onKeyDown });
      fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Enter' });
      expect(onKeyDown).toHaveBeenCalledTimes(1); // reaches the surface's submit handler
      const send = screen.getByRole('button', { name: /^send$/i });
      expect(send).not.toBeDisabled();
      fireEvent.click(send);
      expect(onSend).toHaveBeenCalledTimes(1);
    });

    it('typing notifies the hook so a user edit can cancel the pending patch', () => {
      mockVoice = { ...baseVoice, polishing: true, onComposerInput: vi.fn() };
      setup({ draft: 'raw text' });
      fireEvent.change(screen.getByRole('textbox'), { target: { value: 'raw text edited' } });
      expect(mockVoice.onComposerInput).toHaveBeenCalledWith('raw text edited');
    });
  });
});
