// markdown.ts — a tiny, dependency-free Markdown → token-tree parser for the
// native renderer's assistant/thinking text. There is NO markdown library in
// package.json (no react-markdown / marked / remark), and pulling one in for
// this is overkill, so we hand-roll a block + inline tokenizer that covers the
// subset Claude actually emits: headings, fenced code blocks, GFM pipe tables,
// (nested, task) bullet/numbered lists, blockquotes, horizontal rules,
// paragraphs, and inline code / bold / italic / links. Anything we don't
// recognize falls through as plain text, so there is never a hard failure — at
// worst a construct renders verbatim.
//
// This file produces a data structure (Block[] / Inline[]); the React rendering
// lives in components/native/Markdown.tsx so this stays framework-free and unit
// testable. lib/highlight.ts (detectLang + tokenizeLine) is reused for fenced
// code blocks.

import { detectLang, type Lang } from './highlight';

// ---------------------------------------------------------------------------
// Block model
// ---------------------------------------------------------------------------

export type TableAlign = 'left' | 'center' | 'right' | null;

// A list item carries its own inline content plus an optional nested child list
// (indentation-based) and an optional task-checkbox state. `task` is undefined
// for ordinary items, true/false for `- [x]` / `- [ ]` items.
export interface ListItem {
  inlines: Inline[];
  children?: Extract<Block, { kind: 'list' }>;
  task?: boolean;
}

export type Block =
  | { kind: 'heading'; level: number; inlines: Inline[] }
  | { kind: 'paragraph'; inlines: Inline[]; runIn?: boolean }
  | { kind: 'code'; lang: Lang; langLabel: string; code: string }
  | { kind: 'list'; ordered: boolean; items: ListItem[] }
  | { kind: 'table'; align: TableAlign[]; header: Inline[][]; rows: Inline[][][] }
  | { kind: 'quote'; inlines: Inline[] }
  | { kind: 'hr' };

// ---------------------------------------------------------------------------
// Inline model
// ---------------------------------------------------------------------------

export type Inline =
  | { kind: 'text'; text: string }
  | { kind: 'code'; text: string }
  | { kind: 'strong'; children: Inline[] }
  | { kind: 'em'; children: Inline[] }
  | { kind: 'link'; text: string; href: string };

// flattenHeadingsForFocus — the Focus-mode "no document" pass over a parsed
// Block[]. A vibe-coding feed should never read like an essay, so a heading is
// demoted from a standalone <h1>–<h6> to a bold RUN-IN lead-in: a heading
// immediately followed by a paragraph merges into that paragraph as
// "**Heading** — paragraph text…" (one flowing line), and a heading with no
// following paragraph (e.g. above a list or code block) becomes a lone bold
// paragraph so the list/code still sits right under it. Pure + lossless of text
// (only the block shape changes); a no-op on heading-free content. Full mode
// never calls this, so it keeps real headings. Applied AFTER parse so streaming
// and settled text share one transform.
export function flattenHeadingsForFocus(blocks: Block[]): Block[] {
  const out: Block[] = [];
  for (let i = 0; i < blocks.length; i++) {
    const b = blocks[i];
    if (b.kind !== 'heading') {
      out.push(b);
      continue;
    }
    const lead: Inline = { kind: 'strong', children: b.inlines };
    const next = blocks[i + 1];
    // runIn marks a flattened heading so Focus CSS can give it a little extra
    // space ABOVE — the only section signal left once headings lose their size
    // step, so a long answer still reads as sections rather than one flat slab.
    if (next && next.kind === 'paragraph') {
      out.push({ kind: 'paragraph', runIn: true, inlines: [lead, { kind: 'text', text: ' — ' }, ...next.inlines] });
      i++; // consumed the following paragraph
    } else {
      out.push({ kind: 'paragraph', runIn: true, inlines: [lead] });
    }
  }
  return out;
}

// NOTE: the Focus "prose fold" (splitFoldBlocks / FoldableProse) was REMOVED. It
// folded an answer by POSITION (keep head + tail, hide the middle), which kept hiding
// substance — the conclusion in v0.0.600, the argument in v0.0.608 — because the model
// writes lede→substance→CTA with no fixed content map, so no fold position is ever
// safe. Focus now shows answer prose in full; compactness comes from terse generation
// + tool/thinking grouping, never from truncating the answer.

// ---------------------------------------------------------------------------
// Block parser
// ---------------------------------------------------------------------------

