import { useEffect, useRef, useSyncExternalStore } from 'react';
import type { CleanupStatus, HoldHandlers } from 'soluvoice';
import { useStore } from '../store';
import { voiceSession, type SessionDelivery } from './voiceSession';
import { playReadyTick } from './voiceReadyCue';

// A press shorter than this can't hold a word: a plain click on the mic. It is
// discarded before anything is uploaded (so it costs no daily voice use) and
// the user is told the button is hold-to-talk.
export const SHORT_PRESS_MS = 400;
export const SHORT_PRESS_HINT = 'Hold the mic button while you talk.';
const SHORT_PRESS_KEY_HINT = ' Or tap Alt+S to start and stop.';

export interface ContinuousVoiceOptions<T> {
  language?: string;
  keepWarmMs?: number;
  leadInMs?: number;
  timeoutMs?: number;
  paneId?: string | null;
  title?: string;
  acquireTarget: () => T | null;
  getContext?: (target: T) => string | undefined;
  valid?: (target: T) => boolean;
  captureValid?: (target: T) => boolean;
  blurOnStart?: boolean | ((target: T) => boolean);
  onText: (
    text: string,
    info: SessionDelivery & {
      target: T;
      startDelayMs: number;
      warm: boolean;
      cleanup: CleanupStatus;
    },
  ) => boolean | void | Promise<boolean | void>;
  onError?: (message: string, kind: 'error' | 'warn') => void;
}
/** React owns presentation; the module controller owns retained recovery data. */
export function useContinuousVoice<T>(options: ContinuousVoiceOptions<T>) {
  const latest = useRef(options);
  latest.current = options;
  const owned = useRef<string | null>(null);
  const mounted = useRef(true);
  // fresh: this press started the recording. Only such a press can be a stray
  // click; one that stops a recording begun by Alt+S or Space must commit it.
  const slide = useRef<{ x: number; y: number; cancel: boolean; at: number; touch: boolean; fresh: boolean } | null>(null);
  const snapshot = useSyncExternalStore(
    voiceSession.subscribe,
    voiceSession.getSnapshot,
    voiceSession.getSnapshot,
  );
  const start = useRef(() => {
    const opts = latest.current;
    if (voiceSession.getSnapshot()) return;
    const target = opts.acquireTarget();
    if (target === null) return;
    const state = useStore.getState();
    let context: string | undefined;
    try {
      context = opts.getContext?.(target);
    } catch {
      /* best effort */
    }
    const blur =
      typeof opts.blurOnStart === 'function'
        ? opts.blurOnStart(target)
        : opts.blurOnStart !== false;
    if (blur && document.activeElement instanceof HTMLElement) document.activeElement.blur();
    if (
      voiceSession.start({
        paneId:
          opts.paneId ??
          (typeof target === 'object' && target && 'paneId' in target
            ? String(target.paneId)
            : null),
        title: opts.title ?? 'Voice dictation',
        language: opts.language ?? state.voiceLanguage,
        context,
        autoSend: state.voiceAutoSend,
        deferCleanup: state.voiceDeferCleanup && !state.voiceAutoSend,
        keepWarmMs: opts.keepWarmMs ?? state.voiceKeepWarmMs,
        valid: () => mounted.current && (opts.valid?.(target) ?? true),
        captureValid: () => mounted.current && (opts.captureValid?.(target) ?? opts.valid?.(target) ?? true),
        deliver: (text, info) =>
          opts.onText(text, {
            ...info,
            target,
            startDelayMs: info.startDelayMs,
            warm: info.warm,
            cleanup: info.cleanup as CleanupStatus,
          }),
        onReady: () => {
          if (useStore.getState().voiceReadyTick) playReadyTick();
        },
        onError: (message) => opts.onError?.(message, 'error'),
      })
    )
      owned.current = voiceSession.getSnapshot()!.sessionId;
  }).current;
  const stop = useRef((cancel = false) => {
    if (owned.current === voiceSession.getSnapshot()?.sessionId) voiceSession.stop(cancel);
  }).current;
  const preserve = useRef(() => {
    if (owned.current === voiceSession.getSnapshot()?.sessionId) voiceSession.stop(false, 'blur');
  }).current;
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      if (owned.current) voiceSession.detach(owned.current);
    };
  }, []);
  const begin = (x: number, y: number, touch: boolean) => {
    const fresh = !voiceSession.getSnapshot();
    slide.current = { x, y, cancel: false, at: Date.now(), touch, fresh };
    start();
  };
  const move = (x: number, y: number) => {
    if (slide.current)
      slide.current.cancel = Math.hypot(x - slide.current.x, y - slide.current.y) >= 80;
  };
  const end = (cancel?: boolean) => {
    if (slide.current) {
      const { at, touch, fresh } = slide.current;
      const discard = cancel ?? slide.current.cancel;
      slide.current = null;
      if (fresh && !discard && Date.now() - at < SHORT_PRESS_MS) {
        stop(true);
        latest.current.onError?.(SHORT_PRESS_HINT + (touch ? '' : SHORT_PRESS_KEY_HINT), 'warn');
        return;
      }
      stop(discard);
    }
  };
  const holdHandlers: HoldHandlers = {
    onTouchStart: (e) => {
      e.preventDefault();
      const t = e.touches[0];
      if (t) begin(t.clientX, t.clientY, true);
    },
    onTouchMove: (e) => {
      const t = e.touches[0];
      if (t) move(t.clientX, t.clientY);
    },
    onTouchEnd: (e) => {
      e.preventDefault();
      end();
    },
    onTouchCancel: () => end(true),
    onPointerDown: (e) => {
      if (e.pointerType === 'touch') return;
      e.preventDefault();
      try {
        e.currentTarget.setPointerCapture(e.pointerId);
      } catch {
        /* synthetic */
      }
      begin(e.clientX, e.clientY, false);
    },
    onPointerMove: (e) => {
      if (e.pointerType !== 'touch') move(e.clientX, e.clientY);
    },
    onPointerUp: (e) => {
      if (e.pointerType !== 'touch') {
        e.preventDefault();
        end();
      }
    },
    onPointerCancel: () => end(true),
    onContextMenu: (e) => e.preventDefault(),
  };
  const progress = snapshot?.sessionId === owned.current ? snapshot : null;
  return {
    phase: progress?.phase ?? 'idle',
    cleanupStatus: 'none' as CleanupStatus,
    aboutToCancel: slide.current?.cancel ?? false,
    start,
    stop,
    preserve,
    holdHandlers,
    progress,
  };
}
