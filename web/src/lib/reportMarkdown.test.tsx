import { render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { renderReportMarkdown } from './reportMarkdown';

function html(md: string) {
  return render(<div>{renderReportMarkdown(md)}</div>).container;
}

describe('renderReportMarkdown', () => {
  it('renders bold without literal asterisks', () => {
    const c = html('**Needs your decision today:** merge PR #111');
    expect(c.querySelector('strong')?.textContent).toBe('Needs your decision today:');
    expect(c.textContent).not.toContain('**');
  });

  it('renders headings without # marks', () => {
    const c = html('# Digest\n## CI status');
    expect(c.querySelector('.report-h1')?.textContent).toBe('Digest');
    expect(c.querySelector('.report-h2')?.textContent).toBe('CI status');
    expect(c.textContent).not.toContain('#');
  });

  it('renders bullet and numbered lines with markers', () => {
    const c = html('- first thing\n1. second thing');
    const items = c.querySelectorAll('.report-li');
    expect(items).toHaveLength(2);
    expect(items[0].textContent).toContain('first thing');
    expect(items[1].textContent).toContain('2.'.replace('2', '1'));
  });

  it('keeps markdown literal inside code spans and fences', () => {
    const c = html('run `npm test -- --run **all**`\n```\n**not bold** # not a heading\n```');
    expect(c.querySelector('code')?.textContent).toContain('**all**');
    expect(c.querySelector('.report-code-block')?.textContent).toBe('**not bold** # not a heading');
    expect(c.querySelector('.report-code-block strong')).toBeNull();
  });

  it('renders links as safe anchors', () => {
    const c = html('see [PR #8](https://github.com/x/y/pull/8) for details');
    const a = c.querySelector('a');
    expect(a?.getAttribute('href')).toBe('https://github.com/x/y/pull/8');
    expect(a?.getAttribute('rel')).toBe('noreferrer');
    expect(a?.textContent).toBe('PR #8');
  });

  it('never emits raw HTML from the report', () => {
    const c = html('<img src=x onerror=alert(1)> **hi**');
    expect(c.querySelector('img')).toBeNull();
    expect(c.textContent).toContain('<img src=x onerror=alert(1)>');
  });

  it('passes unknown syntax through as plain text', () => {
    const c = html('_underscore_ | not | a table |');
    expect(c.textContent).toContain('_underscore_');
    expect(c.textContent).toContain('| not | a table |');
  });

  it('renders tables with header and body', () => {
    const c = html('| PR | State |\n|----|-------|\n| #111 | **ready** |\n| #110 | draft |');
    const table = c.querySelector('.report-table');
    expect(table?.querySelectorAll('th')).toHaveLength(2);
    expect(table?.querySelectorAll('tbody tr')).toHaveLength(2);
    expect(table?.querySelector('tbody strong')?.textContent).toBe('ready');
    expect(c.textContent).not.toContain('----');
  });

  it('renders italics only when stars hug the text', () => {
    const c = html('this is *important* but 2 * 3 * 4 stays math');
    expect(c.querySelector('em')?.textContent).toBe('important');
    expect(c.textContent).toContain('2 * 3 * 4');
  });

  it('renders strikethrough, quotes, and rules', () => {
    const c = html('~~dropped~~\n> a quoted note\n> second line\n\n---');
    expect(c.querySelector('del')?.textContent).toBe('dropped');
    const q = c.querySelector('.report-quote');
    expect(q?.textContent).toContain('a quoted note');
    expect(q?.textContent).toContain('second line');
    expect(c.querySelector('.report-hr')).not.toBeNull();
    expect(c.textContent).not.toContain('---');
  });

  it('promotes standalone all-bold lines to headings', () => {
    const c = html('**Needs your decision today:**\n1. **PR #111** — ready to merge.');
    const h = c.querySelector('.report-h3');
    expect(h?.textContent).toBe('Needs your decision today');
    // Inline bold inside content lines stays inline — only STANDALONE
    // bold lines get promoted.
    expect(c.querySelector('.report-li strong')?.textContent).toBe('PR #111');
  });

  it('renders checklists with state glyphs', () => {
    const c = html('- [ ] merge PR #111\n- [x] close PR #110');
    const markers = c.querySelectorAll('.report-li-marker');
    expect(markers[0].textContent).toBe('☐');
    expect(markers[1].textContent).toBe('☑');
    expect(c.textContent).not.toContain('[x]');
  });
});
