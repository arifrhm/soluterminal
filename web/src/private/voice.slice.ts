// Voice dictation state: settings, the live session, the recorder handle,
// history and the daily allowance (see voice.register.tsx). Merged into the
// app store by store.ts.
import type { CaptureStopReason } from 'soluvoice';
import type { VoiceSegmentResult } from '../lib/voiceHistory';
import type { StateCreator } from 'zustand';
import type { State } from '../store';
import type { VoiceCleanedBy } from './voice.api';

/** A single past voice-dictation outcome, persisted to localStorage so the
 *  user can recover text that went to the wrong pane, got misheard, or was
 *  lost when a pane closed mid-transcribe. Ring-buffered (newest first) at
 *  VOICE_HISTORY_MAX. Privacy-scoped: never leaves the machine. */
export interface VoiceTranscription {
  /** Creation timestamp in ms, doubles as the unique id. */
  id: number;
  sessionId?: string;
  partial?: boolean;
  stopReason?: CaptureStopReason;
  durationMs?: number;
  totalSegments?: number;
  completedSegments?: number;
  requestedLanguage?: string;
  detectedLanguage?: string;
  segments?: VoiceSegmentResult[];
  /** The cleaned transcription text as it was (or would have been)
   *  inserted into the pane. */
  text: string;
  /** Friendly label for the delivery target, e.g. pane title or first
   *  cmd token. Empty string when target pane was already gone. */
  paneTitle: string;
  /** Whether the text was actually delivered to a live pane. false when
   *  the target was killed mid-transcribe — still captured here so the
   *  user has a recovery path. */
  delivered: boolean;
  /** Who cleaned it up: the user's own Claude plan or SoluTerminal's cloud.
   *  Absent when it wasn't cleaned (raw text) or on older entries. */
  cleanedBy?: VoiceCleanedBy;
  /** What happened to the cleanup pass, so a bad transcript can always be
   *  explained. Absent on older entries. */
  polish?: VoicePolish;
  /** What Whisper heard, when it differs from `text`. */
  raw?: string;
  /** ms from pressing the mic to the recorder capturing. Words spoken in
   *  this gap are lost, so a large value explains a missing first word. */
  startDelayMs?: number;
  /** The mic was still open from the previous dictation (no start gap). */
  warmMic?: boolean;
}

/** Outcome of the cleanup pass for one dictation:
 *  - cleaned: cleanup changed the text and it was used
 *  - unchanged: cleanup ran and found nothing to fix
 *  - short: too short to clean (sent as heard)
 *  - kept-raw: cleanup ran but you had typed or pressed Enter, so the
 *    heard text stayed
 *  - failed: cleanup errored or timed out (sent as heard)
 *  - off: no cleanup available (e.g. not connected) */
export type VoicePolish = 'cleaned' | 'unchanged' | 'short' | 'kept-raw' | 'failed' | 'off';

export interface VoiceSession {
  /** Pane id locked at recording-start. Transcription always lands here
   *  regardless of where focus goes during/after recording. Null when
   *  the recording target is the ARIA chat textarea instead of a pane —
   *  the session is still tracked so global stop/cancel paths work, but
   *  no pane chip is rendered. */
  paneId: string | null;
  /** 'recording' = mic capturing; 'transcribing' = waiting for the
   *  transcription server. Chip copy differs per phase. */
  phase: 'starting' | 'recording' | 'draining' | 'recovery' | 'transcribing';
  sessionId?: string;
  capturedMs?: number;
  completedSegments?: number;
  totalSegments?: number;
  retainedBytes?: number;
  retainedDurationMs?: number;
  partial?: boolean;
  stopReason?: CaptureStopReason;
  /** Set true mid-recording once the user has slid their
   *  finger/pointer past the cancel threshold (or any other path has
   *  armed cancel-on-release). The pane chip flips to a red "Release
   *  to cancel" state so the user gets a clear visual commitment cue
   *  before they lift. Ignored in 'transcribing' phase — by then the
   *  audio is already en route. */
  aboutToCancel?: boolean;
}

export interface VoiceControl {
  start: () => void;
  stop: (cancel: boolean) => void;
  preserve?: () => void;
}

