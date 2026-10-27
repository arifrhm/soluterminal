import { act, cleanup, fireEvent, render } from '@testing-library/react';
import { useRef } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useComposerVoice } from './useComposerVoice';

const voiceMock = vi.hoisted(() => ({
  phase: 'idle' as 'idle' | 'starting' | 'recording' | 'transcribing',
  start: vi.fn(),
  stop: vi.fn(),
}));

vi.mock('../../lib/useContinuousVoice', () => ({
  useContinuousVoice: vi.fn(() => ({
    phase: voiceMock.phase,
    start: voiceMock.start,
    stop: voiceMock.stop,
    preserve: () => voiceMock.stop(false),
    progress: null,
  })),
}));

function Harness() {
  const targetRef = useRef<HTMLTextAreaElement>(null);
  const voice = useComposerVoice({ targetRef, enabled: true });
  return <textarea ref={targetRef} data-hands-free={voice.handsFree} {...voice.handlers} />;
}

function pointer(
  target: Document | Element,
  type: string,
  pointerId = 1,
  coords = { clientX: 10, clientY: 100 },
) {
  const event = new MouseEvent(type, { bubbles: true, cancelable: true, ...coords });
  Object.defineProperties(event, {
    pointerId: { value: pointerId },
    pointerType: { value: 'mouse' },
  });
  fireEvent(target, event);
}

function setup() {
  vi.useFakeTimers();
  const view = render(<Harness />);
  const textarea = view.container.querySelector('textarea')!;
  pointer(textarea, 'pointerdown');
  return { view, textarea };
}

const interruptions = [
  ['window blur', () => window.dispatchEvent(new Event('blur'))],
  [
    'document hidden',
    () => {
      vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
      document.dispatchEvent(new Event('visibilitychange'));
    },
  ],
  [
    'embed hidden',
    () =>
      window.dispatchEvent(
        new CustomEvent('solu:embed-visibility', { detail: { visible: false } }),
      ),
  ],
  ['document pointer cancellation', () => pointer(document, 'pointercancel')],
] as const;

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
  voiceMock.phase = 'idle';
  voiceMock.start.mockReset();
  voiceMock.stop.mockReset();
});

