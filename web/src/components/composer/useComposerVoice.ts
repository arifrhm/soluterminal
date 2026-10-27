// Composer voice engine — RELOCATES soluvoice's recording onto the chat
// textarea itself, so the composer needs NO mic button. The text box is the
// control:
//   • tap          → focus + type (native keyboard, instant)
//   • press & hold → record; release sends (push-to-talk). WHILE HOLDING:
//        slide UP    → lock hands-free (release keeps recording; tap ⏹ to send)
//        slide LEFT  → cancel (release discards), the same direction as Shinoque
//
// The tap-vs-hold + slide-to-lock/cancel state machine is lifted verbatim from
// the ARIA comms composer (aria/desktop/src/components/composer/useComposerVoice.ts)
// — that gesture model is the part worth reusing. The plumbing is rewired to
// soluterminal's existing voice backend: soluvoice's useVoice + api.transcribe
// (cloud Whisper-turbo + Haiku cleanup) and the voiceLanguage / voiceAutoSend
// store flags. soluterminal has no audio "voice notes", so this is DICTATION-
// ONLY: a release transcribes and inserts into the draft; with auto-send (🚀)
// on it then submits the draft via onSubmitText (review-then-send when off).
//
// soluvoice's own `holdHandlers` can't go on a textarea: they preventDefault the
// focus and START recording on the very first pointerdown, so every tap-to-type
// would blip the mic and never open the keyboard. Instead we drive soluvoice
// IMPERATIVELY (start/stop/phase) behind a small tap-vs-hold state machine, and
// only suppress native focus long enough to tell a tap from a hold (focus is
// summoned manually on a confirmed tap — still inside the user gesture, so iOS
// opens the keyboard). There is deliberately NO double-tap.

import { useCallback, useEffect, useRef, useState } from 'react';
import { insertIntoTextarea } from 'soluvoice';
import { useContinuousVoice } from '../../lib/useContinuousVoice';
import type { SessionProgress } from '../../lib/voiceSession';
import type { ComposerVoiceConfig, ComposerVoiceState, SlideArm, TextareaVoiceHandlers } from '../../extensions';

import { api } from '../../api';
import { useStore } from '../../store';
import type { VoiceControl } from '../../private/voice.slice';

import { trimVoiceContext } from '../../lib/voiceRequest';

import { voiceErrorForHost } from '../../lib/voiceMicMessage';

// Press longer than this → it's a hold (record), not a tap (focus).
const HOLD_MS = 280;
// Drag this far UP during a hold → arm lock (hands-free on release).
const SLIDE_LOCK_PX = 55;
// Drag this far LEFT during a hold → arm cancel (discard on release). Left, not
// right: it matches Shinoque, and a rightward drag that starts near the screen's
// left edge is the mobile drawer's open gesture.
const SLIDE_CANCEL_PX = 75;

// The composer reads this engine through the composerVoice slot
// (extensions.ts), which owns the shared types.
export type { ComposerVoiceConfig, SlideArm, TextareaVoiceHandlers };

export interface ComposerVoice extends ComposerVoiceState {
  progress: SessionProgress | null;
}

interface PressOrigin {
  pointerId?: number;
  x: number;
  y: number;
}