export interface VoiceSlice {
  // Voice-to-text: when true, transcribed text is auto-submitted to the
  // focused pane (text + \r). When false, text is inserted for review
  // and the user hits Enter themselves. Persisted to localStorage.
  voiceAutoSend: boolean;
  setVoiceAutoSend: (v: boolean) => void;

  // Voice-to-text (composer, review mode only): when true, dictation shows
  // the RAW Whisper text immediately and polishes it in place a beat later
  // (a second cleanup call), instead of blocking on the full serial pipeline.
  // Send is briefly gated (capped) while polishing so you never fire raw by
  // accident. Experimental — default OFF. Ignored under auto-send (🚀).
  voiceDeferCleanup: boolean;
  setVoiceDeferCleanup: (v: boolean) => void;

  // Voice language hint forwarded to Whisper. "" = auto-detect (default);
  // otherwise an ISO-639-1 code ("en", "zh", "ms", "ja", ...). Exposed
  // because Whisper's auto-detect is unreliable on short clips —
  // English-only users kept getting the odd clip mis-detected as Malay.
  voiceLanguage: string;
  setVoiceLanguage: (v: string) => void;

  // How long the mic stays open after dictating (0 = release at once), so
  // the next dictation starts instantly and keeps its first word. Off by
  // default: the mic opens on press and closes when the clip ends. When on,
  // the OS mic indicator stays lit for the window and each later clip starts
  // with VOICE_LEAD_IN_MS of audio from just before the press.
  voiceKeepWarmMs: number;
  /** Turns the 2-minute ready window on or off. */
  setVoiceKeepWarm: (on: boolean) => void;

  // A soft tick the moment the mic is really recording, so a cold start
  // never swallows the first word unnoticed. On by default.
  voiceReadyTick: boolean;
  setVoiceReadyTick: (v: boolean) => void;

  // Rolling history of the last N successful voice transcriptions, so the
  // user can recover text that went to the wrong pane, got misheard, or
  // was lost when a pane closed mid-transcribe. Ring-buffered (newest
  // first), capped at VOICE_HISTORY_MAX, persisted to localStorage.
  // Never transmitted anywhere — stays on this device only.
  voiceHistory: VoiceTranscription[];
  /** Prepend a new transcription; drops the oldest when over cap. */
  addVoiceHistoryEntry: (entry: Omit<VoiceTranscription, 'id'>) => void;
  /** Wipe the persisted history. */
  clearVoiceHistory: () => void;
  removeVoiceCheckpoint: (sessionId: string) => void;
  /** Re-send a previously-transcribed entry to the currently-focused
   *  pane. Returns true if the focused pane accepted the bytes. */
  reinsertVoiceHistory: (id: number) => boolean;

  // Voice-to-text live session — null when idle. Captured at the moment
  // recording STARTS and held through the full recording + transcription
  // cycle so the target pane can't drift if the user moves focus
  // mid-session. The transcribed text always lands in `paneId`, even if
  // the user navigated away while we were waiting for Groq/Claude to
  // return. `phase` drives the UI chip text ('🎤 Listening' vs.
  // '⏳ Transcribing → will insert here').
  voiceSession: VoiceSession | null;
  setVoiceSession: (s: VoiceSession | null) => void;

  // Imperative handle registered by VoiceButton. Lets the global Alt+S
  // keybinding start/stop recording without lifting all the MediaRecorder
  // plumbing out of the component. Backed by a stack so a root fallback
  // controller can remain available when visible mic buttons unmount.
  voiceControl: VoiceControl | null;
  registerVoiceControl: (ctrl: VoiceControl | null) => void;
  unregisterVoiceControl: (ctrl: VoiceControl) => void;

  // The daily voice allowance the cloud last reported (free accounts get 20
  // a day). null until the first dictation of this session. Shown on the mic
  // so the limit is never a surprise.
  voiceQuota: { remaining: number; limit: number | null; at: number } | null;
  setVoiceQuota: (q: { remaining: number; limit: number | null } | null) => void;
}

const VOICE_AUTOSEND_KEY = 'solu.voiceAutoSend';
const voiceAutoSendDefault = (() => {
  try { return localStorage.getItem(VOICE_AUTOSEND_KEY) === '1'; } catch { return false; }
})();

