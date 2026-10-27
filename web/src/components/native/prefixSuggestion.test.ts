import { describe, expect, it } from 'vitest';
import { bashPrefixSuggestion } from './NativeEventViews';

// The suggestion only decides which scoped rule we OFFER — the daemon's
// matching is sound regardless — so these lock in "sensible, never absurd".
describe('bashPrefixSuggestion', () => {
  it('suggests cmd+subcommand for known multi-token tools', () => {
    expect(bashPrefixSuggestion({ command: 'git status --short' })).toBe('git status');
    expect(bashPrefixSuggestion({ command: 'npm run build' })).toBe('npm run');
    expect(bashPrefixSuggestion({ command: 'kubectl get pods' })).toBe('kubectl get');
  });

  it('suggests the first token otherwise', () => {
    expect(bashPrefixSuggestion({ command: 'ls -la src' })).toBe('ls');
    expect(bashPrefixSuggestion({ command: 'gofmt -l .' })).toBe('gofmt');
  });

  it('skips flag-like or option second tokens on multi-token tools', () => {
    expect(bashPrefixSuggestion({ command: 'git --version' })).toBe('git');
  });

  it('returns null for non-word commands and missing input', () => {
    expect(bashPrefixSuggestion({ command: './run.sh' })).toBeNull();
    expect(bashPrefixSuggestion({ command: '$CMD --x' })).toBeNull();
    expect(bashPrefixSuggestion({ command: '' })).toBeNull();
    expect(bashPrefixSuggestion({})).toBeNull();
    expect(bashPrefixSuggestion(null)).toBeNull();
    expect(bashPrefixSuggestion('not an object')).toBeNull();
  });
});
