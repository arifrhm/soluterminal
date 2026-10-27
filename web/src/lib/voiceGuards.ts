// --- typed input per pane -------------------------------------------------

const lastTypedAt = new Map<string, number>();

/** Called for every keystroke the user sends to a terminal pane. */
export function notePaneTyped(paneId: string): void {
  lastTypedAt.set(paneId, Date.now());
}

/** True when the user typed into the pane at or after `since` (ms). */
export function paneTypedSince(paneId: string, since: number): boolean {
  const at = lastTypedAt.get(paneId);
  return at !== undefined && at >= since;
}

export function resetPaneTypedForTests(): void {
  lastTypedAt.clear();
}

// Cloud limits the entire multipart body to 4 MiB, not recording duration.
export const VOICE_MAX_RECORD_MS = 30 * 60_000;
export const VOICE_MAX_SEGMENTS = 36;
export const VOICE_MAX_AUDIO_BYTES = 8 * 1024 * 1024;
export const VOICE_WARN_AUDIO_BYTES = 6 * 1024 * 1024;
export const VOICE_AUDIO_RESERVE_BYTES = 64 * 1024;
export const VOICE_SEGMENT_TEXT_BYTES = 32 * 1024;
export const VOICE_SESSION_TEXT_BYTES = 256 * 1024;
