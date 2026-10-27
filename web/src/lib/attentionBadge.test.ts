import { describe, expect, it } from 'vitest';
import { attentionTitle } from './attentionBadge';

describe('attentionTitle', () => {
  it('prefixes the waiting count and strips a previous prefix', () => {
    expect(attentionTitle('soluterminal', 2)).toBe('(2) soluterminal');
    expect(attentionTitle('(2) soluterminal', 3)).toBe('(3) soluterminal');
    expect(attentionTitle('(99+) soluterminal', 0)).toBe('soluterminal');
    expect(attentionTitle('soluterminal', 120)).toBe('(99+) soluterminal');
  });
});
