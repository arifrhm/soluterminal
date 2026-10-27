// Markdown.tsx — renders the Block[] / Inline[] produced by lib/markdown.ts as
// React. Fenced code blocks reuse lib/highlight.ts's per-line tokenizer (the
// same one FileTile uses) so code looks consistent across the app. Pure render;
// no state.

import { Fragment, memo, useEffect, useMemo, useRef, useState } from 'react';
import { copyToClipboard } from '../../lib/clipboard';
import {
  parseMarkdown,
  flattenHeadingsForFocus,
  type Block,
  type Inline,
  type ListItem,
} from '../../lib/markdown';
import { tokenizeLine } from '../../lib/highlight';

function alignStyle(align: 'left' | 'center' | 'right' | null): React.CSSProperties | undefined {
  return align ? { textAlign: align } : undefined;
}

function renderInlines(inlines: Inline[]): React.ReactNode {
  return inlines.map((node, idx) => {
    switch (node.kind) {
      case 'text':
        return <Fragment key={idx}>{node.text}</Fragment>;
      case 'code':
        return (
          <code key={idx} className="md-inline-code">
            {node.text}
          </code>
        );
      case 'strong':
        return <strong key={idx}>{renderInlines(node.children)}</strong>;
      case 'em':
        return <em key={idx}>{renderInlines(node.children)}</em>;
      case 'link':
        return (
          <a key={idx} href={node.href} target="_blank" rel="noreferrer noopener">
            {node.text || node.href}
          </a>
        );
      default:
        return null;
    }
  });
}

// memo so a re-render of the surrounding transcript that passes a
// reference-equal `block` (e.g. an unrelated sibling row settling) skips the
// per-line syntax-highlight tokenization. During streaming the markdown parser
// allocates a fresh `block` each parse, so this bails only across renders that
// reuse the same Block object — but the heavy O(n·lines) re-highlight on every
// settled code block is the case that matters, and that one is covered.
const CodeBlock = memo(function CodeBlock({ block }: { block: Extract<Block, { kind: 'code' }> }) {
  const lines = block.code.split('\n');
  const [copied, setCopied] = useState(false);
  const [copyFailed, setCopyFailed] = useState(false);
  const copy = () => {
    void copyToClipboard(block.code).then((ok) => {
      setCopied(ok);
      setCopyFailed(!ok);
      if (ok) window.setTimeout(() => setCopied(false), 1200);
    });
  };
  return (
    <pre className="md-code-block">
      {block.langLabel && <span className="md-code-lang">{block.langLabel}</span>}
      <button type="button" className="md-code-copy" title={copyFailed ? 'Copy failed — select the code and copy manually' : 'Copy'} aria-label={copyFailed ? 'Copy failed — retry copying code' : copied ? 'Code copied' : 'Copy code'} onClick={copy}>
        {copyFailed ? 'Copy failed' : copied ? '✓' : '⧉'}
      </button>
      <code>
        {lines.map((line, i) => (
          <span className="md-code-line" key={i}>
            {tokenizeLine(line, block.lang).map((t, j) =>
              t.cls ? (
                <span className={`tok-${t.cls}`} key={j}>
                  {t.text}
                </span>
              ) : (
                <Fragment key={j}>{t.text}</Fragment>
              ),
            )}
            {i < lines.length - 1 ? '\n' : ''}
          </span>
        ))}
      </code>
    </pre>
  );
});

function renderList(
  block: Extract<Block, { kind: 'list' }>,
  key: React.Key,
): React.ReactNode {
  const Tag = block.ordered ? 'ol' : 'ul';
  return (
    <Tag key={key} className="md-list">
      {block.items.map((item, i) => renderListItem(item, i))}
    </Tag>
  );
}

function renderListItem(item: ListItem, key: React.Key): React.ReactNode {
  const isTask = item.task !== undefined;
  return (
    <li key={key} className={isTask ? 'md-task-item' : undefined}>
      {isTask && (
        <input
          type="checkbox"
          className="md-task-checkbox"
          checked={item.task}
          disabled
          readOnly
        />
      )}
      {renderInlines(item.inlines)}
      {item.children && renderList(item.children, 'children')}
    </li>
  );
}

