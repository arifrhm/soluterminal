import { describe, expect, it } from 'vitest';
import { isAbsoluteRepoPath } from './repoPath';

describe('isAbsoluteRepoPath', () => {
  it('accepts what either daemon platform calls absolute', () => {
    expect(isAbsoluteRepoPath('/home/u/projects/thing')).toBe(true);
    expect(isAbsoluteRepoPath('C:\\Users\\me\\thing')).toBe(true);
    expect(isAbsoluteRepoPath('c:/Users/me/thing')).toBe(true);
    expect(isAbsoluteRepoPath('\\\\server\\share')).toBe(true);
  });

  it('refuses blank, whitespace and relative paths', () => {
    expect(isAbsoluteRepoPath('')).toBe(false);
    expect(isAbsoluteRepoPath('   ')).toBe(false);
    expect(isAbsoluteRepoPath('projects/thing')).toBe(false);
    expect(isAbsoluteRepoPath('./thing')).toBe(false);
    expect(isAbsoluteRepoPath('~/thing')).toBe(false);
  });
});
