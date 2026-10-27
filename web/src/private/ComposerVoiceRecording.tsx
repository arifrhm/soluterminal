// The overlay drawn over the composer box while dictating: the elapsed meter,
// the hold hints, and the hands-free cancel/stop buttons (composerVoice slot).
import type { ComposerVoiceState } from '../extensions';
import { RecordingMeter } from '../components/composer/RecordingMeter';
import { IconStop } from '../components/composer/icons';
import { IconLock } from '../components/ui/icons';

export function ComposerVoiceRecording({ voice: cv }: { voice: ComposerVoiceState }) {
  return (
    <div
      className={`composer-rec${cv.slide === 'cancel' ? ' is-cancel' : ''}${cv.slide === 'lock' ? ' is-lock' : ''}${cv.handsFree ? ' is-handsfree' : ''}`}

    >
      {cv.transcribing ? (
        <span className="composer-rec-status">Transcribing…</span>
      ) : (
        <>
          <span className="composer-rec-dot" aria-hidden="true" />
          <RecordingMeter elapsedMs={cv.elapsedMs} />
          {cv.handsFree ? (
            <span className="composer-rec-actions">
              <button
                type="button"
                className="composer-rec-cancel"
                onClick={cv.cancel}
                aria-label="Cancel recording"
                title="Cancel"
              >
                ✕
              </button>
              <button
                type="button"
                className="composer-rec-stop"
                onClick={cv.stopAndSend}
                aria-label="Stop and transcribe"
                title="Stop"
              >
                <IconStop />
              </button>
            </span>
          ) : (
            <span
              className={`composer-rec-hint${cv.slide === 'cancel' ? ' is-cancel' : ''}${cv.slide === 'lock' ? ' is-lock' : ''}`}
            >
              {cv.slide === 'cancel'
                ? 'release to cancel'
                : cv.slide === 'lock'
                  ? <>release to lock <IconLock size={11} style={{ verticalAlign: '-1px' }} /></>
                  : 'slide up to lock · left to cancel'}
            </span>
          )}
        </>
      )}
    </div>
  );
}
