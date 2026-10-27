import { describe, expect, it } from 'vitest';
import { planSelectionSend, sanitizeSelection } from './selectionSend';

describe('send selection safety', () => {
  it('strips control characters that could break out of a bracketed paste', () => {
    expect(sanitizeSelection('echo hi\x1b[201~\rrm -rf x\x07')).toBe('echo hi[201~\nrm -rf x');
  });

  it('pastes a single line without submitting it', () => {
    expect(planSelectionSend('npm test')).toEqual({ action: 'paste', text: 'npm test' });
    expect(planSelectionSend('npm test\n')).toEqual({ action: 'paste', text: 'npm test' });
  });

  it('never types multi-line text into a pane (each line could run)', () => {
    expect(planSelectionSend('ls\nrm -rf build')).toMatchObject({ action: 'refuse', reason: 'multiline-unsafe' });
    expect(planSelectionSend('ls\r\nrm -rf build')).toMatchObject({ action: 'refuse', reason: 'multiline-unsafe' });
    expect(planSelectionSend('a\u0085b')).toEqual({ action: 'paste', text: 'ab' }); // NEL stripped, not a line break
  });

  it('refuses empty selections', () => {
    expect(planSelectionSend('  \n\x1b ')).toMatchObject({ action: 'refuse', reason: 'empty' });
  });
});
