import { useEffect, useRef, useState } from 'react';
import { captureLive, openCapture, type Capture } from './capture.js';
import { pickMimeType } from './mime.js';
import type { CleanupStatus, TranscribeFn, VoicePhase } from './types.js';

// Shortest recording we'll bother transcribing. Anything below this is
// almost certainly an accidental tap — sending it wastes a Whisper call
// and produces noise like "you" or "thank you" from Whisper's priors.
const DEFAULT_MIN_RECORD_MS = 250;

// Distance in pixels the user must drag off the hold target before
// release flips from "commit and transcribe" to "discard". Matches the
// WhatsApp/iMessage voice-memo gesture. 80px is roughly three average
// fingertip widths — far enough that a jittery held finger won't arm
// cancel, short enough that a deliberate slide is quick.
const DEFAULT_SLIDE_CANCEL_PX = 80;

// Hard ceiling on how long we'll wait for a transcription to come back
// before giving up, surfacing an error, and unlocking the button.
// Without this, a stalled fetch (network blip, cold start, DNS failure)
// pins phase at 'transcribing' forever and the user can't retry without
// a page reload. Whisper-via-Groq is usually <2s and LLM cleanup adds
// <8s; 30s means something's actually broken.
const DEFAULT_TRANSCRIBE_TIMEOUT_MS = 30_000;

export interface VoiceInfo<T> {
  cleanup: CleanupStatus;
  /** The target pinned at record-start (see acquireTarget). */
  target: T;
  /**
   * ms from start() to the recorder actually capturing. Anything the
   * user said in this window before the first press of a burst is lost
   * (a reused warm mic makes it ~0).
   */
  startDelayMs: number;
  /** True when this recording reused a still-open (warm) mic. */
  warm: boolean;
}

export interface UseVoiceOptions<T = void> {
  /** Sends audio out and returns text. See createEndpointTranscriber. */
  transcribe: TranscribeFn;
  /** Receives the final transcript. Delivery is entirely the app's job. */
  onText: (text: string, info: VoiceInfo<T>) => void | Promise<void>;
  /**
   * Called at record-start to pin where the transcript should land.
   * Return null to refuse recording (no valid target focused). The
   * returned value is passed back untouched in onText's info.target —
   * pinning at start (not at delivery) is deliberate: the user may
   * navigate elsewhere during the multi-second record+transcribe
   * window, and the text must land where they started dictating.
   * Omit if your app has a single fixed destination.
   */
  acquireTarget?: () => T | null;
  /**
   * Disambiguation context for the cleanup LLM, captured at
   * record-start (parallelizes with mic init; capturing at stop would
   * serialize ahead of the upload and add dead time).
   */
  getContext?: (target: T) => string | undefined;
  /** Whisper language hint, e.g. "en". Empty/undefined = auto-detect. */
  language?: string;
  /** Surfaced for toasts: mic errors, timeouts, empty transcripts. */
  onError?: (message: string, kind: 'error' | 'warn') => void;
  /**
   * Fired on phase transitions and slide-to-cancel arm/disarm — drive
   * "Listening… / release to cancel" chips from this. null target when
   * idle.
   */
  onSession?: (s: { phase: VoicePhase; target: T | null; aboutToCancel: boolean }) => void;
  /**
   * Blur the active element when recording starts (default true).
   * Mobile browsers see "focused input + touch" and pop the virtual
   * keyboard over the recording UI; blurring first stops that. Turn
   * off when dictating into a visible input that must keep its cursor.
   */
  blurOnStart?: boolean | ((target: T) => boolean);
  minRecordMs?: number;
  slideCancelPx?: number;
  timeoutMs?: number;
  /**
   * Keep the mic open this long after a recording ends, so the next
   * press in a burst starts instantly instead of waiting for the device
   * (and its gain control) to come up again. Nothing is recorded while
   * warm; the OS mic indicator stays on. Released early when the page
   * is hidden or unmounted. 0 (default) = release right away.
   */
  keepWarmMs?: number;
  /**
   * Record through a delay line of this length. Every clip then starts
   * with this much audio from just before the press: silence on a cold
   * mic (Whisper misreads a word that starts at sample 0), and the real
   * pre-press audio on a warm one. Stopping waits the same amount so the
   * tail isn't cut. 0 (default) = record the mic directly.
   */
  leadInMs?: number;
}

