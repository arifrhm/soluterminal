import { act, renderHook } from '@testing-library/react';
import type { PointerEvent as ReactPointerEvent, TouchEvent as ReactTouchEvent } from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';

const session = vi.hoisted(() => ({
  snapshot: null as { sessionId: string } | null,
  start: vi.fn(),
  stop: vi.fn(),
}));

vi.mock('./voiceSession', () => ({
  voiceSession: {
    subscribe: () => () => {},
    getSnapshot: () => session.snapshot,
    start: (...args: unknown[]) => {
      session.start(...args);
      session.snapshot = { sessionId: 's1' };
      return true;
    },
    stop: (...args: unknown[]) => session.stop(...args),
    detach: () => {},
  },
}));

import { SHORT_PRESS_HINT, SHORT_PRESS_MS, useContinuousVoice } from './useContinuousVoice';

const pointer = (x = 10) =>
  ({
    pointerType: 'mouse',
    pointerId: 1,
    clientX: x,
    clientY: 10,
    preventDefault: () => {},
    currentTarget: { setPointerCapture: () => {} },
  }) as unknown as ReactPointerEvent<HTMLElement>;

const touch = () =>
  ({ touches: [{ clientX: 10, clientY: 10 }], preventDefault: () => {} }) as unknown as ReactTouchEvent<HTMLElement>;

function setup() {
  const onError = vi.fn();
  const { result } = renderHook(() =>
    useContinuousVoice({ acquireTarget: () => ({ paneId: 'p1' }), onText: () => true, onError }),
  );
  return { handlers: result.current.holdHandlers, start: result.current.start, onError };
}

beforeEach(() => {
  vi.useFakeTimers();
  session.snapshot = null;
  session.start.mockClear();
  session.stop.mockClear();
});
afterEach(() => vi.useRealTimers());

it('discards a click on the mic without uploading and explains hold-to-talk', () => {
  const { handlers, onError } = setup();
  act(() => handlers.onPointerDown!(pointer()));
  vi.advanceTimersByTime(SHORT_PRESS_MS - 100);
  act(() => handlers.onPointerUp!(pointer()));
  expect(session.stop).toHaveBeenCalledWith(true);
  expect(onError).toHaveBeenCalledWith(`${SHORT_PRESS_HINT} Or tap Alt+S to start and stop.`, 'warn');
});

it('transcribes a real hold as before', () => {
  const { handlers, onError } = setup();
  act(() => handlers.onPointerDown!(pointer()));
  vi.advanceTimersByTime(SHORT_PRESS_MS + 600);
  act(() => handlers.onPointerUp!(pointer()));
  expect(session.stop).toHaveBeenCalledWith(false);
  expect(onError).not.toHaveBeenCalled();
});

it('gives touch users the hint without a keyboard shortcut', () => {
  const { handlers, onError } = setup();
  act(() => handlers.onTouchStart!(touch()));
  act(() => handlers.onTouchEnd!(touch()));
  expect(session.stop).toHaveBeenCalledWith(true);
  expect(onError).toHaveBeenCalledWith(SHORT_PRESS_HINT, 'warn');
});

it('a quick click that stops a recording started elsewhere commits it', () => {
  const { handlers, start, onError } = setup();
  act(() => start()); // Alt+S and Space start through the same controller
  vi.advanceTimersByTime(5000);
  act(() => handlers.onPointerDown!(pointer()));
  act(() => handlers.onPointerUp!(pointer()));
  expect(session.start).toHaveBeenCalledTimes(1);
  expect(session.stop).toHaveBeenCalledWith(false);
  expect(session.stop).not.toHaveBeenCalledWith(true);
  expect(onError).not.toHaveBeenCalled();
});

it('keeps slide-to-cancel silent', () => {
  const { handlers, onError } = setup();
  act(() => handlers.onPointerDown!(pointer(10)));
  act(() => handlers.onPointerMove!(pointer(200)));
  act(() => handlers.onPointerUp!(pointer(200)));
  expect(session.stop).toHaveBeenCalledWith(true);
  expect(onError).not.toHaveBeenCalled();
});
