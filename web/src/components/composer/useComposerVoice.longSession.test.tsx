import { microphone, emitVoicePart, voiceSettle } from '../../test/continuousVoiceHarness';
import { act, cleanup, fireEvent, render } from '@testing-library/react';
import { useRef } from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { useComposerVoice } from './useComposerVoice';
import { useStore } from '../../store';
import { api } from '../../api';
import { voiceSession } from '../../lib/voiceSession';

let current = true;
const submit = vi.fn();
function Harness() {
  const ref = useRef<HTMLTextAreaElement>(null);
  const voice = useComposerVoice({
    targetRef: ref,
    enabled: true,
    shortcutPaneId: 'p1',
    shortcutActive: true,
    onSubmitText: submit,
    isTargetCurrent: () => current,
  });
  return (
    <textarea ref={ref} {...voice.handlers} data-phase={voice.phase} data-lock={voice.handsFree} />
  );
}
beforeEach(() => {
  current = true;
  submit.mockReset();
  useStore.getState().registerVoiceControl(null);
  useStore.setState({
    voiceHistory: [],
    voiceLanguage: 'en',
    voiceReadyTick: false,
    voiceAutoSend: true,
    voiceDeferCleanup: false,
  });
  vi.spyOn(api, 'voicePrewarm').mockImplementation(() => {});
  vi.spyOn(api, 'transcribe').mockResolvedValue({
    text: 'hello',
    rawText: 'um hello',
    cleanup: 'ok',
    quota: null,
  });
});
afterEach(() => {
  cleanup();
  voiceSession.cancel();
  vi.restoreAllMocks();
  vi.useRealTimers();
  microphone.pending = false;
});
it('holds and locks through rollovers, inserts once and schedules one final submit', async () => {
  vi.useFakeTimers();
  const view = render(<Harness />);
  const textarea = view.container.querySelector('textarea')!;
  fireEvent.touchStart(textarea, { touches: [{ clientX: 100, clientY: 100 }] });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(300);
  });
  fireEvent.touchMove(textarea, { touches: [{ clientX: 100, clientY: 40 }] });
  fireEvent.touchEnd(textarea);
  expect(textarea.dataset.lock).toBe('true');
  await emitVoicePart();
  await emitVoicePart(1);
  expect(textarea.value).toBe('');
  expect(submit).not.toHaveBeenCalled();
  act(() => useStore.getState().voiceControl!.stop(false));
  await voiceSettle();
  expect(textarea.value).toBe('hello hello');
  await act(async () => {
    await vi.advanceTimersByTimeAsync(50);
  });
  expect(submit).toHaveBeenCalledOnce();
});
it('checkpoints history before rejecting a stale target and retains recovery after disposal', async () => {
  const view = render(<Harness />);
  act(() => useStore.getState().voiceControl!.start());
  await voiceSettle();
  await emitVoicePart();
  current = false;
  view.unmount();
  await voiceSettle();
  expect(voiceSession.getSnapshot()?.phase).toBe('recovery');
  act(() => voiceSession.useAvailable());
  await voiceSettle();
  expect(useStore.getState().voiceHistory[0]).toMatchObject({ text: 'hello', delivered: false });
  expect(submit).not.toHaveBeenCalled();
});
it('tap focuses, release during initialization cancels, and late completion never inserts', async () => {
  microphone.pending = true;
  const view = render(<Harness />);
  const el = view.container.querySelector('textarea')!;
  fireEvent.touchStart(el, { touches: [{ clientX: 10, clientY: 10 }] });
  fireEvent.touchEnd(el);
  expect(document.activeElement).toBe(el);
  expect(voiceSession.getSnapshot()).toBeNull();
  act(() => useStore.getState().voiceControl!.start());
  act(() => useStore.getState().voiceControl!.stop(false));
  microphone.ready?.();
  await voiceSettle();
  expect(voiceSession.getSnapshot()).toBeNull();
  expect(el.value).toBe('');
});
