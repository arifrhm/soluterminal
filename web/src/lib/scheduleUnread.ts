import type { Schedule } from '../types';

// A task schedule is "unread" when its newest run landed after the user
// last opened its report — the signal behind the sidebar dot and the
// "N new" chip. Errors count: a failure you haven't seen matters MORE
// than a digest you haven't seen. Skips don't (nothing was produced).
// Non-task kinds never count — their runs aren't reports.
export function scheduleUnread(s: Schedule): boolean {
  if (s.kind !== 'task') return false;
  const newest = s.runs?.[0];
  if (!newest || newest.status === 'skipped') return false;
  if (!s.lastReadAt) return true;
  return new Date(newest.at).getTime() > new Date(s.lastReadAt).getTime();
}
