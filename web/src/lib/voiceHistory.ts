// Voice history labels shared by terminal dictation (VoiceButton) and the
// message box (useComposerVoice), so every dictation explains its cleanup the
// same way wherever it was spoken.
import type { CleanupStatus } from 'soluvoice';

import type { VoicePolish } from '../private/voice.slice';

// Below this the server skips cleanup (minBytesForCleanup) — nothing to polish.
export const VOICE_POLISH_MIN_CHARS = 25;

// History label for a one-shot (wait-for-cleanup) dictation. The server
// reports a short clip as "disabled"/"skipped", so length decides "short".
export function oneShotPolish(cleanup: CleanupStatus, text: string): VoicePolish {
  if (text.trim().length < VOICE_POLISH_MIN_CHARS) return 'short';
  if (cleanup === 'ok') return 'cleaned';
  if (cleanup === 'disabled' || cleanup === 'none') return 'off';
  return 'failed';
}

export interface VoiceSegmentResult {
  index: number;
  text: string;
  rawText?: string;
  requestedLanguage?: string;
  detectedLanguage?: string;
  audioDurationMs?: number;
  transcriptionProvider?: string;
  cleanupProvider?: string;
  cleanup?: string;
}
/** No lexical deduplication: repeated words and code-switching are legitimate. */
export function aggregateVoiceSegments(segments: readonly (VoiceSegmentResult | undefined)[]) {
  const join = (raw: boolean) => {
    const parts: string[] = [];
    let missing = false;
    for (const segment of segments) {
      if (!segment) {
        missing = true;
        continue;
      }
      const text = (raw ? (segment.rawText ?? segment.text) : segment.text).trim();
      if (missing) {
        parts.push('[Recording part omitted]');
        missing = false;
      }
      if (text) parts.push(text);
    }
    if (missing) parts.push('[Recording part omitted]');
    return parts.join(' ');
  };
  const detected = new Set(
    segments.flatMap((s) => (s?.detectedLanguage ? [s.detectedLanguage] : [])),
  );
  return {
    text: join(false),
    raw: join(true),
    partial: segments.some((s) => !s),
    detectedLanguage: detected.size > 1 ? 'mixed' : [...detected][0],
  };
}
