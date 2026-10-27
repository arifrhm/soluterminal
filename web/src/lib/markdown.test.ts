import { describe, it, expect } from 'vitest';
import {
  parseMarkdown,
  parseInline,
  flattenHeadingsForFocus,
  type Block,
  type Inline,
  type ListItem,
} from './markdown';

// Helper: flatten an Inline[] to its visible text (ignoring markup kind).
function inlineText(inlines: Inline[]): string {
  return inlines
    .map((n) => {
      switch (n.kind) {
        case 'text':
        case 'code':
          return n.text;
        case 'link':
          return n.text;
        case 'strong':
        case 'em':
          return inlineText(n.children);
      }
    })
    .join('');
}

function only<K extends Block['kind']>(blocks: Block[], kind: K): Extract<Block, { kind: K }> {
  const b = blocks.find((x) => x.kind === kind);
  expect(b, `expected a ${kind} block`).toBeTruthy();
  return b as Extract<Block, { kind: K }>;
}

// ---------------------------------------------------------------------------
// Regression: existing behavior must still work.
// ---------------------------------------------------------------------------

describe('markdown — existing behavior preserved', () => {
  it('parses headings', () => {
    const b = only(parseMarkdown('## Hello'), 'heading');
    expect(b.level).toBe(2);
    expect(inlineText(b.inlines)).toBe('Hello');
  });

  it('parses paragraphs', () => {
    const b = only(parseMarkdown('just a line'), 'paragraph');
    expect(inlineText(b.inlines)).toBe('just a line');
  });

  it('parses fenced code with a language label', () => {
    const b = only(parseMarkdown('```ts\nconst x = 1\n```'), 'code');
    expect(b.langLabel).toBe('ts');
    expect(b.code).toBe('const x = 1');
  });

  it('parses blockquotes, hr, and links', () => {
    const blocks = parseMarkdown('> quoted\n\n---\n\n[t](http://e.com)');
    expect(only(blocks, 'quote')).toBeTruthy();
    expect(only(blocks, 'hr')).toBeTruthy();
    const p = only(blocks, 'paragraph');
    expect(p.inlines.some((n) => n.kind === 'link' && n.href === 'http://e.com')).toBe(true);
  });

  it('keeps **bold** and *italic* working', () => {
    const inl = parseInline('a **b** and *c*');
    expect(inl.some((n) => n.kind === 'strong')).toBe(true);
    expect(inl.some((n) => n.kind === 'em')).toBe(true);
  });

  it('parses simple ordered / unordered lists', () => {
    const ul = only(parseMarkdown('- a\n- b'), 'list');
    expect(ul.ordered).toBe(false);
    expect(ul.items.map((it) => inlineText(it.inlines))).toEqual(['a', 'b']);

    const ol = only(parseMarkdown('1. one\n2. two'), 'list');
    expect(ol.ordered).toBe(true);
    expect(ol.items.map((it) => inlineText(it.inlines))).toEqual(['one', 'two']);
  });
});

// ---------------------------------------------------------------------------
// 1. Tables
// ---------------------------------------------------------------------------