function renderBlock(block: Block, idx: number): React.ReactNode {
  switch (block.kind) {
    case 'heading': {
      const Tag = (`h${Math.min(block.level, 6)}` as keyof JSX.IntrinsicElements);
      return (
        <Tag key={idx} className="md-heading">
          {renderInlines(block.inlines)}
        </Tag>
      );
    }
    case 'paragraph':
      return (
        <p key={idx} className={block.runIn ? 'md-p md-runin' : 'md-p'}>
          {renderInlines(block.inlines)}
        </p>
      );
    case 'code':
      return <CodeBlock key={idx} block={block} />;
    case 'list':
      return renderList(block, idx);
    case 'table':
      return (
        <div key={idx} className="md-table-wrap">
          <table className="md-table">
            <thead>
              <tr>
                {block.header.map((cell, c) => (
                  <th key={c} style={alignStyle(block.align[c])}>
                    {renderInlines(cell)}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {block.rows.map((row, r) => (
                <tr key={r}>
                  {row.map((cell, c) => (
                    <td key={c} style={alignStyle(block.align[c])}>
                      {renderInlines(cell)}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      );
    case 'quote':
      return (
        <blockquote key={idx} className="md-quote">
          {renderInlines(block.inlines)}
        </blockquote>
      );
    case 'hr':
      return <hr key={idx} className="md-hr" />;
    default:
      return null;
  }
}

export function Markdown({
  source,
  streaming,
}: {
  source: string;
  streaming?: boolean;
}) {
  // Headings are always demoted to bold run-in lead-ins (no essay/document feel);
  // flattenHeadingsForFocus runs unconditionally in each branch.
  // Branch BEFORE any parse-bearing hook so the synchronous parse never runs on
  // the streaming path (a hook called unconditionally would re-parse every token
  // and defeat the rAF coalescing). The two branches are distinct component types,
  // so settle (streaming → false) unmounts StreamingMarkdown and mounts StaticMarkdown
  // with an exact, synchronous parse of the final text.
  return streaming ? (
    <StreamingMarkdown source={source} />
  ) : (
    <StaticMarkdown source={source} />
  );
}

// Settled (non-streaming) text: parse synchronously + memoized on [source]. The
// parent row view is React.memo'd, so a settled block never re-renders on later
// tokens — this parses once per block and stays cheap. Focus shows the answer in
// FULL (no prose fold): folding by position kept hiding substance — the conclusion
// in v0.0.600, the argument in v0.0.608 — because the model writes lede→substance→CTA
// with no fixed content map, so no fold position is safe. Compactness comes from terse
// generation + tool/thinking grouping instead, never from truncating answer prose.
function StaticMarkdown({ source }: { source: string }) {
  const parsed = useMemo(() => parseMarkdown(source), [source]);
  const blocks = useMemo(() => flattenHeadingsForFocus(parsed), [parsed]);
  return <div className="md">{blocks.map((b, i) => renderBlock(b, i))}</div>;
}

// While a block streams, the parent row re-renders once PER TOKEN and `source`
// grows each time, so a synchronous parse-per-render is O(n²) over the message
// (full re-parse + per-line re-highlight of the whole accumulated text every
// token). Coalesce instead: stash the latest `source` and re-parse at most ONCE
// per animation frame. The rendered markdown lags the raw text by ≤1 frame
// during streaming (imperceptible); when the block settles the parent swaps to
// the non-streaming branch above and parses the exact final text synchronously.
// The parser tolerates a half-open fence mid-stream (unknown constructs fall
// through as plain text), so a frame-boundary snapshot is always renderable.
function StreamingMarkdown({ source }: { source: string }) {
  const [blocks, setBlocks] = useState<Block[]>(() => parseMarkdown(source));
  const latest = useRef(source);
  const rafRef = useRef<number | null>(null);

  // Re-parse at most once per frame: each delta updates `latest` and, only if no
  // frame is already pending, schedules one. A pending frame is never cancelled
  // by a newer delta (it just picks up the newest `latest`), so a fast burst of
  // tokens still parses within the next frame instead of starving.
  useEffect(() => {
    latest.current = source;
    if (rafRef.current !== null) return; // a parse is already pending this frame
    rafRef.current = requestAnimationFrame(() => {
      rafRef.current = null;
      setBlocks(parseMarkdown(latest.current));
    });
  }, [source]);

  // Cancel any pending frame on unmount (run-once cleanup, empty deps).
  useEffect(() => {
    return () => {
      if (rafRef.current !== null) cancelAnimationFrame(rafRef.current);
    };
  }, []);

  // Run-in transform is a cheap linear pass over a handful of blocks; fine per
  // frame (the heavy work is the parse above, already rAF-coalesced).
  const display = flattenHeadingsForFocus(blocks);
  return <div className="md">{display.map((b, i) => renderBlock(b, i))}</div>;
}
