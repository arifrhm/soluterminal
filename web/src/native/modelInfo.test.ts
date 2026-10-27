import { describe, expect, it } from 'vitest';
import {
  CONTEXT_LIMIT_1M,
  CONTEXT_LIMIT_DEFAULT,
  contextLimitForModel,
  prettyModelName,
  modelIdForAgent,
  MODEL_OPTIONS,
  sameModelId,
} from './modelInfo';

describe('modelIdForAgent', () => {
  it('never lets a Claude model label into a Codex footer', () => {
    expect(modelIdForAgent('claude-opus-4-8', 'codex')).toBeUndefined();
    expect(modelIdForAgent('Opus 4.8', 'codex')).toBeUndefined();
    expect(modelIdForAgent('gpt-5.3-codex', 'codex')).toBe('gpt-5.3-codex');
  });

  it('rejects clearly OpenAI models for Claude but permits provider aliases', () => {
    expect(modelIdForAgent('gpt-5.3-codex', 'claude')).toBeUndefined();
    expect(modelIdForAgent('claude-sonnet-5', 'claude')).toBe('claude-sonnet-5');
    expect(modelIdForAgent('glm-5.2', 'claude')).toBe('glm-5.2');
  });
});

describe('contextLimitForModel', () => {
  it('treats Opus 4.8 as 1M even with NO [1m] marker (the Focus ctx% bug)', () => {
    // The exact id session_init reports for Benjamin's panes. Before the fix this
    // returned 200k → a ~166k-token session read 83% used instead of the true ~17%.
    expect(contextLimitForModel('claude-opus-4-8')).toBe(CONTEXT_LIMIT_1M);
    expect(contextLimitForModel('claude-opus-4-8', 166_000)).toBe(CONTEXT_LIMIT_1M);
  });

  it('treats the other natively-1M families as 1M without a marker', () => {
    for (const id of [
      'claude-opus-4-6',
      'claude-opus-4-7',
      'claude-sonnet-4-6',
      'claude-fable-5-1',
      'claude-mythos-5',
    ]) {
      expect(contextLimitForModel(id)).toBe(CONTEXT_LIMIT_1M);
    }
  });

  it('treats Opus 5 and later Opus 5.x ids as natively 1M', () => {
    expect(contextLimitForModel('claude-opus-5')).toBe(CONTEXT_LIMIT_1M);
    expect(contextLimitForModel('claude-opus-5-1')).toBe(CONTEXT_LIMIT_1M);
  });

  it('treats GLM-5.x as 1M (the glm wrapper runs on the 1M window, id has no [1m])', () => {
    expect(contextLimitForModel('glm-5.2')).toBe(CONTEXT_LIMIT_1M);
    expect(contextLimitForModel('glm-5')).toBe(CONTEXT_LIMIT_1M);
    // glm-4.7 is the small/fast helper, not the session model → stays 200k.
    expect(contextLimitForModel('glm-4.7')).toBe(CONTEXT_LIMIT_DEFAULT);
  });

  it('honours an explicit [1m] marker regardless of family', () => {
    expect(contextLimitForModel('claude-opus-4-8[1m]')).toBe(CONTEXT_LIMIT_1M);
    expect(contextLimitForModel('some-model (1m)')).toBe(CONTEXT_LIMIT_1M);
  });

  it('keeps 200k models at 200k', () => {
    expect(contextLimitForModel('claude-haiku-4-5')).toBe(CONTEXT_LIMIT_DEFAULT);
    expect(contextLimitForModel(undefined)).toBe(CONTEXT_LIMIT_DEFAULT);
    expect(contextLimitForModel('')).toBe(CONTEXT_LIMIT_DEFAULT);
  });

  it('backstops an unknown id to 1M once occupancy exceeds the 200k default', () => {
    expect(contextLimitForModel('mystery-model', 250_000)).toBe(CONTEXT_LIMIT_1M);
    expect(contextLimitForModel('mystery-model', 10_000)).toBe(CONTEXT_LIMIT_DEFAULT);
  });
});

describe('prettyModelName', () => {
  it('preserves dotted Codex model ids and suffixes', () => {
    expect(prettyModelName('gpt-5.3-codex')).toBe('GPT 5.3-codex');
  });
  it('formats and strips the [1m] marker + provider prefix', () => {
    expect(prettyModelName('claude-opus-4-8')).toBe('Opus 4.8');
    expect(prettyModelName('claude-opus-4-8[1m]')).toBe('Opus 4.8');
    expect(prettyModelName('claude-fable-5-1')).toBe('Fable 5.1');
    expect(prettyModelName('claude-opus-5')).toBe('Opus 5');
    expect(prettyModelName('claude-sonnet-4-6')).toBe('Sonnet 4.6');
    expect(prettyModelName(undefined)).toBe('');
  });
});

describe('sameModelId', () => {
  it('matches ignoring case and the [1m] context marker', () => {
    expect(sameModelId('claude-fable-5-1[1m]', 'claude-fable-5-1')).toBe(true);
    expect(sameModelId('Claude-Sonnet-5', 'claude-sonnet-5')).toBe(true);
    expect(sameModelId('claude-fable-5-1', 'claude-sonnet-5')).toBe(false);
  });
  it('never matches on empty/undefined (an unknown current model marks nothing)', () => {
    expect(sameModelId(undefined, 'claude-fable-5-1')).toBe(false);
    expect(sameModelId('', '')).toBe(false);
  });
});

describe('MODEL_OPTIONS', () => {
  it('offers the current Claude generation with curated picker copy', () => {
    expect(MODEL_OPTIONS).toEqual([
      { id: 'claude-fable-5-1', label: 'Fable 5.1', hint: 'most capable' },
      { id: 'claude-opus-5', label: 'Opus 5', hint: 'flagship workhorse' },
      { id: 'claude-sonnet-5', label: 'Sonnet 5', hint: 'fast, everyday coding' },
      { id: 'claude-haiku-4-5', label: 'Haiku 4.5', hint: 'fastest, cheapest' },
    ]);
  });
  it('offers only concrete ids — no "default" row (resume stickiness makes it a lie)', () => {
    expect(MODEL_OPTIONS.length).toBeGreaterThan(0);
    for (const o of MODEL_OPTIONS) {
      expect(o.id).toMatch(/^claude-[a-z0-9-]+$/);
      expect(o.label).toBeTruthy();
    }
  });
  it('every option prettifies to its label family (picker and metabar agree)', () => {
    for (const o of MODEL_OPTIONS) {
      expect(prettyModelName(o.id)).toBe(o.label);
    }
  });
});
