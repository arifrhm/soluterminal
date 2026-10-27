import { describe, it, expect } from 'vitest';
import {
  isOwnable,
  ownedByOther,
  canClaim,
  applyClaim,
  dropOwnedBy,
  pruneStale,
  type OwnersMap,
} from './workspaceOwnership';

const TTL = 15_000;

describe('workspaceOwnership — pure logic', () => {
  it('isOwnable: named workspaces yes, scratch/empty no', () => {
    expect(isOwnable('Foo')).toBe(true);
    expect(isOwnable('_current')).toBe(false);
    expect(isOwnable('')).toBe(false);
    expect(isOwnable(null)).toBe(false);
    expect(isOwnable(undefined)).toBe(false);
  });

  describe('ownedByOther / canClaim', () => {
    it('a free workspace is claimable by anyone', () => {
      expect(ownedByOther({}, 'Foo', 'main', 1000, TTL)).toBeNull();
      expect(canClaim({}, 'Foo', 'main', 1000, TTL)).toBe(true);
    });

    it('a workspace held by SELF is claimable (restore-after-reload)', () => {
      const m: OwnersMap = { Foo: { label: 'main', ts: 1000 } };
      expect(ownedByOther(m, 'Foo', 'main', 1500, TTL)).toBeNull();
    });

    it('a workspace held LIVE by another window blocks the claim', () => {
      const m: OwnersMap = { Foo: { label: 'workspace_2', ts: 1000 } };
      // exactly at the TTL boundary the entry is still live
      expect(ownedByOther(m, 'Foo', 'main', 1000 + TTL, TTL)).toBe('workspace_2');
      expect(canClaim(m, 'Foo', 'main', 1000 + TTL, TTL)).toBe(false);
    });

    it('a STALE claim by another window is reclaimable', () => {
      const m: OwnersMap = { Foo: { label: 'workspace_2', ts: 1000 } };
      expect(ownedByOther(m, 'Foo', 'main', 1000 + TTL + 1, TTL)).toBeNull();
      expect(canClaim(m, 'Foo', 'main', 1000 + TTL + 1, TTL)).toBe(true);
    });

    it('scratch (_current) is never owned, even if a map entry exists', () => {
      const m: OwnersMap = { _current: { label: 'workspace_2', ts: 1000 } };
      expect(ownedByOther(m, '_current', 'main', 1000, TTL)).toBeNull();
    });
  });

  describe('applyClaim', () => {
    it('records the claim and drops other workspaces owned by the same window', () => {
      const m: OwnersMap = {
        Old: { label: 'main', ts: 500 },
        Other: { label: 'workspace_2', ts: 500 },
      };
      const next = applyClaim(m, 'New', 'main', 2000);
      expect(next).toEqual({
        Other: { label: 'workspace_2', ts: 500 },
        New: { label: 'main', ts: 2000 },
      });
      // input is not mutated
      expect(m.Old).toEqual({ label: 'main', ts: 500 });
    });

    it('claiming scratch drops this window\'s prior claim and records nothing', () => {
      const m: OwnersMap = {
        Old: { label: 'main', ts: 500 },
        Other: { label: 'workspace_2', ts: 500 },
      };
      expect(applyClaim(m, '_current', 'main', 2000)).toEqual({
        Other: { label: 'workspace_2', ts: 500 },
      });
    });

    it('is forceful post-guard: overwrites a different owner', () => {
      const m: OwnersMap = { Foo: { label: 'workspace_2', ts: 500 } };
      expect(applyClaim(m, 'Foo', 'main', 2000)).toEqual({ Foo: { label: 'main', ts: 2000 } });
    });
  });

  it('dropOwnedBy removes only entries for the given label', () => {
    const m: OwnersMap = { A: { label: 'main', ts: 1 }, B: { label: 'workspace_2', ts: 1 } };
    expect(dropOwnedBy(m, 'main')).toEqual({ B: { label: 'workspace_2', ts: 1 } });
  });

  it('pruneStale keeps only live entries', () => {
    const m: OwnersMap = { A: { label: 'main', ts: 0 }, B: { label: 'workspace_2', ts: 10_000 } };
    expect(pruneStale(m, 16_000, TTL)).toEqual({ B: { label: 'workspace_2', ts: 10_000 } });
  });
});