const HEADING_RE = /^(#{1,6})\s+(.*)$/;
const FENCE_RE = /^(```+|~~~+)\s*([\w.+-]*)\s*$/;
// List item matchers that also capture leading indentation (for nesting) and,
// for bullets, an optional task-list checkbox marker.
const UL_RE = /^([ \t]*)[-*+][ \t]+(.*)$/;
const OL_RE = /^([ \t]*)\d+[.)][ \t]+(.*)$/;
const TASK_RE = /^\[([ xX])\][ \t]+(.*)$/;
const QUOTE_RE = /^>\s?(.*)$/;
const HR_RE = /^\s*([-*_])(?:\s*\1){2,}\s*$/;

// A table row is any line whose trimmed form contains an (unescaped) pipe and
// looks like `| ... |` or `a | b`. The delimiter row is `|---|:--:|--:|` etc.
const TABLE_DELIM_CELL_RE = /^\s*:?-+:?\s*$/;

export function parseMarkdown(src: string): Block[] {
  const lines = src.replace(/\r\n?/g, '\n').split('\n');
  const blocks: Block[] = [];
  let i = 0;

  while (i < lines.length) {
    const line = lines[i];

    // Blank line — skip.
    if (line.trim() === '') {
      i++;
      continue;
    }

    // Fenced code block.
    const fence = line.match(FENCE_RE);
    if (fence) {
      const marker = fence[1][0]; // ` or ~
      const langLabel = fence[2] || '';
      const body: string[] = [];
      i++;
      while (i < lines.length && !new RegExp(`^${marker === '`' ? '`' : '~'}{3,}\\s*$`).test(lines[i])) {
        body.push(lines[i]);
        i++;
      }
      if (i < lines.length) i++; // consume closing fence
      const lang: Lang = langLabel ? detectLang(`x.${langLabel}`) : 'plain';
      blocks.push({ kind: 'code', lang, langLabel, code: body.join('\n') });
      continue;
    }

    // Horizontal rule.
    if (HR_RE.test(line)) {
      blocks.push({ kind: 'hr' });
      i++;
      continue;
    }

    // Heading.
    const heading = line.match(HEADING_RE);
    if (heading) {
      blocks.push({ kind: 'heading', level: heading[1].length, inlines: parseInline(heading[2]) });
      i++;
      continue;
    }

    // GFM pipe table: a header row, then a delimiter row, then zero+ body rows.
    if (isTableStart(lines, i)) {
      const table = parseTable(lines, i);
      blocks.push(table.block);
      i = table.next;
      continue;
    }

    // Blockquote (consume the contiguous run).
    if (QUOTE_RE.test(line)) {
      const buf: string[] = [];
      while (i < lines.length && QUOTE_RE.test(lines[i])) {
        buf.push(lines[i].match(QUOTE_RE)![1]);
        i++;
      }
      blocks.push({ kind: 'quote', inlines: parseInline(buf.join(' ')) });
      continue;
    }

    // List (bullet or ordered) — consume the contiguous run, honoring
    // indentation-based nesting.
    if (UL_RE.test(line) || OL_RE.test(line)) {
      const list = parseList(lines, i, indentWidth(leadingWhitespace(line)));
      blocks.push(list.block);
      i = list.next;
      continue;
    }

    // Paragraph — accumulate until a blank line or a block-starting line.
    const para: string[] = [];
    while (i < lines.length) {
      const l = lines[i];
      if (
        l.trim() === '' ||
        FENCE_RE.test(l) ||
        HEADING_RE.test(l) ||
        HR_RE.test(l) ||
        QUOTE_RE.test(l) ||
        UL_RE.test(l) ||
        OL_RE.test(l) ||
        isTableStart(lines, i)
      ) {
        break;
      }
      para.push(l);
      i++;
    }
    blocks.push({ kind: 'paragraph', inlines: parseInline(para.join('\n')) });
  }

  return blocks;
}

// ---------------------------------------------------------------------------
// Lists — indentation-based nesting + task-list checkboxes.
// ---------------------------------------------------------------------------

function leadingWhitespace(line: string): string {
  const m = line.match(/^[ \t]*/);
  return m ? m[0] : '';
}

// Treat a tab as 2 columns (same granularity as the 2-space convention).
function indentWidth(ws: string): number {
  let w = 0;
  for (const ch of ws) w += ch === '\t' ? 2 : 1;
  return w;
}

interface MatchedItem {
  indent: number;
  ordered: boolean;
  content: string;
  task?: boolean;
}

function matchListItem(line: string): MatchedItem | null {
  const ol = line.match(OL_RE);
  if (ol) {
    return { indent: indentWidth(ol[1]), ordered: true, content: ol[2] };
  }
  const ul = line.match(UL_RE);
  if (ul) {
    let content = ul[2];
    let task: boolean | undefined;
    const t = content.match(TASK_RE);
    if (t) {
      task = t[1].toLowerCase() === 'x';
      content = t[2];
    }
    return { indent: indentWidth(ul[1]), ordered: false, content, task };
  }
  return null;
}