export function useComposerVoice(config: ComposerVoiceConfig): ComposerVoice {
  const { targetRef, enabled, shortcutActive = false, shortcutPaneId } = config;
  const voiceLanguage = useStore(s => s.voiceLanguage);
  const keepWarmMs = useStore(s => s.voiceKeepWarmMs);
  const activeVoicePaneId = useStore(s => s.voiceSession?.paneId ?? null);
  const [handsFree, setHandsFree] = useState(false);
  const [slide, setSlide] = useState<SlideArm>('none');
  const enabledRef = useRef(enabled); enabledRef.current = enabled;
  const currentConfig = useRef(config); currentConfig.current = config;
  const deliveryGuard = useRef<() => boolean>(() => true);
  const voice = useContinuousVoice<HTMLTextAreaElement>({
    language: voiceLanguage, keepWarmMs, paneId: shortcutPaneId,
    title: shortcutPaneId ? useStore.getState().panes[shortcutPaneId]?.title ?? 'message box' : 'message box',
    acquireTarget: () => {
      deliveryGuard.current = currentConfig.current.captureTargetGuard?.() ?? (() => true);
      if (!enabledRef.current || !(currentConfig.current.isTargetCurrent?.() ?? true) || !deliveryGuard.current()) return null;
      api.voicePrewarm(!!config.getContext);
      return targetRef.current;
    },
    valid: target => target.isConnected && target === targetRef.current && deliveryGuard.current() && (currentConfig.current.isTargetCurrent?.() ?? true),
    getContext: () => trimVoiceContext(config.getContext?.()),
    onText: (text, { target, autoSend, sessionId }) => {
      // The controller has already checkpointed raw/cleaned history, even
      // if this destination was disconnected during upstream processing.
      if (!target.isConnected || target !== targetRef.current || !deliveryGuard.current()) return false;
      insertIntoTextarea(target, text);
      if (autoSend && config.onSubmitText) {
        const submit = config.onSubmitText;
        requestAnimationFrame(() => requestAnimationFrame(() => {
          // Cancel or a newer session invalidates a deferred submission.
          const current = useStore.getState();
          const entry = current.voiceHistory.find(e => e.sessionId === sessionId);
          if (entry?.delivered && !entry.partial && current.voiceAutoSend && target.isConnected && target === targetRef.current && deliveryGuard.current() && (currentConfig.current.isTargetCurrent?.() ?? true)) submit();
        }));
      }
      return true;
    },
    onError: (message, kind) => currentConfig.current.onError?.(voiceErrorForHost(message), kind),
  });
  // soluvoice may return a fresh object each render; pin it so the gesture
  // handlers can stay stable (they read voiceRef.current.start/stop).
  const voiceRef = useRef(voice);
  voiceRef.current = voice;

  const { phase } = voice;
  const phaseRef = useRef(phase);
  phaseRef.current = phase;
  const recording = phase === 'recording';
  const transcribing = phase === 'draining' || phase === 'recovery' || phase === 'starting';
  const elapsedMs = voice.progress?.capturedMs ?? 0;

  // Focus's recorder temporarily sits above the always-mounted terminal
  // VoiceButton in the existing controller stack. Keep one stable proxy so an
  // App keydown can pin it through a focus change; the proxy always reaches the
  // freshest soluvoice object through voiceRef.
  const shortcutControlRef = useRef<VoiceControl | null>(null);
  if (shortcutControlRef.current === null) {
    shortcutControlRef.current = {
      start: () => voiceRef.current.start(),
      stop: (cancelRecording) => voiceRef.current.stop(cancelRecording),
      preserve: () => voiceRef.current.preserve(),
    };
  }
  useEffect(() => {
    // Keep ownership while an active recording/transcription finishes even if
    // pane focus moves. Do not let the newly-focused idle composer cover an
    // older pane's mouse-started recorder in the stack; Esc must still cancel
    // the pane named by voiceSession. App additionally pins keyboard starts.
    if (!shortcutPaneId) return;
    const ownsActiveSession = phase !== 'idle' || activeVoicePaneId === shortcutPaneId;
    const mayOwnForNextStart = shortcutActive
      && (activeVoicePaneId === null || activeVoicePaneId === shortcutPaneId);
    if (!ownsActiveSession && !mayOwnForNextStart) return;
    const ctrl = shortcutControlRef.current!;
    const store = useStore.getState();
    store.registerVoiceControl(ctrl);
    return () => store.unregisterVoiceControl(ctrl);
  }, [shortcutActive, shortcutPaneId, phase, activeVoicePaneId]);

  // Mutable gesture state (kept in refs — handlers run synchronously).
  const pressRef = useRef<PressOrigin | null>(null);
  const holdTimerRef = useRef<number | null>(null);
  const holdActiveRef = useRef(false);
  const slideRef = useRef<SlideArm>('none');
  const handsFreeRef = useRef(false);

  const clearHoldTimer = useCallback(() => {
    if (holdTimerRef.current !== null) {
      window.clearTimeout(holdTimerRef.current);
      holdTimerRef.current = null;
    }
  }, []);

  const setSlideArm = useCallback((v: SlideArm) => {
    if (slideRef.current !== v) {
      slideRef.current = v;
      setSlide(v);
    }
  }, []);

  // Held past the threshold → engage push-to-talk recording.
  const engageHold = useCallback(() => {
    holdTimerRef.current = null;
    holdActiveRef.current = true;
    targetRef.current?.blur();
    setSlideArm('none');
    voiceRef.current.start();
  }, [setSlideArm, targetRef]);

  const stopAndSend = useCallback(() => {
    handsFreeRef.current = false;
    setHandsFree(false);
    voiceRef.current.stop(false);
  }, []);

  const cancel = useCallback(() => {
    handsFreeRef.current = false;
    setHandsFree(false);
    setSlideArm('none');
    voiceRef.current.stop(true);
  }, [setSlideArm]);

  // During a hold, classify the drag: dominant UP → lock, far LEFT → cancel.
  const slideUpdate = useCallback(
    (x: number, y: number) => {
      const origin = pressRef.current;
      if (origin === null) return;
      const up = origin.y - y;
      const left = origin.x - x;
      if (up >= SLIDE_LOCK_PX && up >= left) setSlideArm('lock');
      else if (left >= SLIDE_CANCEL_PX) setSlideArm('cancel');
      else setSlideArm('none');
    },
    [setSlideArm],
  );

  // ---- pointer-down equivalent (shared by touch + mouse) ----
  const onDown = useCallback(
    (x: number, y: number, pointerId?: number) => {
      if (phaseRef.current === 'recording') return;
      pressRef.current = { x, y, pointerId };
      holdActiveRef.current = false;
      clearHoldTimer();
      holdTimerRef.current = window.setTimeout(engageHold, HOLD_MS);
    },
    [clearHoldTimer, engageHold],
  );

  // ---- pointer-up equivalent → finish a hold (send / lock / cancel) or tap ----
  const onUp = useCallback(() => {
    clearHoldTimer();
    if (holdActiveRef.current) {
      holdActiveRef.current = false;
      pressRef.current = null;
      const arm = slideRef.current;
      setSlideArm('none');
      if (arm === 'lock') {
        // Keep recording hands-free; the overlay's ⏹ / ✕ take over.
        handsFreeRef.current = true;
        setHandsFree(true);
      } else {
        // stop also releases a start still waiting for microphone permission.
        voiceRef.current.stop(arm === 'cancel');
      }
      return;
    }
    if (pressRef.current === null) return;
    pressRef.current = null;
    // Released before the hold threshold → a tap: focus to type (instant).
    if (phaseRef.current === 'recording') return;
    targetRef.current?.focus();
  }, [clearHoldTimer, setSlideArm, targetRef]);

  const onCancelGesture = useCallback(() => {
    clearHoldTimer();
    pressRef.current = null;
    holdActiveRef.current = false;
    handsFreeRef.current = false;
    setHandsFree(false);
    setSlideArm('none');
    // stop() also latches a cancellation while getUserMedia is still
    // resolving, so a permission prompt cannot resurrect a lost hold.
    voiceRef.current.stop(true);
  }, [clearHoldTimer, setSlideArm]);

  // Leaving the window (Alt+Tab, the page hidden, the Shinoque host hiding the
  // pane) mid-recording sends what was said for transcription, the same rule
  // App's window-blur handler applies to Alt+S recordings. Both handlers may
  // run on one blur; both commit, so neither can discard the other's send.
  // A press still waiting for the hold threshold (or for mic permission) has
  // recorded nothing yet, so that is simply cancelled.
  const onLeaveWindow = useCallback(() => {
    const wasRecording = phaseRef.current === 'recording';
    clearHoldTimer();
    pressRef.current = null;
    holdActiveRef.current = false;
    handsFreeRef.current = false;
    setHandsFree(false);
    setSlideArm('none');
    if (wasRecording) voiceRef.current.preserve();
    else if (phaseRef.current === 'starting' || phaseRef.current === 'idle') voiceRef.current.stop(true);
  }, [clearHoldTimer, setSlideArm]);

  // A pane can be reparented, hidden by the Shinoque host, or lose the window
  // before the textarea receives its matching pointer-up. In that case the
  // browser may never deliver the element's pointercancel/touchcancel either:
  // the hold timer would still engage recording and leave the textarea hidden,
  // making subsequent typing look dead until a reload. Handle the document
  // boundary as well as unmount so a lost gesture cannot strand the composer.
  useEffect(() => {
    const cancelPointer = (event: PointerEvent) => {
      // Document capture sees cancellations from every control and pointer.
      // Only the pointer that owns this unfinished hold may cancel it.
      const press = pressRef.current;
      if (press?.pointerId !== undefined && press.pointerId === event.pointerId) {
        onCancelGesture();
      }
    };
    const leaveWhenHidden = () => {
      if (document.visibilityState === 'hidden') onLeaveWindow();
    };
    const leaveWhenEmbedHidden = (event: Event) => {
      const visible = (event as CustomEvent<{ visible?: unknown }>).detail?.visible;
      if (visible === false) onLeaveWindow();
    };
    window.addEventListener('blur', onLeaveWindow);
    document.addEventListener('visibilitychange', leaveWhenHidden);
    document.addEventListener('pointercancel', cancelPointer, true);
    window.addEventListener('solu:embed-visibility', leaveWhenEmbedHidden);
    return () => {
      window.removeEventListener('blur', onLeaveWindow);
      document.removeEventListener('visibilitychange', leaveWhenHidden);
      document.removeEventListener('pointercancel', cancelPointer, true);
      window.removeEventListener('solu:embed-visibility', leaveWhenEmbedHidden);
      // The library stops an existing stream on unmount, but a pending
      // getUserMedia request also needs its stop latch set.
      clearHoldTimer();
    };
  }, [clearHoldTimer, onCancelGesture, onLeaveWindow]);

  const handlers: TextareaVoiceHandlers = {
    onTouchStart: (e) => {
      if (!enabledRef.current || (phaseRef.current !== 'idle' && phaseRef.current !== 'recording')) return;
      // Suppress native focus/keyboard so we can tell tap from hold; a confirmed
      // tap re-focuses manually in onUp (still within this user gesture).
      e.preventDefault();
      const t = e.touches[0];
      if (t) onDown(t.clientX, t.clientY);
    },
    onTouchMove: (e) => {
      if (!holdActiveRef.current) return;
      const t = e.touches[0];
      if (t) slideUpdate(t.clientX, t.clientY);
    },
    onTouchEnd: (e) => {
      if (!enabledRef.current) return;
      e.preventDefault();
      onUp();
    },
    onTouchCancel: () => onCancelGesture(),
    onPointerDown: (e) => {
      if (e.pointerType === 'touch') return; // touch handled above
      if (!enabledRef.current || (phaseRef.current !== 'idle' && phaseRef.current !== 'recording')) return;
      e.preventDefault();
      try {
        e.currentTarget.setPointerCapture(e.pointerId);
      } catch {
        /* losing capture only degrades slide-to-lock/cancel */
      }
      onDown(e.clientX, e.clientY, e.pointerId);
    },
    onPointerMove: (e) => {
      if (e.pointerType === 'touch' || !holdActiveRef.current) return;
      slideUpdate(e.clientX, e.clientY);
    },
    onPointerUp: (e) => {
      if (e.pointerType === 'touch' || !enabledRef.current) return;
      e.preventDefault();
      try {
        e.currentTarget.releasePointerCapture(e.pointerId);
      } catch {
        /* ignore */
      }
      onUp();
    },
    onPointerCancel: (e) => {
      if (e.pointerType === 'touch') return;
      onCancelGesture();
    },
    // Suppress the long-press "copy / paste" menu while holding to record.
    onContextMenu: (e) => {
      if (holdActiveRef.current || holdTimerRef.current !== null) e.preventDefault();
    },
  };

  return {
    phase,
    recording,
    transcribing,
    handsFree,
    slide,
    elapsedMs,
    handlers,
    stopAndSend,
    cancel,
    polishing: false,
    submitBlocked: phase !== 'idle',
    onComposerInput: () => {},
    progress: voice.progress,
  };
}
