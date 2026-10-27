import { describe, it, expect } from 'vitest';
import { parseNotes, firstHighlight } from './updateNotes';

// Sample matching publish.sh's baked-in `notes` format: a header line, a blank
// line, then one `- <type>(<scope>): <subject> (vX.Y.Z)` bullet per commit.
const SAMPLE = `Changes since v0.0.712:

- feat(web): move phone-access indicator to footer as a minimal "Cloud" status
- fix(native): stream native panes over WebSocket (v0.0.713)
- chore: bump deps
- refactor(daemon)!: rename pane registry`;

describe('parseNotes', () => {
  it('skips the header line', () => {
    const out = parseNotes(SAMPLE);
    expect(out.find((n) => /changes since/i.test(n.text))).toBeUndefined();
  });

  it('strips bullet, conventional-commit prefix, and trailing version, then sentence-cases', () => {
    const out = parseNotes(SAMPLE);
    expect(out[0].text).toBe('Move phone-access indicator to footer as a minimal "Cloud" status');
    expect(out[1].text).toBe('Stream native panes over WebSocket');
    expect(out[2].text).toBe('Bump deps');
    expect(out[3].text).toBe('Rename pane registry');
  });

  it('captures the commit kind (feat / fix / other)', () => {
    const out = parseNotes(SAMPLE);
    expect(out[0].kind).toBe('feat');
    expect(out[1].kind).toBe('fix');
    expect(out[2].kind).toBe('other'); // chore
    expect(out[3].kind).toBe('other'); // refactor
  });

  it('returns [] for empty / nullish input', () => {
    expect(parseNotes('')).toEqual([]);
    expect(parseNotes(undefined)).toEqual([]);
    expect(parseNotes(null)).toEqual([]);
  });

  it('handles plain lines with no conventional-commit prefix', () => {
    const out = parseNotes('- just a plain note');
    expect(out).toEqual([{ text: 'Just a plain note', kind: 'other' }]);
  });
});

describe('firstHighlight', () => {
  it('returns the first cleaned highlight, not the header line', () => {
    expect(firstHighlight(SAMPLE)).toBe(
      'Move phone-access indicator to footer as a minimal "Cloud" status',
    );
  });
  it('returns empty string when there is nothing', () => {
    expect(firstHighlight('Recent changes:')).toBe('');
    expect(firstHighlight(undefined)).toBe('');
  });
});
