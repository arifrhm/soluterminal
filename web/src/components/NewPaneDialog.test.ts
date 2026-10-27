import { describe, expect, it } from 'vitest';
import { parseCommandLine } from './NewPaneDialog';

describe('parseCommandLine', () => {
  it('keeps quoted arguments and escaped spaces together', () => {
    expect(parseCommandLine('claude --resume "work tree" path\\ with\\ spaces')).toEqual([
      'claude',
      '--resume',
      'work tree',
      'path with spaces',
    ]);
  });

  it('handles single quotes, double quotes, and empty quoted arguments', () => {
    expect(parseCommandLine(`cmd 'single quoted' "double quoted" ""`)).toEqual([
      'cmd',
      'single quoted',
      'double quoted',
      '',
    ]);
  });

  it('reports unclosed quoted arguments', () => {
    expect(() => parseCommandLine('claude "unfinished')).toThrow(/quoted command argument/);
  });
});
