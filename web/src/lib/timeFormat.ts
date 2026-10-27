// Exact-time and duration formatting for the Tasks dashboard. Concise
// relative values live in cells (sidebar/time.ts relativeTime); these provide
// the exact absolute date/time with timezone for tooltips and the detail
// timing block. Missing data always renders as an em dash, never a made-up 0.

export const EM_DASH = '—';

// dateStyle/timeStyle cannot be combined with timeZoneName (Intl throws), so
// the equivalent explicit component options are spelled out instead.
const absoluteFormat = new Intl.DateTimeFormat(undefined, {
  year: 'numeric',
  month: 'short',
  day: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  timeZoneName: 'short',
});

export function formatAbsolute(iso: string | null | undefined): string {
  if (!iso) return EM_DASH;
  const ms = Date.parse(iso);
  if (Number.isNaN(ms)) return EM_DASH;
  return absoluteFormat.format(ms);
}

// Compact wall-clock durations: '2h 14m', '3m 05s', '12s'.
export function formatDuration(ms: number | null | undefined): string {
  if (ms == null || !Number.isFinite(ms) || ms < 0) return EM_DASH;
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${String(seconds % 60).padStart(2, '0')}s`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ${String(minutes % 60).padStart(2, '0')}m`;
  return `${Math.floor(hours / 24)}d ${hours % 24}h`;
}
