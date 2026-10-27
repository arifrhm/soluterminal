import type { ReactNode } from 'react';

// Markdown renderer for scheduled-task reports. Claude's headless output is
// markdown, and showing literal **stars** and # marks failed the glance test
// the reports reader exists for. Covers what Claude actually emits in
// reports — headings, bold/italic/strikethrough, inline + fenced code,
// bullet/numbered/check lists, tables, blockquotes, rules, links — and NOT
// a general markdown engine. Unknown syntax passes through as plain text,
// which is the safe failure mode for a report. No raw HTML ever.

const INLINE_RE = /(`[^`\n]+`|\*\*[^*\n]+\*\*|~~[^~\n]+~~|\*[^*\n]+\*|\[[^\]\n]+\]\(https?:\/\/[^\s)]+\))/g;
const LINK_RE = /^\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)$/;

function renderInline(text: string, keyBase: string): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  let n = 0;
  for (const m of text.matchAll(INLINE_RE)) {
    const at = m.index ?? 0;
    const tok = m[0];
    const k = `${keyBase}-${n++}`;
    let node: ReactNode;
    if (tok.startsWith('`')) {
      node = <code key={k} className="report-code-inline">{tok.slice(1, -1)}</code>;
    } else if (tok.startsWith('**')) {
      node = <strong key={k}>{tok.slice(2, -2)}</strong>;
    } else if (tok.startsWith('~~')) {
      node = <del key={k}>{tok.slice(2, -2)}</del>;
    } else if (tok.startsWith('[')) {
      const link = tok.match(LINK_RE);
      node = link
        ? <a key={k} href={link[2]} target="_blank" rel="noreferrer">{link[1]}</a>
        : tok;
    } else {
      // Single-star italics only when the stars hug the text — "2 * 3 * 4"
      // must stay literal. Guarded in code, not regex lookbehind: lookbehind
      // in a regex LITERAL throws at parse time on older iOS Safari, which
      // would kill the whole bundle, not just this feature.
      const inner = tok.slice(1, -1);
      node = inner && inner.trim() === inner ? <em key={k}>{inner}</em> : tok;
    }
    if (at > last) out.push(text.slice(last, at));
    out.push(node);
    last = at + tok.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

// A table needs a header row and a |---|-style separator directly under it.
const TABLE_SEP_RE = /^\s*\|?[\s:|-]*-[\s:|-]*\|?\s*$/;

function splitRow(line: string): string[] {
  let s = line.trim();
  if (s.startsWith('|')) s = s.slice(1);
  if (s.endsWith('|')) s = s.slice(0, -1);
  return s.split('|').map((c) => c.trim());
}

export function renderReportMarkdown(text: string): ReactNode[] {
  const out: ReactNode[] = [];
  const lines = text.split('\n');
  let i = 0;
  let key = 0;
  while (i < lines.length) {
    const line = lines[i];

    if (line.startsWith('```')) {
      const buf: string[] = [];
      i++;
      while (i < lines.length && !lines[i].startsWith('```')) {
        buf.push(lines[i]);
        i++;
      }
      i++; // closing fence (or EOF)
      out.push(<pre key={key++} className="report-code-block">{buf.join('\n')}</pre>);
      continue;
    }

    // Table: header row + separator + body rows.
    if (line.trim().startsWith('|') && i + 1 < lines.length && TABLE_SEP_RE.test(lines[i + 1])) {
      const header = splitRow(line);
      const rows: string[][] = [];
      i += 2;
      while (i < lines.length && lines[i].trim().startsWith('|')) {
        rows.push(splitRow(lines[i]));
        i++;
      }
      out.push(
        <table key={key++} className="report-table">
          <thead>
            <tr>{header.map((h, c) => <th key={c}>{renderInline(h, `th${key}-${c}`)}</th>)}</tr>
          </thead>
          <tbody>
            {rows.map((r, ri) => (
              <tr key={ri}>{r.map((cell, c) => <td key={c}>{renderInline(cell, `td${key}-${ri}-${c}`)}</td>)}</tr>
            ))}
          </tbody>
        </table>,
      );
      continue;
    }

    // Blockquote: group consecutive `>` lines.
    if (/^\s*>/.test(line)) {
      const buf: string[] = [];
      while (i < lines.length && /^\s*>/.test(lines[i])) {
        buf.push(lines[i].replace(/^\s*> ?/, ''));
        i++;
      }
      out.push(
        <blockquote key={key++} className="report-quote">
          {buf.map((q, qi) => <div key={qi}>{q ? renderInline(q, `q${key}-${qi}`) : ' '}</div>)}
        </blockquote>,
      );
      continue;
    }

    // Horizontal rule: --- / *** / ___ alone on a line.
    if (/^\s*([-*_])\s*(?:\1\s*){2,}$/.test(line)) {
      out.push(<div key={key++} className="report-hr" />);
      i++;
      continue;
    }

    const h = line.match(/^(#{1,4})\s+(.*)$/);
    if (h) {
      const level = h[1].length;
      out.push(
        <div key={key++} className={`report-h report-h${level}`}>{renderInline(h[2], `h${key}`)}</div>,
      );
      i++;
      continue;
    }

    // A standalone all-bold line ("**Needs your decision:**") is a heading
    // in author's clothing — Claude writes these constantly instead of ##.
    // Promote it so reports get hierarchy even when the model didn't
    // cooperate (and so pre-contract schedules read well retroactively).
    const boldLine = line.match(/^\*\*([^*]+)\*\*:?\s*$/);
    if (boldLine) {
      out.push(
        <div key={key++} className="report-h report-h3">{boldLine[1].replace(/:$/, '')}</div>,
      );
      i++;
      continue;
    }

    const bullet = line.match(/^(\s*)[-*]\s+(.*)$/);
    if (bullet) {
      // Checklist items get a state glyph instead of the dot.
      const check = bullet[2].match(/^\[([ xX])\]\s+(.*)$/);
      const marker = check ? (check[1] === ' ' ? '☐' : '☑') : '•';
      const content = check ? check[2] : bullet[2];
      out.push(
        <div key={key++} className="report-li" style={{ marginLeft: bullet[1].length ? 14 : 0 }}>
          <span className={`report-li-marker ${check && check[1] !== ' ' ? 'report-li-done' : ''}`}>{marker}</span>
          <span>{renderInline(content, `b${key}`)}</span>
        </div>,
      );
      i++;
      continue;
    }

    const numbered = line.match(/^(\s*)(\d+)\.\s+(.*)$/);
    if (numbered) {
      out.push(
        <div key={key++} className="report-li" style={{ marginLeft: numbered[1].length ? 14 : 0 }}>
          <span className="report-li-marker">{numbered[2]}.</span>
          <span>{renderInline(numbered[3], `n${key}`)}</span>
        </div>,
      );
      i++;
      continue;
    }

    if (line.trim() === '') {
      out.push(<div key={key++} className="report-gap" />);
      i++;
      continue;
    }

    out.push(<div key={key++} className="report-p">{renderInline(line, `p${key}`)}</div>);
    i++;
  }
  return out;
}
