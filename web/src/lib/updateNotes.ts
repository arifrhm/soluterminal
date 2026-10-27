// Release notes arrive as the raw git-log string baked into update.json by
// publish.sh — a header line plus one bullet per commit, e.g.:
//
//   Changes since v0.0.712:
//
//   - feat(web): move phone-access indicator to footer (v0.0.713)
//   - fix(native): stream native panes over WebSocket — removes the limit
//
// That's developer-facing noise. parseNotes() turns it into short, scannable,
// typed highlights for the update UI: drop the header + bullet, strip the
// conventional-commit type/scope prefix and the trailing "(vX.Y.Z)" stamp,
// remember whether it was a feat/fix/other, and sentence-case the remainder.

export type NoteKind = 'feat' | 'fix' | 'other';

export interface NoteLine {
  text: string;
  kind: NoteKind;
}

const HEADER_LINE = /^(?:changes since|recent changes)\b/i;
const LEADING_BULLET = /^[\s\-*•]+/;
const TYPE_PREFIX =
  /^(feat|fix|chore|docs|refactor|perf|test|style|build|ci|revert)(?:\([^)]*\))?!?:\s*/i;
const TRAILING_VERSION = /\s*\(v\d+\.\d+\.\d+\)\s*$/;

function cleanNote(raw: string): NoteLine | null {
  let s = raw.trim();
  if (!s || HEADER_LINE.test(s)) return null;
  s = s.replace(LEADING_BULLET, '').trim();
  if (!s) return null;

  const typeMatch = s.match(TYPE_PREFIX);
  const type = typeMatch?.[1]?.toLowerCase();
  const kind: NoteKind = type === 'feat' ? 'feat' : type === 'fix' ? 'fix' : 'other';

  s = s.replace(TYPE_PREFIX, '').replace(TRAILING_VERSION, '').trim();
  if (!s) return null;
  s = s.charAt(0).toUpperCase() + s.slice(1);
  return { text: s, kind };
}

export function parseNotes(notes: string | undefined | null): NoteLine[] {
  if (!notes) return [];
  const out: NoteLine[] = [];
  for (const line of notes.split('\n')) {
    const c = cleanNote(line);
    if (c) out.push(c);
  }
  return out;
}

// First real highlight (for the post-update "what's new" toast headline).
export function firstHighlight(notes: string | undefined | null): string {
  return parseNotes(notes)[0]?.text ?? '';
}
