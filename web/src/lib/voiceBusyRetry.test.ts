import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useStore } from '../store';
import { transcribeKeepingAudio, VoiceDiscardedError, withBusyRetry } from './voiceBusyRetry';
import { VoiceRequestError } from './voiceRequest';

const busy = () => new VoiceRequestError('Voice is busy right now. Try again in a few seconds.', 'busy');

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
  useStore.setState({ dialog: null, toasts: [] });
});

describe('withBusyRetry', () => {
  it('sends once when the first try works', async () => {
    const attempt = vi.fn().mockResolvedValue('ok');
    const askRetry = vi.fn();
    await expect(withBusyRetry(attempt, undefined, { askRetry })).resolves.toBe('ok');
    expect(attempt).toHaveBeenCalledOnce();
    expect(askRetry).not.toHaveBeenCalled();
  });

  it('retries busy answers with backoff and returns the one result', async () => {
    const attempt = vi.fn().mockRejectedValueOnce(busy()).mockRejectedValueOnce(busy()).mockResolvedValue('text');
    const onRetrying = vi.fn();
    const result = withBusyRetry(attempt, undefined, { askRetry: vi.fn(), onRetrying, delaysMs: [3_000, 8_000] });
    await vi.advanceTimersByTimeAsync(2_999);
    expect(attempt).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(attempt).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(8_000);
    await expect(result).resolves.toBe('text');
    expect(attempt).toHaveBeenCalledTimes(3);
    expect(onRetrying.mock.calls).toEqual([[1, 3_000], [2, 8_000]]);
  });

  it('asks after the automatic retries and tries again on Retry', async () => {
    const attempt = vi.fn().mockRejectedValueOnce(busy()).mockRejectedValueOnce(busy()).mockResolvedValue('later');
    const askRetry = vi.fn().mockResolvedValue(true);
    const result = withBusyRetry(attempt, undefined, { askRetry, delaysMs: [1_000] });
    await vi.advanceTimersByTimeAsync(1_000);
    await expect(result).resolves.toBe('later');
    expect(askRetry).toHaveBeenCalledOnce();
    expect(attempt).toHaveBeenCalledTimes(3);
  });

  it('throws VoiceDiscardedError on Discard', async () => {
    const attempt = vi.fn().mockRejectedValue(busy());
    const result = withBusyRetry(attempt, undefined, { askRetry: vi.fn().mockResolvedValue(false), delaysMs: [] });
    await expect(result).rejects.toBeInstanceOf(VoiceDiscardedError);
    expect(attempt).toHaveBeenCalledOnce();
  });

  it('does not retry other failures', async () => {
    const quota = new VoiceRequestError("You've used today's voice dictations.", 'quota');
    const attempt = vi.fn().mockRejectedValue(quota);
    await expect(withBusyRetry(attempt, undefined, { askRetry: vi.fn() })).rejects.toBe(quota);
    expect(attempt).toHaveBeenCalledOnce();
  });

  it('stops waiting when the recorder aborts', async () => {
    const ac = new AbortController();
    const attempt = vi.fn().mockRejectedValue(busy());
    const result = withBusyRetry(attempt, ac.signal, { askRetry: vi.fn(), delaysMs: [10_000] });
    const settled = expect(result).rejects.toThrow('gone');
    await vi.advanceTimersByTimeAsync(100);
    ac.abort(new Error('gone'));
    await settled;
    expect(attempt).toHaveBeenCalledOnce();
  });

  it('gives each upload its own stall timeout', async () => {
    const attempt = vi.fn((signal: AbortSignal) => new Promise<string>((_resolve, reject) => {
      signal.addEventListener('abort', () => reject(signal.reason));
    }));
    const result = withBusyRetry(attempt, undefined, { askRetry: vi.fn(), attemptTimeoutMs: 30_000 });
    const settled = expect(result).rejects.toThrow('Voice timed out after 30s');
    await vi.advanceTimersByTimeAsync(30_000);
    await settled;
  });
});

describe('transcribeKeepingAudio', () => {
  it('shows a retrying toast, then a Retry/Discard choice that keeps the audio', async () => {
    const attempt = vi.fn().mockRejectedValue(busy());
    const result = transcribeKeepingAudio(attempt, undefined);
    const settled = expect(result).rejects.toBeInstanceOf(VoiceDiscardedError);
    await vi.advanceTimersByTimeAsync(0);
    expect(useStore.getState().toasts.at(-1)?.message).toBe('Voice is busy, retrying… Your recording is kept.');
    await vi.advanceTimersByTimeAsync(26_000);
    expect(attempt).toHaveBeenCalledTimes(4);
    const dialog = useStore.getState().dialog;
    expect(dialog).toMatchObject({ kind: 'confirm', confirmLabel: 'Retry', cancelLabel: 'Discard' });
    useStore.getState().closeDialog(false);
    await settled;
  });
});

it('bounded mode stops after four busy attempts without asking or looping', async () => {
  vi.useFakeTimers();
  const attempt = vi.fn().mockRejectedValue(new VoiceRequestError('busy', 'busy'));
  const askRetry = vi.fn();
  const result = withBusyRetry(attempt, undefined, { bounded: true, askRetry }).catch(e => e);
  await vi.advanceTimersByTimeAsync(26000);
  expect(await result).toMatchObject({code:'busy'}); expect(attempt).toHaveBeenCalledTimes(4); expect(askRetry).not.toHaveBeenCalled();
  vi.useRealTimers();
});

it('bounds an attempt whose transport ignores AbortSignal', async () => {
  const attempt = vi.fn(() => new Promise<string>(() => {}));
  const result = withBusyRetry(attempt, undefined, { bounded: true, attemptTimeoutMs: 100 });
  const checked = expect(result).rejects.toThrow('Voice timed out after');
  await vi.advanceTimersByTimeAsync(100);
  await checked;
});
