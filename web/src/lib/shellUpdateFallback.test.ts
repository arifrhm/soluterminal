import { describe, expect, it } from 'vitest';
import { DESKTOP_DOWNLOAD_URL, describeShellUpdateFailure } from './shellUpdateFallback';

describe('describeShellUpdateFailure', () => {
  it('keeps only the plain reason from a native updater error', () => {
    expect(describeShellUpdateFailure(
      'Error: installer update failed (exit 10): could not read the downloaded installer: in use',
    )).toBe('could not read the downloaded installer: in use');
  });

  it('uses the first line and caps long messages', () => {
    const long = `${'x'.repeat(200)}\nsecond line`;
    const reason = describeShellUpdateFailure(long);
    expect(reason).toHaveLength(158);
    expect(reason.endsWith('…')).toBe(true);
    expect(reason).not.toContain('second line');
  });

  it('returns nothing without an error', () => {
    expect(describeShellUpdateFailure(undefined)).toBe('');
  });

  it('points at the neutral public download page', () => {
    expect(DESKTOP_DOWNLOAD_URL).toBe('https://soluterminal.com/download/');
    expect(DESKTOP_DOWNLOAD_URL).not.toContain('github');
  });
});
