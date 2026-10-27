import { microphone, voiceSettle } from '../test/continuousVoiceHarness';
import { act, cleanup, fireEvent, render } from '@testing-library/react';
import { useRef } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useStore } from '../store';
import type { PaneInfo } from '../types';
import { VOICE_LEAD_IN_MS } from '../private/voice.slice';
import { VoiceButton } from '../components/VoiceButton';
import { useComposerVoice } from '../components/composer/useComposerVoice';
import { voiceSession } from './voiceSession';
import { api } from '../api';

// Exercise both public entry points through the continuous capture factory:
// a fresh install must not leave the microphone open between dictations.
function ComposerHarness() {
  const targetRef = useRef<HTMLTextAreaElement>(null);
  const voice = useComposerVoice({ targetRef, enabled: true });
  return <textarea ref={targetRef} {...voice.handlers} />;
}

beforeEach(() => {
  vi.useFakeTimers();
  microphone.options = null;
  useStore.getState().registerVoiceControl(null);
  useStore.setState({
    voiceReadyTick: false,
    mainPresentation: { kind: 'workspace' },
    layout: { type: 'pane', paneId: 'p1' },
    focusedPaneId: 'p1',
    panes: { p1: { id: 'p1', pid: 10, cmd: ['bash'], title: 'Shell' } as PaneInfo },
  });
  vi.spyOn(api, 'voicePrewarm').mockImplementation(() => {});
});

afterEach(() => {
  cleanup();
  act(() => voiceSession.cancel());
  useStore.getState().setVoiceKeepWarm(false);
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe('warm mic is opt-in', () => {
  it('starts off in a fresh install', () => {
    expect(useStore.getState().voiceKeepWarmMs).toBe(0);
  });

  describe.each(['VoiceButton', 'composer voice'] as const)('%s', (entry) => {
    it.each([
      [false, 0],
      [true, 120_000],
    ] as const)('passes the user opt-in %s to the actual capture factory', async (enabled, expected) => {
      useStore.getState().setVoiceKeepWarm(enabled);
      if (entry === 'VoiceButton') {
        render(<VoiceButton headless />);
        act(() => useStore.getState().voiceControl!.start());
      } else {
        const view = render(<ComposerHarness />);
        fireEvent.touchStart(view.container.querySelector('textarea')!, {
          touches: [{ clientX: 10, clientY: 10 }],
        });
        act(() => vi.advanceTimersByTime(300));
      }
      await voiceSettle();
      expect(microphone.options?.keepWarmMs).toBe(expected);
      expect(microphone.options?.leadInMs).toBe(VOICE_LEAD_IN_MS);
    });
  });
});
