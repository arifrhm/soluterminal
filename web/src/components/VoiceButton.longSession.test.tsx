import { microphone, emitVoicePart, voiceSettle } from '../test/continuousVoiceHarness';
import { act, cleanup, render } from '@testing-library/react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { VoiceButton } from './VoiceButton';
import { useStore } from '../store';
import type { PaneInfo } from '../types';
import { api } from '../api';
import { voiceSession } from '../lib/voiceSession';

beforeEach(() => {
  useStore.getState().registerVoiceControl(null);
  useStore.setState({
    voiceHistory: [],
    voiceAutoSend: true,
    voiceDeferCleanup: false,
    voiceReadyTick: false,
    voiceLanguage: 'en',
    mainPresentation: { kind: 'workspace' },
    layout: { type: 'pane', paneId: 'p1' },
    focusedPaneId: 'p1',
    panes: {
      p1: { id: 'p1', pid: 10, cmd: ['claude'], hasClaude: true, title: 'Agent' } as PaneInfo,
    },
  });
  vi.spyOn(api, 'voicePrewarm').mockImplementation(() => {});
  vi.spyOn(api, 'transcribe').mockImplementation(async (_blob, opts) => ({
    text: 'part ' + opts?.language,
    rawText: 'um part',
    cleanup: 'ok',
    detectedLanguage: 'ms',
    quota: null,
  }));
  vi.spyOn(api, 'injectPane').mockResolvedValue({ bytes: 10 });
});
afterEach(() => {
  cleanup();
  voiceSession.cancel();
  vi.useRealTimers();
  vi.restoreAllMocks();
  microphone.pending = false;
});
it('injects one ordered aggregate and at most one agent Enter, with pinned language', async () => {
  vi.useFakeTimers();
  render(<VoiceButton headless />);
  act(() => useStore.getState().voiceControl!.start());
  await voiceSettle();
  await emitVoicePart(0);
  useStore.setState({ voiceLanguage: 'ms' });
  await emitVoicePart(1);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(125000);
  });
  expect(useStore.getState().voiceSession?.phase).toBe('recording');
  expect(api.injectPane).not.toHaveBeenCalled();
  act(() => useStore.getState().voiceControl!.stop(false));
  await voiceSettle();
  expect(api.injectPane).toHaveBeenCalledTimes(1);
  expect(vi.mocked(api.injectPane).mock.calls[0][1]).toBe('part en part en');
  await act(async () => {
    await vi.advanceTimersByTimeAsync(500);
  });
  expect(vi.mocked(api.injectPane).mock.calls.map((c) => c[1])).toEqual(['part en part en', '\r']);
  expect(useStore.getState().voiceHistory).toHaveLength(1);
  expect(useStore.getState().voiceHistory[0]).toMatchObject({
    delivered: true,
    raw: 'um part um part',
    detectedLanguage: 'ms',
  });
});
it('withdraws auto-send and preserves history if the destination changes', async () => {
  render(<VoiceButton headless />);
  act(() => useStore.getState().voiceControl!.start());
  await voiceSettle();
  await emitVoicePart();
  useStore.setState({ voiceAutoSend: false, focusedPaneId: null });
  act(() => microphone.options!.onProgress?.({ capturedMs: 125000, activeBytes: 0, segments: 1 }));
  expect(voiceSession.getSnapshot()?.phase).toBe('recording');
  act(() => useStore.getState().voiceControl!.stop(false));
  await voiceSettle();
  expect(api.injectPane).not.toHaveBeenCalled();
  expect(voiceSession.getSnapshot()?.phase).toBe('recovery');
  act(() => voiceSession.useAvailable());
  await voiceSettle();
  expect(useStore.getState().voiceHistory[0].delivered).toBe(false);
});
it('never presses Enter in a shell and retains failed injection in history', async () => {
  useStore.setState({
    panes: { p1: { id: 'p1', pid: 10, cmd: ['bash'], title: 'Shell' } as PaneInfo },
  });
  vi.mocked(api.injectPane).mockRejectedValueOnce(new Error('gone'));
  render(<VoiceButton headless />);
  act(() => useStore.getState().voiceControl!.start());
  await voiceSettle();
  await emitVoicePart();
  act(() => useStore.getState().voiceControl!.stop(false));
  await voiceSettle();
  expect(api.injectPane).toHaveBeenCalledTimes(1);
  expect(useStore.getState().voiceHistory[0].delivered).toBe(false);
});

it('pastes actual multiline dictation for shell review without pressing Enter', async () => {
  useStore.setState({
    voiceAutoSend: false,
    panes: { p1: { id: 'p1', pid: 10, cmd: ['bash'], title: 'Shell' } as PaneInfo },
  });
  vi.mocked(api.transcribe).mockResolvedValue({
    text: 'first line\nsecond line',
    cleanup: 'ok',
    quota: null,
  });
  render(<VoiceButton headless />);
  act(() => useStore.getState().voiceControl!.start());
  await voiceSettle();
  await emitVoicePart();
  act(() => useStore.getState().voiceControl!.stop(false));
  await voiceSettle();
  expect(api.injectPane).toHaveBeenCalledTimes(1);
  expect(vi.mocked(api.injectPane).mock.calls[0][1]).toBe(
    '\x1b[200~first line\nsecond line\x1b[201~',
  );
});