// Parse a list whose items sit at indentation >= `minIndent`. The list's own
// type/level is fixed by the first item; more-indented items recurse into a
// nested child list attached to the preceding item. Returns the parsed block
// and the index of the first line that is no longer part of this list.
function parseList(
  lines: string[],
  start: number,
  minIndent: number,
): { block: Extract<Block, { kind: 'list' }>; next: number } {
  const first = matchListItem(lines[start])!;
  const baseIndent = first.indent;
  const ordered = first.ordered;
  const items: ListItem[] = [];
  let i = start;

  while (i < lines.length) {
    const m = matchListItem(lines[i]);
    if (!m) {
      // A blank line is allowed between items; peek past a single blank line to
      // see whether the list continues at this indent.
      if (lines[i].trim() === '' && i + 1 < lines.length && matchListItem(lines[i + 1])) {
        i++;
        continue;
      }
      break;
    }
    // Less-indented than this list's base, or a sibling list of the *other*
    // type at the same base indent → this list ends here.
    if (m.indent < baseIndent) break;
    if (m.indent === baseIndent && m.ordered !== ordered) break;

    if (m.indent > baseIndent) {
      // Nested: attach a child list to the last item we pushed.
      const child = parseList(lines, i, baseIndent + 1);
      if (items.length > 0) items[items.length - 1].children = child.block;
      i = child.next;
      continue;
    }

    // Sibling item at this list's level.
    const item: ListItem = { inlines: parseInline(m.content) };
    if (m.task !== undefined) item.task = m.task;
    items.push(item);
    i++;
  }

  // minIndent only matters for the top-level call to keep the signature stable;
  // nested calls pass baseIndent+1 so unrelated callers can ignore it.
  void minIndent;
  return { block: { kind: 'list', ordered, items }, next: i };
}

// ---------------------------------------------------------------------------
// Tables — GFM pipe tables with alignment.
// ---------------------------------------------------------------------------

// Split a table row on unescaped pipes, honoring `\|`. Leading/trailing empty
// cells from bounding pipes are dropped. Each cell is trimmed.
function splitTableRow(line: string): string[] {
  const cells: string[] = [];
  let cur = '';
  for (let i = 0; i < line.length; i++) {
    const ch = line[i];
    if (ch === '\\' && line[i + 1] === '|') {
      // Keep the literal pipe (without the backslash) inside the cell.
      cur += '|';
      i++;
      continue;
    }
    if (ch === '|') {
      cells.push(cur);
      cur = '';
      continue;
    }
    cur += ch;
  }
  cells.push(cur);
  // Drop the empty cells produced by a leading and/or trailing pipe.
  if (cells.length && cells[0].trim() === '') cells.shift();
  if (cells.length && cells[cells.length - 1].trim() === '') cells.pop();
  return cells.map((c) => c.trim());
}

// A line is a candidate table row if it contains at least one unescaped pipe.
function hasUnescapedPipe(line: string): boolean {
  for (let i = 0; i < line.length; i++) {
    if (line[i] === '\\') {
      i++;
      continue;
    }
    if (line[i] === '|') return true;
  }
  return false;
}

function isDelimiterRow(line: string): boolean {
  if (!hasUnescapedPipe(line)) return false;
  const cells = splitTableRow(line);
  if (cells.length === 0) return false;
  return cells.every((c) => TABLE_DELIM_CELL_RE.test(c));
}

// A table starts at `i` when line[i] is a header row and line[i+1] is a
// delimiter row whose cell count matches the header's.
function isTableStart(lines: string[], i: number): boolean {
  if (i + 1 >= lines.length) return false;
  const header = lines[i];
  const delim = lines[i + 1];
  if (!hasUnescapedPipe(header) || header.trim() === '') return false;
  if (isDelimiterRow(header)) return false; // header can't itself be a delimiter
  if (!isDelimiterRow(delim)) return false;
  return splitTableRow(header).length === splitTableRow(delim).length;
}

function parseAlign(cell: string): TableAlign {
  const left = cell.startsWith(':');
  const right = cell.endsWith(':');
  if (left && right) return 'center';
  if (right) return 'right';
  if (left) return 'left';
  return null;
}

function parseTable(
  lines: string[],
  start: number,
): { block: Extract<Block, { kind: 'table' }>; next: number } {
  const header = splitTableRow(lines[start]).map((c) => parseInline(c));
  const align = splitTableRow(lines[start + 1]).map(parseAlign);
  const rows: Inline[][][] = [];
  let i = start + 2;
  while (i < lines.length && lines[i].trim() !== '' && hasUnescapedPipe(lines[i]) && !isDelimiterRow(lines[i])) {
    const cells = splitTableRow(lines[i]).map((c) => parseInline(c));
    // Normalize ragged rows to the header width.
    while (cells.length < header.length) cells.push([{ kind: 'text', text: '' }]);
    if (cells.length > header.length) cells.length = header.length;
    rows.push(cells);
    i++;
  }
  return { block: { kind: 'table', align, header, rows }, next: i };
}

