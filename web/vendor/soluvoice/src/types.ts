export type VoicePhase = 'idle' | 'recording' | 'transcribing';

// Mirrors the server's `cleanup` field. "none" = no transcription has
// completed yet this session.
//   ok       — Whisper + LLM polish (best quality)
//   disabled — no cleanup key on the server (raw Whisper)
//   failed   — cleanup call errored (raw Whisper; check server logs)
//   skipped  — cleanup output rejected by safety guards (raw Whisper)
// Absent field from the server = treat as disabled.
export type CleanupStatus = 'none' | 'ok' | 'disabled' | 'failed' | 'skipped';

export interface TranscribeResult extends VoiceDiagnostics {
  text: string;
  cleanup?: string;
}

export interface TranscribeOpts {
  /** e.g. "audio.webm" — extension matches the recorded MIME. */
  filename: string;
  /** Whisper language hint, e.g. "en". Omit for auto-detect. */
  language?: string;
  /**
   * App-provided disambiguation context for the cleanup LLM (e.g. the
   * last ~50 lines of a terminal buffer, the surrounding document).
   * Best-effort; omit if you have none.
   */
  context?: string;
  signal: AbortSignal;
}

export type TranscribeFn = (audio: Blob, opts: TranscribeOpts) => Promise<TranscribeResult>;

export type CaptureStopReason =
  | 'user'
  | 'blur'
  | 'duration'
  | 'segments'
  | 'backlog'
  | 'text_limit'
  | 'error'
  | 'disposed'
  | 'interrupted';
export interface CaptureProgress {
  capturedMs: number;
  activeBytes: number;
  segments: number;
  startDelayMs?: number;
  warm?: boolean;
}
export interface CaptureSegment {
  index: number;
  blob: Blob;
  frames: number;
  durationMs: number;
  endFrame: number;
}
export interface VoiceDiagnostics {
  rawText?: string;
  requestedLanguage?: string;
  detectedLanguage?: string;
  audioDurationMs?: number;
  transcriptionProvider?: string;
  cleanupProvider?: string;
}
