import { describe, expect, it } from 'vitest';
import { userFacingNotes } from './releaseNotes';

const RAW = 'Changes since v0.0.1199:\n\n- Record the entitlements publish as a non-starting boot step';

describe('userFacingNotes', () => {
  it('prefers curated notes for a known version', () => {
    const notes = userFacingNotes('0.0.1201', RAW, false);
    expect(notes.length).toBeGreaterThan(1);
    expect(notes.map((n) => n.text).join(' ')).not.toMatch(/entitlements publish/);
  });

  it('never shows commit subjects to a public install', () => {
    expect(userFacingNotes('0.0.1200', RAW, false)).toEqual([{ text: 'Improvements and fixes.', kind: 'other' }]);
  });

  it('keeps the raw list for team installs', () => {
    expect(userFacingNotes('0.0.1200', RAW, true)[0].text).toBe('Record the entitlements publish as a non-starting boot step');
  });
});

// Public notes ship in the app, update.json and the GitHub release, so team-only
// features must never be named there. publish.sh runs the same check.
