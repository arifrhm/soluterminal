import { describe, expect, it } from 'vitest';
import { diffLines } from './textDiff';

// Compact notation so an expectation reads like the diff it describes.
const render = (a: string, b: string) =>
  diffLines(a, b).map((r) => `${r.kind === 'add' ? '+' : r.kind === 'del' ? '-' : ' '}${r.text}`);

describe('diffLines', () => {
  it('reports every line as unchanged for identical text', () => {
    expect(render('one\ntwo', 'one\ntwo')).toEqual([' one', ' two']);
  });

  it('marks inserted lines', () => {
    expect(render('one\nthree', 'one\ntwo\nthree')).toEqual([' one', '+two', ' three']);
  });

  it('marks deleted lines', () => {
    expect(render('one\ntwo\nthree', 'one\nthree')).toEqual([' one', '-two', ' three']);
  });

  // A replaced line is a deletion adjacent to its addition — the shape that
  // lets a reader compare the old sentence with the new one in place.
  it('renders a replacement as a deletion followed by an addition', () => {
    expect(render('keep\nold\ntail', 'keep\nnew\ntail')).toEqual([' keep', '-old', '+new', ' tail']);
  });

  it('handles interleaved edits without losing a line', () => {
    const rows = diffLines('a\nb\nc\nd', 'a\nx\nc\ny\nd');
    expect(rows.filter((r) => r.kind !== 'del').map((r) => r.text)).toEqual(['a', 'x', 'c', 'y', 'd']);
    expect(rows.filter((r) => r.kind !== 'add').map((r) => r.text)).toEqual(['a', 'b', 'c', 'd']);
  });

  // An empty side is zero lines, not one blank one: a phantom blank row would
  // claim something was removed that never existed.
  it('treats an empty side as no lines at all', () => {
    expect(render('', 'hello')).toEqual(['+hello']);
    expect(render('hello', '')).toEqual(['-hello']);
    expect(render('', '')).toEqual([]);
  });

  it('normalizes CRLF so a line-ending change is not a whole-file rewrite', () => {
    expect(render('one\r\ntwo', 'one\ntwo')).toEqual([' one', ' two']);
  });
});
