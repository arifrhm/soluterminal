import { describe, it, expect } from 'vitest';
import { fileSearchMatches } from './fileSearch';

const lines = ['package main', '', 'func main() {', '  return ERR', '}', 'ERROR here'];

describe('fileSearchMatches', () => {
  it('returns no matches for an empty / whitespace query', () => {
    expect(fileSearchMatches(lines, '')).toEqual([]);
    expect(fileSearchMatches(lines, '   ')).toEqual([]);
  });

  it('matches case-insensitively across lines', () => {
    expect(fileSearchMatches(lines, 'err')).toEqual([3, 5]);
    expect(fileSearchMatches(lines, 'MAIN')).toEqual([0, 2]);
  });

  it('returns [] when nothing contains the query', () => {
    expect(fileSearchMatches(lines, 'zzz')).toEqual([]);
  });

  it(':N jumps to a 1-based line, clamped to range', () => {
    expect(fileSearchMatches(lines, ':1')).toEqual([0]);
    expect(fileSearchMatches(lines, ':3')).toEqual([2]);
    expect(fileSearchMatches(lines, ':999')).toEqual([5]); // clamp to last
    expect(fileSearchMatches(lines, ':0')).toEqual([0]);   // clamp to first
  });

  it(':N on an empty file yields no jump', () => {
    expect(fileSearchMatches([], ':5')).toEqual([]);
  });
});
