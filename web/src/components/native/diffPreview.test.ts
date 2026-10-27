// Unit tests for the native approval/tool-call diff preview parsing + line diff.
// These guard the killer affordance (red/green diff on Write/Edit/MultiEdit) and
// the keyboard-gating risk classification.

import { describe, it, expect } from 'vitest';
import {
  parseFileDiff,
  diffLines,
  classifyApprovalRisk,
} from './NativeEventViews';

describe('parseFileDiff', () => {
  it('parses Write as a single all-additions hunk', () => {
    const d = parseFileDiff('Write', { file_path: '/a/b.ts', content: 'line1\nline2' });
    expect(d).toEqual({ filePath: '/a/b.ts', hunks: [{ before: '', after: 'line1\nline2' }] });
  });

  it('parses Edit as one old→new hunk', () => {
    const d = parseFileDiff('Edit', {
      file_path: '/a/b.ts',
      old_string: 'foo',
      new_string: 'bar',
    });
    expect(d).toEqual({ filePath: '/a/b.ts', hunks: [{ before: 'foo', after: 'bar' }] });
  });

  it('parses MultiEdit as one hunk per edit', () => {
    const d = parseFileDiff('MultiEdit', {
      file_path: '/a/b.ts',
      edits: [
        { old_string: 'a', new_string: 'A' },
        { old_string: 'b', new_string: 'B' },
      ],
    });
    expect(d?.hunks).toEqual([
      { before: 'a', after: 'A' },
      { before: 'b', after: 'B' },
    ]);
  });

  it('tolerates a missing new_string (deletion) and filePath aliases', () => {
    const d = parseFileDiff('Edit', { filePath: '/x.ts', old_string: 'gone' });
    expect(d).toEqual({ filePath: '/x.ts', hunks: [{ before: 'gone', after: '' }] });
  });

  it('returns null for unrecognized / malformed shapes', () => {
    expect(parseFileDiff('Write', { file_path: '/a' })).toBeNull(); // no content
    expect(parseFileDiff('Edit', { file_path: '/a' })).toBeNull(); // no old/new
    expect(parseFileDiff('MultiEdit', { file_path: '/a', edits: [] })).toBeNull();
    expect(parseFileDiff('Bash', { command: 'ls' })).toBeNull();
    expect(parseFileDiff('Edit', 'not-an-object')).toBeNull();
  });
});

describe('diffLines', () => {
  it('marks every line of a pure addition as add', () => {
    expect(diffLines('', 'x\ny')).toEqual([
      { kind: 'add', text: 'x' },
      { kind: 'add', text: 'y' },
    ]);
  });

  it('marks every line of a pure deletion as del', () => {
    expect(diffLines('x\ny', '')).toEqual([
      { kind: 'del', text: 'x' },
      { kind: 'del', text: 'y' },
    ]);
  });

  it('keeps shared lines as context and diverging lines as del+add', () => {
    const out = diffLines('keep\nold\ntail', 'keep\nnew\ntail');
    expect(out).toContainEqual({ kind: 'ctx', text: 'keep' });
    expect(out).toContainEqual({ kind: 'ctx', text: 'tail' });
    expect(out).toContainEqual({ kind: 'del', text: 'old' });
    expect(out).toContainEqual({ kind: 'add', text: 'new' });
  });
});

describe('classifyApprovalRisk (keyboard gating contract)', () => {
  it('flags file-edit tools as destructive (Enter must NOT auto-approve)', () => {
    for (const t of ['Write', 'Edit', 'MultiEdit', 'NotebookEdit']) {
      expect(classifyApprovalRisk(t, { file_path: '/a' }).destructive).toBe(true);
    }
  });

  it('flags read-like tools as readOnly (and not destructive)', () => {
    for (const t of ['Read', 'Grep', 'Glob', 'LS', 'WebFetch', 'WebSearch']) {
      const r = classifyApprovalRisk(t, {});
      expect(r.readOnly).toBe(true);
      expect(r.destructive).toBe(false);
    }
  });

  it('flags shell mutations/redirects as destructive but a plain command not', () => {
    expect(classifyApprovalRisk('Bash', { command: 'rm -rf x' }).destructive).toBe(true);
    expect(classifyApprovalRisk('Bash', { command: 'echo hi > f' }).destructive).toBe(true);
    expect(classifyApprovalRisk('Bash', { command: 'ls -la' }).destructive).toBe(false);
  });

  it('fails closed for unknown provider tools', () => {
    expect(classifyApprovalRisk('CommandExecution', { command: 'git status' })).toEqual({
      readOnly: false,
      destructive: false,
    });
  });
});
