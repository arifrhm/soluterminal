import { act, cleanup, fireEvent, render } from '@testing-library/react';
import { useRef } from 'react';
import { afterEach, expect, it, vi } from 'vitest';
import { useComposerVoice } from './useComposerVoice';

function Harness() {
  const targetRef = useRef<HTMLTextAreaElement>(null);
  const voice = useComposerVoice({ targetRef, enabled: true });
  return <textarea ref={targetRef} data-recording={voice.recording} {...voice.handlers} />;
}

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

it.each(['blur', 'release', 'unmount', 'embed hidden'])(
  'releases a late microphone stream after %s without starting a recorder',
  async (interruption) => {
    vi.useFakeTimers();
    let grant!: (stream: MediaStream) => void;
    const pending = new Promise<MediaStream>((resolve) => {
      grant = resolve;
    });
    const getUserMedia = vi.fn(() => pending);
    const trackStop = vi.fn();
    const recorder = vi.fn();
    vi.stubGlobal('navigator', { mediaDevices: { getUserMedia } });
    vi.stubGlobal('MediaRecorder', recorder);
    const view = render(<Harness />);
    const textarea = view.container.querySelector('textarea')!;
    fireEvent.touchStart(textarea, { touches: [{ clientX: 10, clientY: 10 }] });
    act(() => {
      vi.advanceTimersByTime(300);
    });
    expect(getUserMedia).toHaveBeenCalledOnce();

    if (interruption === 'unmount') view.unmount();
    else if (interruption === 'release') fireEvent.touchEnd(textarea);
    else
      act(() =>
        window.dispatchEvent(
          interruption === 'blur'
            ? new Event('blur')
            : new CustomEvent('solu:embed-visibility', { detail: { visible: false } }),
        ),
      );

    await act(async () => {
      grant({ getTracks: () => [{ stop: trackStop }] } as unknown as MediaStream);
      await pending;
    });
    expect(trackStop).toHaveBeenCalledOnce();
    expect(recorder).not.toHaveBeenCalled();
    expect(textarea.dataset.recording).toBe('false');
  },
);
