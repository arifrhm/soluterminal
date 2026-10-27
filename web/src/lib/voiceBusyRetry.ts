import { useStore } from '../store';
import { confirmDialog } from './dialog';
import { VoiceRequestError } from './voiceRequest';

// "Voice is busy" means Groq's shared rate limit, which refills within
// seconds (on Groq's Free plan, 20 requests a minute for everyone). The
// recording is still in memory, so it is sent again instead of thrown away:
// a few automatic tries within about 30 seconds, then the user chooses
// Retry or Discard. The cloud refunds the daily allowance on busy, so a
// retry is not counted twice. Retrying happens inside one transcribe call,
// so the recorder delivers the text once, however many tries it took.

/** Waits between automatic retries: about 26 s in all. */
export const VOICE_BUSY_RETRY_DELAYS_MS: readonly number[] = [3_000, 8_000, 15_000];

/** Each upload gets the stall watchdog the recorder used to apply to the call. */
export const VOICE_ATTEMPT_TIMEOUT_MS = 30_000;

/** The recorder's own watchdog has to outlast the retries and the user's
 *  Retry/Discard choice; every attempt carries its own timeout instead. */
export const VOICE_TRANSCRIBE_WATCHDOG_MS = 15 * 60_000;

/** The user discarded a recording that voice was too busy to transcribe. */
export class VoiceDiscardedError extends Error {
  constructor() {
    super('Recording discarded');
    this.name = 'VoiceDiscardedError';
  }
}

export interface BusyRetryHooks {
  /** An automatic retry is about to wait `delayMs` (attempt counts from 1). */
  onRetrying?: (attempt: number, delayMs: number) => void;
  /** Every automatic retry was busy: resolve true to try again, false to discard. */
  askRetry?: () => Promise<boolean>;
  bounded?: boolean;
  delaysMs?: readonly number[];
  attemptTimeoutMs?: number;
}

function isBusy(err: unknown): boolean {
  return err instanceof VoiceRequestError && err.code === 'busy';
}

function abortError(signal: AbortSignal): unknown {
  return signal.reason ?? new DOMException('Aborted', 'AbortError');
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) {
      reject(abortError(signal));
      return;
    }
    const onAbort = () => {
      clearTimeout(timer);
      reject(abortError(signal));
    };
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', onAbort);
      resolve();
    }, ms);
    signal.addEventListener('abort', onAbort, { once: true });
  });
}

/** Runs one attempt with its own stall timeout, still cancelled by `signal`. */
async function runAttempt<R>(attempt: (signal: AbortSignal) => Promise<R>, signal: AbortSignal, timeoutMs: number): Promise<R> {
  const ac = new AbortController();
  let timedOut = false;
  const onAbort = () => ac.abort(abortError(signal));
  if (signal.aborted) onAbort();
  else signal.addEventListener('abort', onAbort, { once: true });
  const timer = setTimeout(() => {
    timedOut = true;
    ac.abort(new DOMException('Timed out', 'TimeoutError'));
  }, timeoutMs);
  let rejectAbort: (() => void) | undefined;
  try {
    if (signal.aborted) throw abortError(signal);
    const aborted = new Promise<never>((_, reject) => {
      rejectAbort = () => reject(abortError(ac.signal));
      ac.signal.addEventListener('abort', rejectAbort, { once: true });
    });
    return await Promise.race([attempt(ac.signal), aborted]);
  } catch (err) {
    // Worded without "timeout" so the recorder shows this message rather
    // than its own, which would quote the long overall watchdog.
    if (timedOut && !signal.aborted) {
      throw new Error(`Voice timed out after ${Math.round(timeoutMs / 1000)}s — network stalled or server hung`);
    }
    throw err;
  } finally {
    clearTimeout(timer);
    if (rejectAbort) ac.signal.removeEventListener('abort', rejectAbort);
    signal.removeEventListener('abort', onAbort);
  }
}

/** Calls `attempt` until it succeeds, retrying only "voice is busy" answers. */
export async function withBusyRetry<R>(
  attempt: (signal: AbortSignal) => Promise<R>,
  outer: AbortSignal | undefined,
  hooks: BusyRetryHooks,
): Promise<R> {
  const signal = outer ?? new AbortController().signal;
  const delays = hooks.delaysMs ?? VOICE_BUSY_RETRY_DELAYS_MS;
  const timeoutMs = hooks.attemptTimeoutMs ?? VOICE_ATTEMPT_TIMEOUT_MS;
  for (;;) {
    for (let i = 0; ; i++) {
      try {
        return await runAttempt(attempt, signal, timeoutMs);
      } catch (err) {
        if (!isBusy(err) || signal.aborted) throw err;
        if (i >= delays.length) { if (hooks.bounded) throw err; break; }
        hooks.onRetrying?.(i + 1, delays[i]);
        await sleep(delays[i], signal);
      }
    }
    if (!(await hooks.askRetry?.())) throw new VoiceDiscardedError();
    if (signal.aborted) throw abortError(signal);
  }
}

/** withBusyRetry with the app's "Busy, retrying…" toast and Retry/Discard dialog. */
export function transcribeKeepingAudio<R>(attempt: (signal: AbortSignal) => Promise<R>, signal: AbortSignal | undefined): Promise<R> {
  return withBusyRetry(attempt, signal, {
    onRetrying: (_attempt, delayMs) => {
      useStore.getState().toast('Voice is busy, retrying… Your recording is kept.', {
        kind: 'info',
        ttlMs: delayMs + 2_000,
        logToRecent: false,
      });
    },
    askRetry: () =>
      confirmDialog({
        title: 'Voice is still busy',
        message: 'Your recording is kept. Send it again, or discard it.',
        confirmLabel: 'Retry',
        cancelLabel: 'Discard',
      }),
  });
}
