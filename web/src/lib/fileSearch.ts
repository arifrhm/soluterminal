// Pure matching logic for the FileTile find bar, extracted from the component
// so it can be unit-tested without mounting React.
//
// Returns the 0-based indices of lines that match `query`:
//   - a bare ":42" is a jump-to-line (1-based, clamped to range) so agent
//     references like "auth.go:153" can be navigated by hand;
//   - anything else is a case-insensitive substring match across lines.
export function fileSearchMatches(lines: string[], query: string): number[] {
  const q = query.trim();
  if (!q) return [];
  const jump = /^:(\d+)$/.exec(q);
  if (jump) {
    if (lines.length === 0) return [];
    const n = Math.min(Math.max(1, parseInt(jump[1], 10)), lines.length) - 1;
    return [n];
  }
  const lc = q.toLowerCase();
  const out: number[] = [];
  lines.forEach((l, i) => { if (l.toLowerCase().includes(lc)) out.push(i); });
  return out;
}