describe('useComposerVoice lost gesture cleanup', () => {
  it.each(interruptions)(
    'cancels a pending hold on %s and permits a fresh gesture',
    (_name, interrupt) => {
      const { textarea } = setup();
      act(() => {
        interrupt();
      });
      act(() => {
        vi.advanceTimersByTime(400);
      });
      expect(voiceMock.start).not.toHaveBeenCalled();
      pointer(textarea, 'pointerup');
      expect(document.activeElement).not.toBe(textarea);
      pointer(textarea, 'pointerdown');
      act(() => {
        vi.advanceTimersByTime(300);
      });
      expect(voiceMock.start).toHaveBeenCalledOnce();
    },
  );

  it.each(interruptions)('cancels microphone initialization on %s', (_name, interrupt) => {
    setup();
    act(() => {
      vi.advanceTimersByTime(300);
    });
    expect(voiceMock.start).toHaveBeenCalledOnce();
    // Permission is unresolved: the voice library still reports idle.
    act(() => {
      interrupt();
    });
    expect(voiceMock.stop).toHaveBeenCalledWith(true);
  });

  // Leaving the window mid-recording sends what was said, matching App's
  // Alt+S blur rule, so the two blur handlers can never disagree.
  it.each(interruptions.slice(0, 3))(
    'sends an active hold for transcription on %s',
    (_name, interrupt) => {
      const { view } = setup();
      act(() => {
        vi.advanceTimersByTime(300);
      });
      voiceMock.phase = 'recording';
      view.rerender(<Harness />);
      act(() => {
        interrupt();
      });
      expect(voiceMock.stop).toHaveBeenCalledWith(false);
      expect(voiceMock.stop).not.toHaveBeenCalledWith(true);
    },
  );

  it('discards an active hold when its own pointer is cancelled', () => {
    const { view } = setup();
    act(() => {
      vi.advanceTimersByTime(300);
    });
    voiceMock.phase = 'recording';
    view.rerender(<Harness />);
    act(() => {
      pointer(document, 'pointercancel');
    });
    expect(voiceMock.stop).toHaveBeenCalledWith(true);
  });

  it('sends a locked hands-free recording when the window loses focus', () => {
    const { view, textarea } = setup();
    act(() => {
      vi.advanceTimersByTime(300);
    });
    pointer(textarea, 'pointermove', 1, { clientX: 10, clientY: 0 });
    pointer(textarea, 'pointerup');
    voiceMock.phase = 'recording';
    view.rerender(<Harness />);
    voiceMock.stop.mockClear();
    act(() => window.dispatchEvent(new Event('blur')));
    expect(voiceMock.stop).toHaveBeenCalledTimes(1);
    expect(voiceMock.stop).toHaveBeenCalledWith(false);
    expect(textarea.dataset.handsFree).toBe('false');
  });

  it('arms cancel when sliding left, not right', () => {
    function SlideHarness() {
      const targetRef = useRef<HTMLTextAreaElement>(null);
      const voice = useComposerVoice({ targetRef, enabled: true });
      return <textarea ref={targetRef} data-slide={voice.slide} {...voice.handlers} />;
    }
    vi.useFakeTimers();
    const view = render(<SlideHarness />);
    const textarea = view.container.querySelector('textarea')!;
    pointer(textarea, 'pointerdown', 1, { clientX: 200, clientY: 100 });
    act(() => {
      vi.advanceTimersByTime(300);
    });
    pointer(textarea, 'pointermove', 1, { clientX: 300, clientY: 100 });
    expect(textarea.dataset.slide).toBe('none');
    pointer(textarea, 'pointermove', 1, { clientX: 100, clientY: 100 });
    expect(textarea.dataset.slide).toBe('cancel');
    pointer(textarea, 'pointerup');
    expect(voiceMock.stop).toHaveBeenCalledWith(true);
  });

  it('ignores another pointer cancellation and visible notifications', () => {
    setup();
    pointer(document, 'pointercancel', 2);
    act(() =>
      window.dispatchEvent(new CustomEvent('solu:embed-visibility', { detail: { visible: true } })),
    );
    act(() => {
      vi.advanceTimersByTime(300);
    });
    expect(voiceMock.start).toHaveBeenCalledOnce();
    expect(voiceMock.stop).not.toHaveBeenCalled();
  });

  it('releases microphone initialization even before recording phase is published', () => {
    const { textarea } = setup();
    act(() => {
      vi.advanceTimersByTime(300);
    });
    pointer(textarea, 'pointerup');
    expect(voiceMock.stop).toHaveBeenCalledWith(false);
  });

  it('keeps a locked recording through unrelated pointer cancellation, then cancels on hiding', () => {
    const { textarea } = setup();
    act(() => {
      vi.advanceTimersByTime(300);
    });
    pointer(textarea, 'pointermove', 1, { clientX: 10, clientY: 0 });
    pointer(textarea, 'pointerup');
    expect(textarea.dataset.handsFree).toBe('true');
    pointer(document, 'pointercancel');
    expect(voiceMock.stop).not.toHaveBeenCalled();
    act(() =>
      window.dispatchEvent(
        new CustomEvent('solu:embed-visibility', { detail: { visible: false } }),
      ),
    );
    expect(voiceMock.stop).toHaveBeenCalledWith(true);
    expect(textarea.dataset.handsFree).toBe('false');
  });

  it('cleans up a pending microphone request and listeners on unmount', () => {
    const { view } = setup();
    act(() => {
      vi.advanceTimersByTime(300);
    });
    view.unmount();
    expect(voiceMock.stop).not.toHaveBeenCalled();
    voiceMock.stop.mockClear();
    act(() => window.dispatchEvent(new Event('blur')));
    expect(voiceMock.stop).not.toHaveBeenCalled();
  });

  it('clears the hold timer on unmount', () => {
    const { view } = setup();
    view.unmount();
    act(() => {
      vi.advanceTimersByTime(400);
    });
    expect(voiceMock.start).not.toHaveBeenCalled();
  });

  it('does not stop an already submitted transcription when hidden', () => {
    voiceMock.phase = 'transcribing';
    render(<Harness />);
    act(() => window.dispatchEvent(new Event('blur')));
    expect(voiceMock.stop).not.toHaveBeenCalled();
  });
});
