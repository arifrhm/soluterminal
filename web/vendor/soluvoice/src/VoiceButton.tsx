import type { CleanupStatus } from './types.js';
import type { Voice } from './useVoice.js';

// Tooltip shown when hovering the cleanup status dot. Kept separate
// from the mic's main title so it doesn't clobber the hold-to-talk hint.
export function cleanupTooltip(s: CleanupStatus): string {
  switch (s) {
    case 'ok':
      return 'Last transcription was cleaned by the LLM (filler words removed, punctuation fixed).';
    case 'disabled':
      return 'Cleanup is disabled on the server. Raw Whisper output was inserted.';
    case 'skipped':
      return 'Cleanup output was rejected by safety guards. Raw Whisper output was inserted.';
    case 'failed':
      return 'Cleanup errored. Raw Whisper output was inserted.';
    default:
      return '';
  }
}

export interface VoiceButtonProps {
  /** The useVoice() instance driving this button. */
  voice: Voice<any>;
  /** Disable when the app has nowhere to deliver a transcript. */
  hasTarget?: boolean;
  /** Override the idle tooltip, e.g. "Hold to talk — dictate into chat". */
  idleLabel?: string;
  /** Tooltip when hasTarget is false. */
  noTargetLabel?: string;
  className?: string;
}

/**
 * Hold-to-talk mic button: press/hold records, release transcribes,
 * slide off to cancel. Import 'soluvoice/styles.css' (or style the
 * .soluvoice-* classes yourself).
 */
export function VoiceButton({
  voice,
  hasTarget = true,
  idleLabel = 'Hold to talk — release to transcribe',
  noTargetLabel = 'Focus an input first',
  className = '',
}: VoiceButtonProps) {
  const { phase, cleanupStatus, holdHandlers } = voice;

  const label =
    phase === 'recording'
      ? 'Recording… release to transcribe'
      : phase === 'transcribing'
        ? 'Transcribing…'
        : hasTarget
          ? idleLabel
          : noTargetLabel;

  const icon = phase === 'transcribing' ? '…' : '🎤';

  return (
    <button
      type="button"
      className={`soluvoice-btn state-${phase} ${className}`.trim()}
      disabled={phase === 'transcribing' || !hasTarget}
      title={label}
      aria-label={label}
      aria-pressed={phase === 'recording'}
      {...holdHandlers}
    >
      <span className="soluvoice-btn-icon" aria-hidden>
        {icon}
      </span>
      {cleanupStatus !== 'none' && (
        <span
          className={`soluvoice-cleanup-dot cleanup-${cleanupStatus}`}
          title={cleanupTooltip(cleanupStatus)}
          aria-hidden
        />
      )}
    </button>
  );
}
