import { act } from '@testing-library/react';
import { vi } from 'vitest';
import type { ContinuousCaptureOptions } from 'soluvoice';

const microphone = vi.hoisted(() => ({
  options: null as ContinuousCaptureOptions | null,
  pending: false,
  ready: null as (() => void) | null,
  fail: null as ((error: Error) => void) | null,
  stop: vi.fn(async () => {}),
  dispose: vi.fn(),
}));
vi.mock('soluvoice', async (importOriginal) => ({
  ...(await importOriginal<typeof import('soluvoice')>()),
  createContinuousCapture: (options: ContinuousCaptureOptions) => {
    microphone.options = options;
    return {
      start: () =>
        microphone.pending
          ? new Promise<void>((resolve, reject) => {
              microphone.ready = resolve;
              microphone.fail = reject;
            })
          : Promise.resolve(),
      stop: microphone.stop,
      cancel: microphone.dispose,
      dispose: microphone.dispose,
      subscribe: () => () => {},
    };
  },
}));
export async function voiceSettle() {
  await act(async () => {
    for (let n = 0; n < 20; n++) await Promise.resolve();
  });
}
export async function emitVoicePart(index = 0) {
  const { encodeWav } = await import('soluvoice');
  await act(async () => {
    microphone.options!.onSegment({
      index,
      blob: encodeWav(new Int16Array(16000).fill(1000)),
      frames: 16000,
      durationMs: 1000,
      endFrame: (index + 1) * 16000,
    });
  });
  await voiceSettle();
}

export { microphone };
