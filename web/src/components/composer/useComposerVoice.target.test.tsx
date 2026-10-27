import { emitVoicePart, voiceSettle } from '../../test/continuousVoiceHarness';
import { act, cleanup, render } from '@testing-library/react';
import { useRef } from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { useComposerVoice } from './useComposerVoice';
import { useStore } from '../../store';
import { api } from '../../api';
import { voiceSession } from '../../lib/voiceSession';
let valid = true;
let generation = 1;
const send = vi.fn();
function Harness() {
  const ref = useRef<HTMLTextAreaElement>(null);
  useComposerVoice({
    targetRef: ref,
    enabled: true,
    shortcutActive: true,
    shortcutPaneId: 'p1',
    isTargetCurrent: () => valid,
    captureTargetGuard: () => {
      const captured = generation;
      return () => generation === captured;
    },
    onSubmitText: send,
  });
  return <textarea ref={ref} />;
}
beforeEach(() => {
  valid = true;
  generation = 1;
  send.mockReset();
  useStore.getState().registerVoiceControl(null);
  useStore.setState({
    voiceHistory: [],
    voiceAutoSend: true,
    voiceDeferCleanup: false,
    voiceReadyTick: false,
  });
  vi.spyOn(api, 'voicePrewarm').mockImplementation(() => {});
  vi.spyOn(api, 'transcribe').mockResolvedValue({ text: 'dictation', quota: null });
});
afterEach(() => {
  cleanup();
  voiceSession.cancel();
  vi.useRealTimers();
  vi.restoreAllMocks();
});
it('rejects stale target insertion while retaining the transcript', async () => {
  const view = render(<Harness />);
  act(() => useStore.getState().voiceControl!.start());
  await voiceSettle();
  await emitVoicePart();
  valid = false;
  act(() => useStore.getState().voiceControl!.stop(false));
  await voiceSettle();
  act(() => voiceSession.useAvailable());
  await voiceSettle();
  expect(view.container.querySelector('textarea')!.value).toBe('');
  expect(useStore.getState().voiceHistory[0].delivered).toBe(false);
});
it('rechecks placement after both deferred auto-send frames', async () => {
  vi.useFakeTimers();
  render(<Harness />);
  act(() => useStore.getState().voiceControl!.start());
  await voiceSettle();
  await emitVoicePart();
  act(() => useStore.getState().voiceControl!.stop(false));
  await voiceSettle();
  generation++;
  await act(async () => {
    await vi.advanceTimersByTimeAsync(50);
  });
  expect(send).not.toHaveBeenCalled();
});
it('never redirects a retained session to a replacement textarea', async () => {
  const first = render(<Harness />);
  act(() => useStore.getState().voiceControl!.start());
  await voiceSettle();
  await emitVoicePart();
  first.unmount();
  await voiceSettle();
  const replacement = render(<Harness />);
  act(() => voiceSession.useAvailable());
  await voiceSettle();
  expect(replacement.container.querySelector('textarea')!.value).toBe('');
});