describe('markdown — GFM tables', () => {
  it('parses a header, alignment, and body rows', () => {
    const src = ['| Name | Age |', '|:-----|----:|', '| Ada  | 36  |', '| Bob  | 7   |'].join('\n');
    const t = only(parseMarkdown(src), 'table');
    expect(t.header.map(inlineText)).toEqual(['Name', 'Age']);
    expect(t.align).toEqual(['left', 'right']);
    expect(t.rows.length).toBe(2);
    expect(t.rows[0].map(inlineText)).toEqual(['Ada', '36']);
    expect(t.rows[1].map(inlineText)).toEqual(['Bob', '7']);
  });

  it('supports center alignment (:--:) and left default (null)', () => {
    const src = ['| a | b | c |', '| :-: | --- | --: |', '| 1 | 2 | 3 |'].join('\n');
    const t = only(parseMarkdown(src), 'table');
    expect(t.align).toEqual(['center', null, 'right']);
  });

  it('handles escaped pipes inside cells', () => {
    const src = ['| col |', '| --- |', '| a \\| b |'].join('\n');
    const t = only(parseMarkdown(src), 'table');
    expect(t.rows[0].map(inlineText)).toEqual(['a | b']);
  });

  it('renders inline markup inside cells', () => {
    const src = ['| h |', '| --- |', '| **bold** `code` |'].join('\n');
    const t = only(parseMarkdown(src), 'table');
    const cell = t.rows[0][0];
    expect(cell.some((n) => n.kind === 'strong')).toBe(true);
    expect(cell.some((n) => n.kind === 'code')).toBe(true);
  });

  it('does NOT treat a lone piped line (no delimiter row) as a table', () => {
    const blocks = parseMarkdown('| not | a | table |');
    expect(blocks.find((b) => b.kind === 'table')).toBeUndefined();
    expect(only(blocks, 'paragraph')).toBeTruthy();
  });

  it('normalizes a ragged row to header width', () => {
    const src = ['| a | b |', '| - | - |', '| 1 |'].join('\n');
    const t = only(parseMarkdown(src), 'table');
    expect(t.rows[0].length).toBe(2);
  });
});

// ---------------------------------------------------------------------------
// 2. Task lists
// ---------------------------------------------------------------------------

describe('markdown — task lists', () => {
  it('parses unchecked and checked items with a task flag', () => {
    const src = ['- [ ] todo', '- [x] done', '- [X] also done'].join('\n');
    const l = only(parseMarkdown(src), 'list');
    expect(l.items.map((it) => it.task)).toEqual([false, true, true]);
    expect(l.items.map((it) => inlineText(it.inlines))).toEqual(['todo', 'done', 'also done']);
  });

  it('leaves the literal "[ ]" out of the rendered text', () => {
    const l = only(parseMarkdown('- [ ] thing'), 'list');
    expect(inlineText(l.items[0].inlines)).toBe('thing');
    expect(inlineText(l.items[0].inlines)).not.toContain('[ ]');
  });

  it('ordinary bullets have no task flag', () => {
    const l = only(parseMarkdown('- plain'), 'list');
    expect(l.items[0].task).toBeUndefined();
  });
});

// ---------------------------------------------------------------------------
// 3. Nested lists
// ---------------------------------------------------------------------------

describe('markdown — nested lists', () => {
  it('nests a 2-space indented bullet under its parent', () => {
    const src = ['- parent', '  - child', '  - child2', '- parent2'].join('\n');
    const l = only(parseMarkdown(src), 'list');
    expect(l.items.length).toBe(2);
    const child = l.items[0].children as Extract<Block, { kind: 'list' }>;
    expect(child).toBeTruthy();
    expect(child.items.map((it: ListItem) => inlineText(it.inlines))).toEqual(['child', 'child2']);
    expect(inlineText(l.items[1].inlines)).toBe('parent2');
  });

  it('nests an ordered list under an unordered item', () => {
    const src = ['- top', '  1. first', '  2. second'].join('\n');
    const l = only(parseMarkdown(src), 'list');
    const child = l.items[0].children as Extract<Block, { kind: 'list' }>;
    expect(child.ordered).toBe(true);
    expect(child.items.map((it: ListItem) => inlineText(it.inlines))).toEqual(['first', 'second']);
  });

  it('handles tab indentation', () => {
    const src = ['- top', '\t- deep'].join('\n');
    const l = only(parseMarkdown(src), 'list');
    expect(l.items[0].children).toBeTruthy();
  });

  it('a flat list (no indent) stays flat', () => {
    const l = only(parseMarkdown('- a\n- b\n- c'), 'list');
    expect(l.items.every((it) => it.children === undefined)).toBe(true);
    expect(l.items.length).toBe(3);
  });

  it('supports two levels of nesting', () => {
    const src = ['- a', '  - b', '    - c'].join('\n');
    const l = only(parseMarkdown(src), 'list');
    const lvl1 = l.items[0].children as Extract<Block, { kind: 'list' }>;
    const lvl2 = lvl1.items[0].children as Extract<Block, { kind: 'list' }>;
    expect(inlineText(lvl2.items[0].inlines)).toBe('c');
  });
});