// ---------------------------------------------------------------------------
// Inline parser — handles `code`, **bold**, *italic*/_italic_, [text](href).
// Inline code is tokenized first (its contents are never re-parsed, so emphasis
// markers inside code spans stay literal). Emphasis is nested; unmatched markers
// fall through as literal text so malformed input never throws.
//
//   * `*` / `**`: matched greedily-from-the-left but balanced — we scan for the
//     nearest matching close marker, and `*` (single) won't swallow `**`.
//   * `_` / `__`: GFM intraword rule — underscore emphasis only opens/closes at
//     a word boundary, so `snake_case_words` is left untouched.
// ---------------------------------------------------------------------------

function isWordChar(ch: string | undefined): boolean {
  return ch !== undefined && /[A-Za-z0-9]/.test(ch);
}

export function parseInline(src: string): Inline[] {
  const out: Inline[] = [];
  let plain = '';
  const flush = () => {
    if (plain) {
      out.push({ kind: 'text', text: plain });
      plain = '';
    }
  };

  let i = 0;
  const n = src.length;
  while (i < n) {
    const c = src[i];

    // Inline code: `...` (and ``...`` for spans containing a backtick).
    if (c === '`') {
      // Count the run of opening backticks; the matching close is the same run.
      let ticks = 1;
      while (src[i + ticks] === '`') ticks++;
      const fence = '`'.repeat(ticks);
      const end = src.indexOf(fence, i + ticks);
      if (end > i) {
        flush();
        out.push({ kind: 'code', text: src.slice(i + ticks, end) });
        i = end + ticks;
        continue;
      }
    }

    // Link: [text](href)
    if (c === '[') {
      const close = src.indexOf(']', i + 1);
      if (close > i && src[close + 1] === '(') {
        const paren = src.indexOf(')', close + 2);
        if (paren > close) {
          flush();
          out.push({
            kind: 'link',
            text: src.slice(i + 1, close),
            href: src.slice(close + 2, paren),
          });
          i = paren + 1;
          continue;
        }
      }
    }

    // Strong: **...** or __...__
    if ((c === '*' || c === '_') && src[i + 1] === c) {
      const ok =
        c === '*' ||
        // Underscore strong needs a left word boundary (no word char before).
        !isWordChar(src[i - 1]);
      if (ok) {
        const end = findClose(src, i + 2, c, true);
        if (end > i + 1) {
          flush();
          out.push({ kind: 'strong', children: parseInline(src.slice(i + 2, end)) });
          i = end + 2;
          continue;
        }
      }
    }

    // Emphasis: *...* or _..._
    if (c === '*' || c === '_') {
      const ok =
        c === '*' ||
        // Underscore emphasis needs a left word boundary — this is what keeps
        // `snake_case` from italicizing its middle segment.
        !isWordChar(src[i - 1]);
      if (ok) {
        const end = findClose(src, i + 1, c, false);
        if (end > i) {
          flush();
          out.push({ kind: 'em', children: parseInline(src.slice(i + 1, end)) });
          i = end + 1;
          continue;
        }
      }
    }

    plain += c;
    i++;
  }
  flush();
  return out.length ? out : [{ kind: 'text', text: src }];
}

// Find the matching close marker for an emphasis run that opened at `from`
// (the index just past the opening marker). For `*`/`**` we skip over inline
// code spans and avoid matching a `*` against a `**`. For `_`/`__` we require a
// right word boundary at the close. Returns -1 (well, <from) when no valid
// close exists, so the caller falls back to literal text.
function findClose(src: string, from: number, marker: string, strong: boolean): number {
  const n = src.length;
  let i = from;
  while (i < n) {
    const ch = src[i];
    // Don't match markers inside an inline code span.
    if (ch === '`') {
      let ticks = 1;
      while (src[i + ticks] === '`') ticks++;
      const fence = '`'.repeat(ticks);
      const end = src.indexOf(fence, i + ticks);
      if (end > i) {
        i = end + ticks;
        continue;
      }
    }
    if (ch === marker) {
      const doubled = src[i + 1] === marker;
      if (strong) {
        // Need a doubled close marker for strong.
        if (doubled) {
          if (marker === '_' && isWordChar(src[i + 2])) {
            // Right boundary fails — keep scanning.
            i += 2;
            continue;
          }
          return i;
        }
        i++;
        continue;
      } else {
        // Single-marker emphasis: a doubled marker is a strong delimiter, not
        // our close — skip it.
        if (doubled) {
          i += 2;
          continue;
        }
        if (marker === '_' && isWordChar(src[i + 1])) {
          // Right boundary fails (intraword `_`) — keep scanning.
          i++;
          continue;
        }
        return i;
      }
    }
    i++;
  }
  return from - 2; // signal "no close" (always < the opener position)
}
