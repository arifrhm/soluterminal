import { afterEach, expect, it } from 'vitest';
import { currentRoutingScope } from '../api';
import type { PaneInfo } from '../types';
import { captureSessionGesture, clearTerminalSessions, ensureTerminalSession } from './terminalViewSessions';

afterEach(() => {
  // Leave the module's window state visible for the next test.
  window.dispatchEvent(new CustomEvent('solu:desktop-restored', { detail: { reason: 'resized', hideTracked: true } }));
  clearTerminalSessions();
});

const restored = (reason: string, hideTracked = true) =>
  window.dispatchEvent(new CustomEvent('solu:desktop-restored', { detail: { reason, ...(hideTracked ? { hideTracked } : {}) } }));
const backgrounded = (reason: string) =>
  window.dispatchEvent(new CustomEvent('solu:desktop-will-background', { detail: { reason } }));
const dictating = () => ensureTerminalSession(currentRoutingScope(), { id: 'dictating', pid: 1 } as PaneInfo);

it('keeps an in-flight gesture when the window only regains focus', () => {
  const session = dictating();
  const generation = session.generation;
  restored('focused');
  expect(session.generation).toBe(generation);
});

it('still invalidates a focus return from a shell that does not announce native hides', () => {
  const session = dictating();
  const generation = session.generation;
  restored('focused', false);
  expect(session.generation).toBeGreaterThan(generation);
});

it.each(['minimize', 'tray-hide', 'close-to-tray'])('invalidates gestures across a %s and return', (reason) => {
  const session = dictating();
  const gesture = captureSessionGesture(session);
  const generation = session.generation;
  backgrounded(reason);
  restored('focused');
  expect(session.generation).toBeGreaterThan(generation);
  expect(gesture()).toBe(false);
  expect(session.windowVisible).toBe(true);
});

it('still invalidates on resize and scale changes', () => {
  const session = dictating();
  const generation = session.generation;
  restored('resized');
  restored('scale-factor-changed');
  expect(session.generation).toBe(generation + 2);
});