// ---------------------------------------------------------------------------
// 4. Emphasis inside words / code
// ---------------------------------------------------------------------------

describe('markdown — emphasis boundaries', () => {
  it('does NOT italicize inside snake_case_words', () => {
    const inl = parseInline('call some_snake_case_fn now');
    expect(inl.some((n) => n.kind === 'em')).toBe(false);
    expect(inlineText(inl)).toBe('call some_snake_case_fn now');
  });

  it('does NOT apply emphasis inside an inline code span', () => {
    const inl = parseInline('`a_b_c` and `x*y*z`');
    // Both spans are single code tokens; no em/strong leaked in.
    expect(inl.filter((n) => n.kind === 'code').length).toBe(2);
    expect(inl.some((n) => n.kind === 'em')).toBe(false);
    const codes = inl.filter((n): n is Extract<Inline, { kind: 'code' }> => n.kind === 'code');
    expect(codes[0].text).toBe('a_b_c');
    expect(codes[1].text).toBe('x*y*z');
  });

  it('still italicizes underscores at word boundaries', () => {
    const inl = parseInline('an _italic_ word');
    const em = inl.find((n) => n.kind === 'em');
    expect(em).toBeTruthy();
    expect(em && em.kind === 'em' && inlineText(em.children)).toBe('italic');
  });

  it('keeps single * emphasis from swallowing ** strong', () => {
    const inl = parseInline('*a* **b**');
    expect(inl.filter((n) => n.kind === 'em').length).toBe(1);
    expect(inl.filter((n) => n.kind === 'strong').length).toBe(1);
  });

  it('does not match a * across an inline code span', () => {
    // The `*` after the code should NOT pair with the one before it.
    const inl = parseInline('a *b `c*d` e* f');
    // There is a valid em from the first * to the last *, but the code span in
    // between must stay intact as a code token.
    expect(inl.some((n) => n.kind === 'em')).toBe(true);
    // The code content must be preserved literally somewhere in the tree.
    expect(inlineText(inl)).toContain('c*d');
  });

  it('falls back to literal text for an unmatched marker', () => {
    const inl = parseInline('unbalanced *marker here');
    expect(inl.some((n) => n.kind === 'em')).toBe(false);
    expect(inlineText(inl)).toBe('unbalanced *marker here');
  });
});

// ---------------------------------------------------------------------------
// Focus-mode run-in heading transform.
// ---------------------------------------------------------------------------
describe('flattenHeadingsForFocus', () => {
  it('merges a heading + following paragraph into one bold run-in line', () => {
    const blocks = parseMarkdown('## The feature\nYou flagged the strip was capped.');
    const out = flattenHeadingsForFocus(blocks);
    // No heading survives; the result is a single paragraph.
    expect(out.some((b) => b.kind === 'heading')).toBe(false);
    expect(out.length).toBe(1);
    const p = out[0] as Extract<Block, { kind: 'paragraph' }>;
    expect(p.kind).toBe('paragraph');
    // Opens with a strong lead-in carrying the heading text, then " — ", then prose.
    expect(p.inlines[0].kind).toBe('strong');
    expect(inlineText([p.inlines[0]])).toBe('The feature');
    expect(inlineText(p.inlines)).toBe('The feature — You flagged the strip was capped.');
  });

  it('demotes a heading with no following paragraph to a lone bold line', () => {
    // Heading directly above a list (common): becomes a bold lead-in, list intact.
    const blocks = parseMarkdown('## Steps\n- one\n- two');
    const out = flattenHeadingsForFocus(blocks);
    expect(out.some((b) => b.kind === 'heading')).toBe(false);
    expect(out[0].kind).toBe('paragraph');
    expect(inlineText((out[0] as Extract<Block, { kind: 'paragraph' }>).inlines)).toBe('Steps');
    expect(out[1].kind).toBe('list');
  });

  it('is a no-op on heading-free content', () => {
    const blocks = parseMarkdown('just a paragraph\n\n- a\n- b');
    const out = flattenHeadingsForFocus(blocks);
    expect(out).toEqual(blocks);
  });
});
