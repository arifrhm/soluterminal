import { microphone, emitVoicePart, voiceSettle } from '../test/continuousVoiceHarness';
import { act, cleanup, render } from '@testing-library/react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { api } from '../api';
import { useStore } from '../store';
import type { PaneInfo } from '../types';
import { voiceSession } from '../lib/voiceSession';
import { VoiceRequestError } from '../lib/voiceRequest';
import { DESKTOP_MIC_BLOCKED } from '../lib/voiceMicMessage';
import { VoiceButton } from './VoiceButton';
beforeEach(() => {
  useStore.getState().registerVoiceControl(null);
  useStore.setState({
    toasts: [],
    voiceLanguage: 'en',
    voiceHistory: [],
    voiceAutoSend: false,
    voiceDeferCleanup: false,
    voiceReadyTick: false,
    mainPresentation: { kind: 'workspace' },
    layout: { type: 'pane', paneId: 'p1' },
    focusedPaneId: 'p1',
    panes: { p1: { id: 'p1', pid: 1, cmd: ['bash'] } as PaneInfo },
  });
  vi.spyOn(api, 'voicePrewarm').mockImplementation(() => {});
});
afterEach(() => {
  cleanup();
  voiceSession.cancel();
  vi.useRealTimers();
  vi.restoreAllMocks();
  microphone.pending = false;
  delete (window as { __SOLU_TAURI__?: unknown }).__SOLU_TAURI__;
});
it.each([false, true])('maps microphone denial for desktop=%s', async (desktop) => {
  if (desktop) (window as { __SOLU_TAURI__?: unknown }).__SOLU_TAURI__ = true;
  microphone.pending = true;
  render(<VoiceButton headless />);
  act(() => useStore.getState().voiceControl!.start());
  microphone.fail?.(new Error('Mic blocked. Allow in site settings, then try again.'));
  await voiceSettle();
  expect(useStore.getState().toasts.at(-1)?.message).toBe(
    desktop ? DESKTOP_MIC_BLOCKED : 'Mic blocked. Allow in site settings, then try again.',
  );
});
it('retries busy with identical recording and language then checkpoints once', async () => {
  vi.useFakeTimers();
  const request = vi
    .spyOn(api, 'transcribe')
    .mockRejectedValueOnce(new VoiceRequestError('busy', 'busy'))
    .mockResolvedValue({ text: 'git status', quota: null });
  render(<VoiceButton headless />);
  act(() => useStore.getState().voiceControl!.start());
  await voiceSettle();
  await emitVoicePart();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(3000);
  });
  expect(request).toHaveBeenCalledTimes(2);
  expect(request.mock.calls[1][0]).toBe(request.mock.calls[0][0]);
  expect(request.mock.calls[1][1]?.language).toBe('en');
  expect(useStore.getState().voiceHistory).toHaveLength(1);
});
it('discard cancels bounded busy recovery without a late failure toast', async () => {
  vi.useFakeTimers();
  vi.spyOn(api, 'transcribe').mockRejectedValue(new VoiceRequestError('busy', 'busy'));
  render(<VoiceButton headless />);
  act(() => useStore.getState().voiceControl!.start());
  await voiceSettle();
  await emitVoicePart();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(26000);
  });
  expect(voiceSession.getSnapshot()?.phase).toBe('recording');
  act(() => voiceSession.stop());
  await voiceSettle();
  expect(voiceSession.getSnapshot()?.phase).toBe('recovery');
  act(() => voiceSession.cancel());
  await voiceSettle();
  expect(useStore.getState().toasts).toEqual([]);
  expect(useStore.getState().voiceHistory).toEqual([]);
});
