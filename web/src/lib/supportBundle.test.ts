import { describe, expect, it } from 'vitest';
import {
  bundleSelectionBytes,
  defaultSelection,
  formatBundleBytes,
  renderSupportBundle,
  type SupportBundleResponse,
} from './supportBundle';

const fixture: SupportBundleResponse = {
  schema: 1,
  generatedAt: '2026-09-06T08:09:10Z',
  filename: 'soluterminal-support-2026-09-06-080910.txt',
  sections: [
    { id: 'versions', title: 'Versions', description: 'Versions', text: 'alpha\nline', bytes: 10, lines: 2, selectedByDefault: true, empty: false, truncated: false },
    { id: 'logs', title: 'Logs', description: 'Logs', text: 'exact log bytes', bytes: 15, lines: 1, selectedByDefault: true, empty: false, truncated: false },
    { id: 'paneText', title: 'Pane tail', description: 'Pane', text: 'private opt-in tail', bytes: 19, lines: 1, selectedByDefault: false, empty: false, truncated: false },
  ],
  omitted: ['Cookies and tokens', 'Screenshots and images'],
  logPaths: ['~/.local/share/soluterminal/solud.log'],
};

describe('support bundle rendering', () => {
  it('renders selected sections in server order and preserves their text bytes', () => {
    const selected = new Set(['logs', 'versions']);
    const rendered = renderSupportBundle(fixture, selected);
    expect(rendered.indexOf('===== Versions =====')).toBeLessThan(rendered.indexOf('===== Logs ====='));
    expect(rendered).toContain(`===== Versions =====\n${fixture.sections[0].text}\n\n`);
    expect(rendered).toContain(`===== Logs =====\n${fixture.sections[1].text}\n\n`);
    expect(rendered).not.toContain('private opt-in tail');
  });

  it('lists omitted content and deselected ids in the header', () => {
    const rendered = renderSupportBundle(fixture, new Set(['versions']));
    expect(rendered).toContain('# - Cookies and tokens');
    expect(rendered).toContain('# - Screenshots and images');
    expect(rendered).toContain('# Excluded section ids: logs, paneText');
  });

  it('produces a valid header-only file when every section is deselected', () => {
    const rendered = renderSupportBundle(fixture, new Set());
    expect(rendered).toContain('# soluterminal support bundle');
    expect(rendered).toContain('# Included section ids: (none)');
    expect(rendered).not.toContain('=====');
  });

  it('derives defaults and selected size from server metadata', () => {
    expect([...defaultSelection(fixture)]).toEqual(['versions', 'logs']);
    expect(bundleSelectionBytes(fixture, new Set(['versions', 'paneText']))).toBe(29);
    expect(formatBundleBytes(12 * 1024)).toBe('12 KB');
    expect(formatBundleBytes(bundleSelectionBytes(fixture, new Set(['logs'])))).toBe('15 B');
  });
});
