import { describe, expect, it } from 'vitest';
import { isVersionNewer } from './useCloudShellUpdate';

describe('isVersionNewer', () => {
  it('detects the real stranded-cloud-shell case (0.0.835 > 0.0.803)', () => {
    expect(isVersionNewer('0.0.835', '0.0.803')).toBe(true);
  });

  it('is strict — equal versions are not newer', () => {
    expect(isVersionNewer('0.0.835', '0.0.835')).toBe(false);
  });

  it('never nags a shell that is already ahead', () => {
    expect(isVersionNewer('0.0.803', '0.0.835')).toBe(false);
  });

  it('compares numerically, not lexically (0.0.10 > 0.0.9)', () => {
    expect(isVersionNewer('0.0.10', '0.0.9')).toBe(true);
    expect(isVersionNewer('0.0.9', '0.0.10')).toBe(false);
  });

  it('handles differing component counts (missing reads as 0)', () => {
    expect(isVersionNewer('0.1', '0.0.999')).toBe(true);
    expect(isVersionNewer('1', '0.9.9')).toBe(true);
    expect(isVersionNewer('0.0.5', '0.0.5.0')).toBe(false);
  });
});
