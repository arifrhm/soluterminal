// The chip drawn over a terminal pane while a dictation targets it
// (paneDictation slot).
import type { PaneDictation } from '../extensions';
import { IconClock, IconMic, IconX } from '../components/ui/icons';

export function PaneDictationChip({ phase, aboutToCancel }: PaneDictation) {
  return (
    <div
      className={`pane-dictating-chip phase-${phase} ${aboutToCancel ? 'about-to-cancel' : ''}`}
      aria-live="polite"
    >
      <span className="pane-dictating-dot" aria-hidden />
      <span className="pane-dictating-icon" aria-hidden>
        {(phase === 'transcribing' || phase === 'draining')
          ? <IconClock size={13} style={{ verticalAlign: '-2px' }} />
          : aboutToCancel ? <IconX size={13} style={{ verticalAlign: '-2px' }} /> : <IconMic size={13} style={{ verticalAlign: '-2px' }} />}
      </span>
      <span>
        {(phase === 'transcribing' || phase === 'draining')
          ? 'Transcribing — will insert here'
          : aboutToCancel
            ? 'Release to cancel'
            : phase === 'recovery' ? 'Dictation paused · review your recording'
              : phase === 'starting' ? 'Preparing microphone…'
                : 'Listening · Alt+C or slide to cancel'}
      </span>
    </div>
  );
}
