import { emitVoicePart, voiceSettle } from '../../test/continuousVoiceHarness';
import { act, cleanup, render } from '@testing-library/react';
import { useRef } from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { useComposerVoice } from './useComposerVoice';
import { useStore } from '../../store';
import { api } from '../../api';
import { voiceSession } from '../../lib/voiceSession';
function Harness() {
  const ref = useRef<HTMLTextAreaElement>(null);
  useComposerVoice({ targetRef: ref, enabled: true, shortcutActive: true, shortcutPaneId: 'p1' });
  return <textarea ref={ref} />;
}
beforeEach(() => {
  useStore.getState().registerVoiceControl(null);
  useStore.setState({
    voiceHistory: [],
    voiceLanguage: 'en',
    voiceAutoSend: false,
    voiceDeferCleanup: true,
    voiceReadyTick: false,
  });
  vi.spyOn(api, 'voicePrewarm').mockImplementation(() => {});
});
afterEach(() => {
  cleanup();
  voiceSession.cancel();
  vi.restoreAllMocks();
});
it('preserves heard and cleaned comparison for deferred cleanup before final insertion', async () => {
  vi.spyOn(api, 'transcribe').mockResolvedValue({
    text: 'um Benjamin says lah please preserve the words',
    cleanup: 'deferred',
    quota: null,
  });
  vi.spyOn(api, 'cleanupVoice').mockResolvedValue({
    text: 'Benjamin says lah, please preserve the words.',
    cleanup: 'ok',
    cleanupProvider: 'local/claude',
  });
  const view = render(<Harness />);
  act(() => useStore.getState().voiceControl!.start());
  await voiceSettle();
  await emitVoicePart();
  expect(view.container.querySelector('textarea')!.value).toBe('');
  expect(useStore.getState().voiceHistory[0]).toMatchObject({
    raw: 'um Benjamin says lah please preserve the words',
    text: 'Benjamin says lah, please preserve the words.',
    delivered: false,
  });
  act(() => useStore.getState().voiceControl!.stop(false));
  await voiceSettle();
  expect(useStore.getState().voiceHistory).toHaveLength(1);
  expect(useStore.getState().voiceHistory[0].delivered).toBe(true);
});
it('retains raw history and never re-transcribes successful audio when cleanup fails', async () => {
  const transcribe = vi
    .spyOn(api, 'transcribe')
    .mockResolvedValue({ text: 'raw words that require some cleanup', cleanup: 'deferred', quota: null });
  const cleanupVoice = vi
    .spyOn(api, 'cleanupVoice')
    .mockRejectedValueOnce(new Error('offline'))
    .mockResolvedValue({ text: 'Raw words that require some cleanup.', cleanup: 'ok' });
  const view = render(<Harness />);
  act(() => useStore.getState().voiceControl!.start());
  await voiceSettle();
  await emitVoicePart();
  expect(useStore.getState().voiceHistory[0].raw).toBe('raw words that require some cleanup');
  expect(voiceSession.getSnapshot()?.phase).toBe('recording');
  act(() => useStore.getState().voiceControl!.stop(false));
  await voiceSettle();
  expect(transcribe).toHaveBeenCalledTimes(1);
  expect(cleanupVoice).toHaveBeenCalledTimes(1);
  expect(view.container.querySelector('textarea')!.value).toBe('raw words that require some cleanup');
  expect(useStore.getState().voiceHistory[0].delivered).toBe(true);
});
it('keeps raw and cleaned inline diagnostics in one history entry', async () => {
  useStore.setState({ voiceDeferCleanup: false });
  vi.spyOn(api, 'transcribe').mockResolvedValue({
    text: 'Clean words.',
    rawText: 'um clean words',
    cleanup: 'ok',
    requestedLanguage: 'en',
    detectedLanguage: 'ms',
    quota: null,
  });
  render(<Harness />);
  act(() => useStore.getState().voiceControl!.start());
  await voiceSettle();
  await emitVoicePart();
  act(() => useStore.getState().voiceControl!.stop(false));
  await voiceSettle();
  expect(useStore.getState().voiceHistory[0]).toMatchObject({
    raw: 'um clean words',
    text: 'Clean words.',
    requestedLanguage: 'en',
    detectedLanguage: 'ms',
  });
});
