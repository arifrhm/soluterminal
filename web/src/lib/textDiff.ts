// A line-level unified diff, for the Template tab's policy comparison
// (docs/DESIGN-agents-surface.md §4.5).
//
// Dependency-free and deliberately small: the only diff renderer in the app is
// the git review pane, which reads unified diffs GIT already produced. Nothing
// here computes one, and pulling a diff library in for two paragraphs of policy
// text would be a dependency the product does not otherwise need.
//
// Data, not UI (the scheduleTemplates.ts stance): this returns rows, and the
// component decides what a +/− gutter looks like.

export type DiffKind = 'same' | 'add' | 'del';

export interface DiffRow {
  kind: DiffKind;
  text: string;
}

/**
 * Longest-common-subsequence line diff from `a` to `b`.
 *
 * Rows come back in reading order, with deletions before the additions that
 * replace them — the shape a unified diff has, so a reader can see the old
 * sentence and the new one adjacent rather than in two separate blocks.
 */
export function diffLines(a: string, b: string): DiffRow[] {
  const left = splitLines(a);
  const right = splitLines(b);

  // lcs[i][j] = length of the longest common subsequence of left[i:], right[j:].
  const lcs: number[][] = Array.from({ length: left.length + 1 }, () =>
    new Array<number>(right.length + 1).fill(0),
  );
  for (let i = left.length - 1; i >= 0; i--) {
    for (let j = right.length - 1; j >= 0; j--) {
      lcs[i][j] = left[i] === right[j]
        ? lcs[i + 1][j + 1] + 1
        : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
    }
  }

  const rows: DiffRow[] = [];
  let i = 0;
  let j = 0;
  while (i < left.length && j < right.length) {
    if (left[i] === right[j]) {
      rows.push({ kind: 'same', text: left[i] });
      i++;
      j++;
    } else if (lcs[i + 1][j] >= lcs[i][j + 1]) {
      rows.push({ kind: 'del', text: left[i] });
      i++;
    } else {
      rows.push({ kind: 'add', text: right[j] });
      j++;
    }
  }
  for (; i < left.length; i++) rows.push({ kind: 'del', text: left[i] });
  for (; j < right.length; j++) rows.push({ kind: 'add', text: right[j] });
  return rows;
}

// An empty string is zero lines, not one empty line: "" vs "hello" is one
// addition, and rendering a phantom blank deletion above it would tell the user
// something was removed that never existed.
function splitLines(text: string): string[] {
  if (text === '') return [];
  return text.replace(/\r\n/g, '\n').split('\n');
}
