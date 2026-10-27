import { microphone, voiceSettle } from '../../test/continuousVoiceHarness';
import { act, cleanup, render } from '@testing-library/react';
import { useRef } from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { useStore } from '../../store';
import type { VoiceControl } from '../../private/voice.slice';
import { voiceSession } from '../../lib/voiceSession';
import { useComposerVoice } from './useComposerVoice';
function Harness({ active, paneId = 'focus-1' }: { active: boolean; paneId?: string }) {
  const targetRef = useRef<HTMLTextAreaElement>(null);
  useComposerVoice({ targetRef, enabled: true, shortcutActive: active, shortcutPaneId: paneId });
  return <textarea ref={targetRef} />;
}
beforeEach(() => {
  useStore.getState().registerVoiceControl(null);
  useStore.setState({ voiceReadyTick: false });
});
afterEach(() => {
  cleanup();
  voiceSession.cancel();
  useStore.getState().registerVoiceControl(null);
  microphone.pending = false;
});
it('registers the focused composer and mirrors its pinned pane session', async () => {
  render(<Harness active />);
  const ctrl = useStore.getState().voiceControl!;
  expect(ctrl).toBeTruthy();
  act(() => ctrl.start());
  await voiceSettle();
  expect(useStore.getState().voiceSession).toMatchObject({ paneId: 'focus-1', phase: 'recording' });
  act(() => ctrl.stop(false));
  await voiceSettle();
  expect(useStore.getState().voiceSession).toBeNull();
});
it('restores the terminal fallback when the idle composer loses focus', () => {
  const fallback: VoiceControl = { start: vi.fn(), stop: vi.fn() };
  useStore.getState().registerVoiceControl(fallback);
  const view = render(<Harness active />);
  expect(useStore.getState().voiceControl).not.toBe(fallback);
  view.rerender(<Harness active={false} />);
  expect(useStore.getState().voiceControl).toBe(fallback);
});
it('does not cover another pane recorder merely because focus moved here', () => {
  const active: VoiceControl = { start: vi.fn(), stop: vi.fn() };
  useStore.getState().registerVoiceControl(active);
  useStore.getState().setVoiceSession({ paneId: 'old', phase: 'recording' });
  render(<Harness active paneId="new" />);
  expect(useStore.getState().voiceControl).toBe(active);
});
