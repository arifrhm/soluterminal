import { describe, expect, it } from 'vitest';
import { mergeFeatureApis } from './api';

describe('mergeFeatureApis', () => {
  it('adds feature methods to the core api', () => {
    const core = { health: () => 'core' };
    const merged = mergeFeatureApis(core, [{ extra: () => 'feature' }]);
    expect(merged).toBe(core);
    expect(Object.keys(merged)).toEqual(['health', 'extra']);
  });

  it('refuses a feature method that would replace a core or earlier one', () => {
    expect(() => mergeFeatureApis({ health: () => 'core' }, [{ health: () => 'feature' }]))
      .toThrow('a feature API redefines api.health');
    expect(() => mergeFeatureApis({}, [{ extra: 1 }, { extra: 2 }]))
      .toThrow('a feature API redefines api.extra');
  });
});