export interface HoldHandlers {
  onTouchStart: (e: React.TouchEvent<HTMLElement>) => void;
  onTouchMove: (e: React.TouchEvent<HTMLElement>) => void;
  onTouchEnd: (e: React.TouchEvent<HTMLElement>) => void;
  onTouchCancel: () => void;
  onPointerDown: (e: React.PointerEvent<HTMLElement>) => void;
  onPointerMove: (e: React.PointerEvent<HTMLElement>) => void;
  onPointerUp: (e: React.PointerEvent<HTMLElement>) => void;
  onPointerCancel: (e: React.PointerEvent<HTMLElement>) => void;
  onContextMenu: (e: React.SyntheticEvent) => void;
}

export interface Voice<T = void> {
  phase: VoicePhase;
  cleanupStatus: CleanupStatus;
  aboutToCancel: boolean;
  /** Imperative start — wire global hotkeys (push-to-talk) to this. */
  start: () => void;
  /** Imperative stop. cancel=true discards the audio. */
  stop: (cancel?: boolean) => void;
  /** Spread onto a button/element for hold-to-talk + slide-to-cancel. */
  holdHandlers: HoldHandlers;
}

export function useVoice<T = void>(options: UseVoiceOptions<T>): Voice<T> {
  const [phase, setPhase] = useState<VoicePhase>('idle');
  const [cleanupStatus, setCleanupStatus] = useState<CleanupStatus>('none');
  const [aboutToCancel, setAboutToCancel] = useState(false);

  // Options live in a ref so start/stop can be stable closures handed
  // to once-registered global hotkey handlers without going stale.
  const optsRef = useRef(options);
  optsRef.current = options;

  const initializingRef = useRef(false);
  const requestRef = useRef<AbortController | null>(null);
  const recorderRef = useRef<MediaRecorder | null>(null);
  // The open mic. Survives between recordings while warm (keepWarmMs).
  const captureRef = useRef<Capture | null>(null);
  const warmTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  // Pending delayed stop (lead-in tail); also guards double stops.
  const stopTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const chunksRef = useRef<Blob[]>([]);
  const startedAtRef = useRef<number>(0);
  const startDelayRef = useRef<{ ms: number; warm: boolean }>({ ms: 0, warm: false });
  const mountedRef = useRef(true);
  // Pinned at recording-start — see acquireTarget docs.
  const targetRef = useRef<T | null>(null);
  // Context captured at recording-start, parallel with mic init.
  const contextRef = useRef<string | undefined>(undefined);
  // Set when the user cancels mid-recording (slide-to-cancel, Escape,
  // system-level pointer/touch cancel). Read inside `onstop` to discard
  // audio instead of transcribing.
  const cancelledRef = useRef(false);
  const slideStartRef = useRef<{ x: number; y: number } | null>(null);
  const slideArmedRef = useRef(false);
  // Latched when stop() is called BEFORE start() has finished
  // initializing the MediaRecorder (e.g. push-to-talk key released
  // while getUserMedia was still resolving the permission prompt).
  // Consumed at the end of start() so we bail out cleanly instead of
  // entering recording state for a moment the user never asked for.
  const stopAsapRef = useRef<null | boolean>(null);
  // Stable ref to the latest phase so the stable start/stop closures
  // can read it without re-creating per render.
  const phaseRef = useRef<VoicePhase>('idle');
  useEffect(() => {
    phaseRef.current = phase;
  }, [phase]);

  function publishSession(p: VoicePhase, cancelArmed: boolean) {
    phaseRef.current = p;
    setAboutToCancel(cancelArmed);
    optsRef.current.onSession?.({
      phase: p,
      target: p === 'idle' ? null : targetRef.current,
      aboutToCancel: cancelArmed,
    });
  }

  function clearWarmTimer() {
    if (warmTimerRef.current !== null) {
      clearTimeout(warmTimerRef.current);
      warmTimerRef.current = null;
    }
  }

  // Close the mic for real: stops the device and the OS indicator.
  function releaseCapture() {
    clearWarmTimer();
    const c = captureRef.current;
    captureRef.current = null;
    c?.close();
  }

  // After a recording: keep the mic warm for the next press, or close it.
  function parkCapture() {
    const keep = optsRef.current.keepWarmMs ?? 0;
    if (keep <= 0 || !mountedRef.current || !captureLive(captureRef.current)) {
      releaseCapture();
      return;
    }
    clearWarmTimer();
    const expire = () => {
      warmTimerRef.current = null;
      // Busy (a new recording, or still transcribing)? Check again later
      // rather than pulling the mic out from under it.
      if (phaseRef.current === 'idle') releaseCapture();
      else warmTimerRef.current = setTimeout(expire, 1000);
    };
    warmTimerRef.current = setTimeout(expire, keep);
  }

  // Tear down any active mic stream on unmount so we don't leave the
  // OS-level recording indicator on after a route change / reload. A warm
  // mic is also released as soon as the page is hidden.
  useEffect(() => {
    const onHidden = () => {
      if (document.visibilityState === 'hidden' && phaseRef.current === 'idle') releaseCapture();
    };
    const onPageHide = () => {
      if (phaseRef.current === 'idle') releaseCapture();
    };
    mountedRef.current = true;
    document.addEventListener('visibilitychange', onHidden);
    window.addEventListener('pagehide', onPageHide);
    return () => {
      mountedRef.current = false;
      cancelledRef.current = true;
      stopAsapRef.current = true;
      requestRef.current?.abort();
      document.removeEventListener('visibilitychange', onHidden);
      window.removeEventListener('pagehide', onPageHide);
      if (stopTimerRef.current !== null) clearTimeout(stopTimerRef.current);
      if (recorderRef.current && recorderRef.current.state !== 'inactive') {
        try {
          recorderRef.current.stop();
        } catch {
          /* ignore */
        }
      }
      releaseCapture();
    };
  }, []);

  async function startRecording() {
    const opts = optsRef.current;
    const fail = (msg: string, kind: 'error' | 'warn' = 'error') => opts.onError?.(msg, kind);

    if (phaseRef.current !== 'idle' || initializingRef.current) return;
    if (typeof navigator === 'undefined' || !navigator.mediaDevices?.getUserMedia) {
      fail('Mic not available in this browser');
      return;
    }
    if (typeof MediaRecorder === 'undefined') {
      fail('Recording not supported in this browser');
      return;
    }

    // Pin the delivery target NOW — anything that gains focus later in
    // this session (user navigating mid-record, mid-transcribe) must
    // not redirect the text.
    let target: T;
    if (opts.acquireTarget) {
      const t = opts.acquireTarget();
      if (t === null) return; // app refused (and toasted) — stay idle
      target = t;
    } else {
      target = undefined as T;
    }
    initializingRef.current = true;
    stopAsapRef.current = null;
    cancelledRef.current = false;
    targetRef.current = target;

    // Capture the context snapshot up front, while the mic is
    // initializing — parallelizes with getUserMedia below. Capturing
    // inside onstop would serialize it ahead of the upload.
    try {
      contextRef.current = opts.getContext?.(target);
    } catch {
      contextRef.current = undefined;
    }

    const wantBlur =
      typeof opts.blurOnStart === 'function'
        ? opts.blurOnStart(target)
        : opts.blurOnStart !== false;
    if (wantBlur && document.activeElement instanceof HTMLElement) {
      document.activeElement.blur();
    }

    // Reuse a warm mic when one is still open; otherwise open the device.
    const requestedAt = Date.now();
    clearWarmTimer();
    let capture: Capture;
    const warm = captureLive(captureRef.current);
    if (warm) {
      capture = captureRef.current as Capture;
    } else {
      releaseCapture();
      try {
        capture = await openCapture(opts.leadInMs ?? 0);
      } catch (err) {
        const msg = (err as Error).message || String(err);
        if ((err as Error).name === 'NotAllowedError') {
          fail('Mic blocked. Allow in site settings, then try again.');
        } else {
          fail('Mic error: ' + msg);
        }
        initializingRef.current = false;
        stopAsapRef.current = null;
        targetRef.current = null;
        return;
      }
      captureRef.current = capture;
    }

    // If stop() was called while getUserMedia was in flight (push-to-
    // talk released before the mic came online), bail and stay idle
    // instead of flashing into 'recording' for a beat only to stop.
    // A press that never became a recording doesn't keep the mic warm.
    if (stopAsapRef.current !== null || !mountedRef.current) {
      initializingRef.current = false;
      stopAsapRef.current = null;
      releaseCapture();
      targetRef.current = null;
      return;
    }

    const { mimeType, ext } = pickMimeType();
    const mr = new MediaRecorder(capture.record, mimeType ? { mimeType } : undefined);
    chunksRef.current = [];
    cancelledRef.current = false;

    mr.ondataavailable = (e) => {
      if (e.data && e.data.size > 0) chunksRef.current.push(e.data);
    };
    mr.onstop = async () => {
      const o = opts;
      stopTimerRef.current = null;
      recorderRef.current = null;
      // Close the mic now, or keep it warm for keepWarmMs. Never leave it
      // open indefinitely: the OS recording indicator stays lit until the
      // tracks stop.
      parkCapture();

      // Captured at start of this recording session — do NOT
      // re-acquire here. That's the whole point of pinning.
      const lockedTarget = targetRef.current as T;

      const reset = () => {
        setPhase('idle');
        targetRef.current = null;
        contextRef.current = undefined;
        publishSession('idle', false);
      };

      if (cancelledRef.current) {
        reset();
        return;
      }

      const duration = Date.now() - startedAtRef.current;
      const minMs = o.minRecordMs ?? DEFAULT_MIN_RECORD_MS;
      if (duration < minMs || chunksRef.current.length === 0) {
        reset();
        return;
      }

      setPhase('transcribing');
      publishSession('transcribing', false);
      const blob = new Blob(chunksRef.current, { type: mimeType || 'audio/webm' });

      // AbortController + watchdog. If the fetch stalls, abort fires,
      // the promise rejects, and the finally clause resets phase —
      // without this a single stall locks the button disabled forever.
      const timeoutMs = o.timeoutMs ?? DEFAULT_TRANSCRIBE_TIMEOUT_MS;
      const ac = new AbortController();
      requestRef.current = ac;
      const timeoutId = setTimeout(() => ac.abort(new Error('timeout')), timeoutMs);
      try {
        const { text, cleanup } = await o.transcribe(blob, {
          filename: `audio.${ext}`,
          context: contextRef.current,
          language: o.language || undefined,
          signal: ac.signal,
        });
        if (ac.signal.aborted || cancelledRef.current || !mountedRef.current) return;
        const status: CleanupStatus =
          cleanup === 'ok' ||
          cleanup === 'disabled' ||
          cleanup === 'failed' ||
          cleanup === 'skipped'
            ? cleanup
            : 'none';
        setCleanupStatus(status);
        const cleanedText = (text || '').trim();
        if (!cleanedText) {
          o.onError?.('No speech detected', 'warn');
          return;
        }
        await o.onText(cleanedText, {
          cleanup: status,
          target: lockedTarget,
          startDelayMs: startDelayRef.current.ms,
          warm: startDelayRef.current.warm,
        });
      } catch (err) {
        const e = err as Error;
        const isAbort = e.name === 'AbortError' || String(e.message).includes('timeout');
        const msg = isAbort
          ? `Transcription timed out after ${Math.round(timeoutMs / 1000)}s — network stalled or server hung`
          : 'Transcription failed: ' + (e.message || String(e));
        o.onError?.(msg, 'error');
      } finally {
        requestRef.current = null;
        clearTimeout(timeoutId);
        reset();
      }
    };

    recorderRef.current = mr;
    startedAtRef.current = Date.now();
    // Second stop-asap check: stop() may have landed between the first
    // check and MediaRecorder construction.
    if (stopAsapRef.current !== null) {
      stopAsapRef.current = null;
      cancelledRef.current = false;
      recorderRef.current = null;
      releaseCapture();
      targetRef.current = null;
      return;
    }
    initializingRef.current = false;
    mr.start();
    startDelayRef.current = { ms: Date.now() - requestedAt, warm };
    setPhase('recording');
    publishSession('recording', false);
  }

  function stopRecording(cancel = false) {
    cancelledRef.current = cancelledRef.current || cancel;
    if (cancel) requestRef.current?.abort();
    const mr = recorderRef.current;
    if (mr && mr.state !== 'inactive') {
      if (stopTimerRef.current !== null) {
        // Already stopping (lead-in tail). A cancel cuts it short.
        if (!cancel) return;
        clearTimeout(stopTimerRef.current);
        stopTimerRef.current = null;
      }
      // The recorder hears the mic `leadInMs` late, so keep it running
      // that much longer to catch the last word. Cancels stop at once.
      const tail = cancel ? 0 : (captureRef.current?.leadInMs ?? 0);
      if (tail > 0) {
        stopTimerRef.current = setTimeout(() => {
          stopTimerRef.current = null;
          try {
            mr.stop();
          } catch {
            /* ignore */
          }
        }, tail);
        return;
      }
      try {
        mr.stop();
      } catch {
        /* ignore */
      }
      return;
    }
    // MediaRecorder doesn't exist yet — startRecording is mid-flight
    // (mic permission prompt, slow getUserMedia). Latch the intent so
    // startRecording tears down cleanly when it resolves.
    if (initializingRef.current) {
      stopAsapRef.current = stopAsapRef.current === true || cancel;
    }
  }

  // Slide-to-cancel: arms cancel-on-release once the pointer/touch has
  // moved past the threshold from its origin, and disarms when the user
  // slides back under it — lets them change their mind mid-gesture.
  function handleSlideMove(clientX: number, clientY: number) {
    const start = slideStartRef.current;
    if (!start) return;
    const px = optsRef.current.slideCancelPx ?? DEFAULT_SLIDE_CANCEL_PX;
    const dx = clientX - start.x;
    const dy = clientY - start.y;
    const armed = dx * dx + dy * dy >= px * px;
    if (armed === slideArmedRef.current) return;
    slideArmedRef.current = armed;
    if (phaseRef.current === 'recording') {
      publishSession('recording', armed);
    }
  }

  // Mobile: touch events with preventDefault so the browser doesn't
  // process the tap as a focus-inducing gesture that summons the
  // virtual keyboard. preventDefault on touchstart also suppresses the
  // synthetic pointerdown that would otherwise fire second, so we don't
  // double-trigger. Desktop mouse flows through the pointer handlers.
  const holdHandlers: HoldHandlers = {
    onTouchStart: (e) => {
      e.preventDefault();
      const t = e.touches[0];
      if (t) slideStartRef.current = { x: t.clientX, y: t.clientY };
      slideArmedRef.current = false;
      void startRecording();
    },
    onTouchMove: (e) => {
      const t = e.touches[0];
      if (t) handleSlideMove(t.clientX, t.clientY);
    },
    onTouchEnd: (e) => {
      e.preventDefault();
      const cancelled = slideArmedRef.current;
      slideStartRef.current = null;
      slideArmedRef.current = false;
      if (phaseRef.current === 'recording' || initializingRef.current) stopRecording(cancelled);
    },
    onTouchCancel: () => {
      slideStartRef.current = null;
      slideArmedRef.current = false;
      if (phaseRef.current === 'recording' || initializingRef.current) stopRecording(true);
    },
    // Desktop: setPointerCapture lets the user drag off the element and
    // still get pointermove/pointerup — without capture, moving
    // off-element sends those events elsewhere and slide-to-cancel +
    // the stop signal would both be lost.
    onPointerDown: (e) => {
      if (e.pointerType === 'touch') return; // handled by onTouchStart
      e.preventDefault();
      // Guarded like releasePointerCapture below: synthetic events and
      // some browser edge cases throw NotFoundError for a pointerId
      // with no active pointer, and losing capture only degrades
      // slide-to-cancel — it must not kill recording start.
      try {
        (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
      } catch {
        /* ignore */
      }
      slideStartRef.current = { x: e.clientX, y: e.clientY };
      slideArmedRef.current = false;
      void startRecording();
    },
    onPointerMove: (e) => {
      if (e.pointerType === 'touch') return;
      if (!slideStartRef.current) return;
      handleSlideMove(e.clientX, e.clientY);
    },
    onPointerUp: (e) => {
      if (e.pointerType === 'touch') return;
      e.preventDefault();
      try {
        (e.currentTarget as HTMLElement).releasePointerCapture(e.pointerId);
      } catch {
        /* ignore */
      }
      const cancelled = slideArmedRef.current;
      slideStartRef.current = null;
      slideArmedRef.current = false;
      if (phaseRef.current === 'recording' || initializingRef.current) stopRecording(cancelled);
    },
    onPointerCancel: (e) => {
      if (e.pointerType === 'touch') return;
      slideStartRef.current = null;
      slideArmedRef.current = false;
      if (phaseRef.current === 'recording' || initializingRef.current) stopRecording(true);
    },
    // Suppress the context menu on long-press (mobile) so holding to
    // record doesn't pop up "copy / share" menus.
    onContextMenu: (e) => e.preventDefault(),
  };

  return {
    phase,
    cleanupStatus,
    aboutToCancel,
    start: () => {
      void startRecording();
    },
    stop: (cancel = false) => {
      stopRecording(cancel);
    },
    holdHandlers,
  };
}
