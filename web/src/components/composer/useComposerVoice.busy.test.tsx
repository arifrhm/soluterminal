import { emitVoicePart, voiceSettle } from '../../test/continuousVoiceHarness';
import { act, cleanup, render } from '@testing-library/react';
import { useRef } from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { useComposerVoice } from './useComposerVoice';
import { useStore } from '../../store';
import { api } from '../../api';
import { voiceSession } from '../../lib/voiceSession';
import { VoiceRequestError } from '../../lib/voiceRequest';
function Harness() {
  const ref = useRef<HTMLTextAreaElement>(null);
  useComposerVoice({ targetRef: ref, enabled: true, shortcutActive: true, shortcutPaneId: 'p1' });
  return <textarea ref={ref} />;
}
beforeEach(() => {
  useStore.getState().registerVoiceControl(null);
  useStore.setState({
    voiceLanguage: 'en',
    voiceHistory: [],
    voiceAutoSend: false,
    voiceDeferCleanup: false,
    voiceReadyTick: false,
  });
  vi.spyOn(api, 'voicePrewarm').mockImplementation(() => {});
  vi.useFakeTimers();
});
afterEach(() => {
  cleanup();
  voiceSession.cancel();
  vi.useRealTimers();
  vi.restoreAllMocks();
});
it('sends identical recording and pinned hint after busy, delivering once after stop', async () => {
  const transcribe = vi
    .spyOn(api, 'transcribe')
    .mockRejectedValueOnce(new VoiceRequestError('busy', 'busy'))
    .mockResolvedValue({ text: 'run the tests', quota: null });
  const view = render(<Harness />);
  act(() => useStore.getState().voiceControl!.start());
  await voiceSettle();
  await emitVoicePart();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(3000);
  });
  expect(transcribe).toHaveBeenCalledTimes(2);
  expect(transcribe.mock.calls[1][0]).toBe(transcribe.mock.calls[0][0]);
  expect(view.container.querySelector('textarea')!.value).toBe('');
  act(() => useStore.getState().voiceControl!.stop(false));
  await voiceSettle();
  expect(view.container.querySelector('textarea')!.value).toBe('run the tests');
});
it('keeps exhausted busy audio for explicit retry or discard without a dialog loop', async () => {
  const transcribe = vi
    .spyOn(api, 'transcribe')
    .mockRejectedValue(new VoiceRequestError('busy', 'busy'));
  render(<Harness />);
  act(() => useStore.getState().voiceControl!.start());
  await voiceSettle();
  await emitVoicePart();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(26000);
  });
  expect(transcribe).toHaveBeenCalledTimes(4);
  expect(voiceSession.getSnapshot()?.phase).toBe('recording');
  act(() => voiceSession.stop());
  await voiceSettle();
  expect(voiceSession.getSnapshot()?.phase).toBe('recovery');
  act(() => voiceSession.cancel());
  await voiceSettle();
  expect(useStore.getState().voiceHistory).toEqual([]);
});