// Raw-then-patch is the DEFAULT voice-dictation behavior — no user-facing toggle
// (it's simply how dictation works now). This key is a hidden escape hatch only:
// set solu.voiceDeferCleanup='0' in localStorage to fall back to the old
// wait-for-cleanup one-shot. Absent / anything-but-'0' → on.
const VOICE_DEFER_CLEANUP_KEY = 'solu.voiceDeferCleanup';
const voiceDeferCleanupDefault = (() => {
  try { return localStorage.getItem(VOICE_DEFER_CLEANUP_KEY) !== '0'; } catch { return true; }
})();

// Rolling voice-dictation history — last N successful transcriptions so
// the user can recover text lost to a closed pane / wrong-pane delivery /
// misheard phrase. Kept purely in localStorage (never transmitted).
const VOICE_HISTORY_KEY = 'solu.voiceHistory';
const VOICE_HISTORY_MAX = 20;
export function boundVoiceHistory(entries: VoiceTranscription[]): VoiceTranscription[] {
  const result = entries.slice(0, VOICE_HISTORY_MAX).map(e => ({ ...e, segments: e.segments?.slice(0, 36) }));
  while (result.length > 1 && new TextEncoder().encode(JSON.stringify(result)).length > 1024 * 1024) result.pop();
  return result;
}
const voiceHistoryDefault: VoiceTranscription[] = (() => {
  try {
    const raw = localStorage.getItem(VOICE_HISTORY_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return [];
    // Defensive shape check so a corrupted entry doesn't crash the
    // whole store bootstrap.
    return parsed.filter((e): e is VoiceTranscription =>
      !!e &&
      typeof (e as VoiceTranscription).id === 'number' &&
      typeof (e as VoiceTranscription).text === 'string' &&
      typeof (e as VoiceTranscription).paneTitle === 'string' &&
      typeof (e as VoiceTranscription).delivered === 'boolean',
    ).slice(0, VOICE_HISTORY_MAX);
  } catch { return []; }
})();

const VOICE_LANGUAGE_KEY = 'solu.voiceLanguage';
// Default to English ('en') for fresh installs; explicit "Auto" (empty
// string) is still honored because localStorage.getItem returns '' (not
// null) when the user selected it, so `??` doesn't fall through. Users
// who prefer another language persist their choice the same way.
const voiceLanguageDefault = (() => {
  try { return localStorage.getItem(VOICE_LANGUAGE_KEY) ?? 'en'; } catch { return 'en'; }
})();

/** The ready window when the user turns "keep the microphone ready" on. */
export const VOICE_KEEP_WARM_MS = 120_000;
/** Saved windows still honoured. 30 s was an earlier choice; keeping it
 *  means nobody's mic stays open longer than they picked. */
const VOICE_KEEP_WARM_SAVED = [0, 30_000, VOICE_KEEP_WARM_MS];

const VOICE_KEEP_WARM_MS_KEY = 'solu.voiceKeepWarmMs';
const VOICE_KEEP_MIC_WARM_LEGACY_KEY = 'solu.voiceKeepMicWarm';
/** Reads the ready window. Off unless the user chose it: an open mic keeps
 *  the OS mic light on and adds pre-press audio to each clip, so it is
 *  opt-in. Earlier builds kept it on without asking, and wrote nothing until
 *  the user changed it, so a missing value means off. The older on/off
 *  setting was only written by the user, so its "on" still counts. */
export function readVoiceKeepWarmMs(): number {
  try {
    const raw = localStorage.getItem(VOICE_KEEP_WARM_MS_KEY);
    const n = raw == null ? NaN : Number(raw);
    if (VOICE_KEEP_WARM_SAVED.includes(n)) return n;
    if (localStorage.getItem(VOICE_KEEP_MIC_WARM_LEGACY_KEY) === '1') return VOICE_KEEP_WARM_MS;
  } catch {}
  return 0;
}

const VOICE_READY_TICK_KEY = 'solu.voiceReadyTick';
const voiceReadyTickDefault = (() => {
  try { return localStorage.getItem(VOICE_READY_TICK_KEY) !== '0'; } catch { return true; }
})();
/** Each clip starts with this much lead-in so the first word isn't at
 *  sample 0. On a freshly opened mic (the default) it is silence, recorded
 *  after the press; only a warm mic has real audio from before the press. */
export const VOICE_LEAD_IN_MS = 300;

const voiceControlStack: VoiceControl[] = [];

declare module '../store' {
  interface FeatureSlices extends VoiceSlice {}
}

const createVoiceSlice: StateCreator<State, [], [], VoiceSlice> = (set, get) => ({
  voiceAutoSend: voiceAutoSendDefault,
  setVoiceAutoSend: (v) => {
    try { localStorage.setItem(VOICE_AUTOSEND_KEY, v ? '1' : '0'); } catch {}
    set({ voiceAutoSend: v });
  },
  voiceDeferCleanup: voiceDeferCleanupDefault,
  setVoiceDeferCleanup: (v) => {
    try { localStorage.setItem(VOICE_DEFER_CLEANUP_KEY, v ? '1' : '0'); } catch {}
    set({ voiceDeferCleanup: v });
  },
  voiceLanguage: voiceLanguageDefault,
  setVoiceLanguage: (v) => {
    try { localStorage.setItem(VOICE_LANGUAGE_KEY, v); } catch {}
    set({ voiceLanguage: v });
  },
  voiceKeepWarmMs: readVoiceKeepWarmMs(),
  setVoiceKeepWarm: (on) => {
    const v = on ? VOICE_KEEP_WARM_MS : 0;
    try { localStorage.setItem(VOICE_KEEP_WARM_MS_KEY, String(v)); } catch {}
    set({ voiceKeepWarmMs: v });
  },
  voiceReadyTick: voiceReadyTickDefault,
  setVoiceReadyTick: (v) => {
    try { localStorage.setItem(VOICE_READY_TICK_KEY, v ? '1' : '0'); } catch {}
    set({ voiceReadyTick: v });
  },

  voiceSession: null,
  setVoiceSession: (s) => set({ voiceSession: s }),
  voiceQuota: null,
  setVoiceQuota: (q) => set({ voiceQuota: q ? { ...q, at: Date.now() } : null }),
  voiceControl: null,
  registerVoiceControl: (ctrl) => {
    if (!ctrl) {
      voiceControlStack.length = 0;
      set({ voiceControl: null });
      return;
    }
    const existing = voiceControlStack.indexOf(ctrl);
    if (existing >= 0) voiceControlStack.splice(existing, 1);
    voiceControlStack.push(ctrl);
    set({ voiceControl: ctrl });
  },
  unregisterVoiceControl: (ctrl) => {
    const idx = voiceControlStack.indexOf(ctrl);
    if (idx >= 0) voiceControlStack.splice(idx, 1);
    set({ voiceControl: voiceControlStack[voiceControlStack.length - 1] ?? null });
  },


  voiceHistory: voiceHistoryDefault,
  addVoiceHistoryEntry: (entry) => {
    const now = Date.now();
    set((s) => {
      // Newest first, cap at VOICE_HISTORY_MAX. No dedup against prior
      // entries — repeating the same phrase is legitimate (retry after
      // misfire, etc.) and dedup would bury the recoverable copy.
      const existing = entry.sessionId ? s.voiceHistory.find(e => e.sessionId === entry.sessionId) : undefined;
      const next = boundVoiceHistory([
        { id: existing?.id ?? now, ...entry },
        ...s.voiceHistory.filter(e => !entry.sessionId || e.sessionId !== entry.sessionId),
      ]);
      try { localStorage.setItem(VOICE_HISTORY_KEY, JSON.stringify(next)); } catch { queueMicrotask(() => get().toast('Voice history could not be saved on this device. This session is still available until page reload.', { kind: 'warn' })); }
      return { voiceHistory: next };
    });
  },
  removeVoiceCheckpoint: (sessionId) => {
    const next = get().voiceHistory.filter(e => e.sessionId !== sessionId);
    set({ voiceHistory: next });
    try { localStorage.setItem(VOICE_HISTORY_KEY, JSON.stringify(next)); } catch { /* memory remains authoritative */ }
  },
  clearVoiceHistory: () => {
    try { localStorage.removeItem(VOICE_HISTORY_KEY); } catch { /* ignore */ }
    set({ voiceHistory: [] });
  },
  reinsertVoiceHistory: (id) => {
    const entry = get().voiceHistory.find((e) => e.id === id);
    if (!entry) return false;
    return get().sendToFocused(entry.text);
  },
});

export default createVoiceSlice;
