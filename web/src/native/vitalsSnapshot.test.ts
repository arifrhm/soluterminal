// Tests for the per-pane "last known vitals" snapshot that rehydrates the Focus
// pane's bottom NativeMetaBar instantly on (re-)entry, killing the async-seed
// blank flash. Pure module + localStorage (jsdom), no React render — mirrors the
// draftBridge.test idiom.

import { describe, it, expect, beforeEach } from 'vitest';
import {
  getVitalsSnapshot,
  saveVitalsSnapshot,
  clearVitalsSnapshot,
  clearVitalsContextPct,
  sanitizeVitalsSnapshot,
  __vitalsSnapshotTest,
} from './vitalsSnapshot';

beforeEach(() => {
  localStorage.clear();
  __vitalsSnapshotTest.reset();
});

describe('vitals snapshot — sanitizer', () => {
  it('returns {} for non-object / null / corrupt input', () => {
    expect(sanitizeVitalsSnapshot(null)).toEqual({});
    expect(sanitizeVitalsSnapshot(undefined)).toEqual({});
    expect(sanitizeVitalsSnapshot('garbage')).toEqual({});
    expect(sanitizeVitalsSnapshot(42)).toEqual({});
  });

  it('keeps only known, correctly-typed fields', () => {
    const out = sanitizeVitalsSnapshot({
      model: 'Opus 4.8',
      contextPct: 0.42,
      cost: 0.18,
      cwd: '…/soluterminal',
      cwdFull: '/home/x/soluterminal',
      bogus: 'drop me',
      cost2: 99,
    });
    expect(out).toEqual({
      model: 'Opus 4.8',
      contextPct: 0.42,
      cost: 0.18,
      cwd: '…/soluterminal',
      cwdFull: '/home/x/soluterminal',
    });
  });

  it('drops wrong-typed / NaN / negative numerics and empty strings', () => {
    const out = sanitizeVitalsSnapshot({
      model: '', // empty → dropped
      contextPct: 'high', // wrong type → dropped
      cost: -5, // negative → dropped
      cwd: 123, // wrong type → dropped
    });
    expect(out).toEqual({});
  });

  it('clamps contextPct into 0..1', () => {
    expect(sanitizeVitalsSnapshot({ contextPct: 1.7 }).contextPct).toBe(1);
    expect(sanitizeVitalsSnapshot({ contextPct: -0.3 }).contextPct).toBe(0);
  });
});

describe('vitals snapshot — save / get round-trip', () => {
  it('returns {} when nothing was ever saved', () => {
    expect(getVitalsSnapshot('pane-x')).toEqual({});
  });

  it('persists across a module-mirror reset (reads back from localStorage)', () => {
    saveVitalsSnapshot('pane-1', { model: 'Opus 4.8', contextPct: 0.5, cost: 0.1 });
    __vitalsSnapshotTest.reset(); // simulate a reload — only localStorage survives
    expect(getVitalsSnapshot('pane-1')).toEqual({
      model: 'Opus 4.8',
      contextPct: 0.5,
      cost: 0.1,
    });
  });

  it('isolates snapshots per pane', () => {
    saveVitalsSnapshot('pane-a', { model: 'Opus 4.8' });
    saveVitalsSnapshot('pane-b', { model: 'Sonnet 4.6' });
    expect(getVitalsSnapshot('pane-a').model).toBe('Opus 4.8');
    expect(getVitalsSnapshot('pane-b').model).toBe('Sonnet 4.6');
  });

  it('does not leak Claude model/context/cost into Codex on a reused pane id', () => {
    saveVitalsSnapshot('pane-shared', {
      model: 'Opus 4.8',
      contextPct: 0.42,
      cost: 1.5,
      cwd: '…/soluterminal',
    }, 'claude');

    expect(getVitalsSnapshot('pane-shared', 'codex')).toEqual({ cwd: '…/soluterminal' });

    saveVitalsSnapshot('pane-shared', { model: 'GPT 5.3-codex' }, 'codex');
    expect(getVitalsSnapshot('pane-shared', 'codex')).toEqual({
      agent: 'codex',
      model: 'GPT 5.3-codex',
      cwd: '…/soluterminal',
    });
  });

  it('treats pre-agent legacy model data as untrusted for an active provider', () => {
    localStorage.setItem('solu.nativeVitals.legacy', JSON.stringify({
      model: 'Opus 4.8',
      cwd: '…/project',
    }));
    expect(getVitalsSnapshot('legacy', 'codex')).toEqual({ cwd: '…/project' });
  });
});

describe('vitals snapshot — merge semantics', () => {
  it('MERGES partial updates instead of replacing', () => {
    saveVitalsSnapshot('p', { model: 'Opus 4.8', contextPct: 0.4 });
    saveVitalsSnapshot('p', { cost: 0.22 }); // partial — must not drop model/ctx
    expect(getVitalsSnapshot('p')).toEqual({
      model: 'Opus 4.8',
      contextPct: 0.4,
      cost: 0.22,
    });
  });

  it('a blank/undefined update never WIPES a good field', () => {
    saveVitalsSnapshot('p', { model: 'Opus 4.8', cost: 0.5 });
    // A transient blank derive (sessionInit not yet loaded) sends undefined/empty.
    saveVitalsSnapshot('p', { model: undefined, cost: undefined, contextPct: undefined });
    expect(getVitalsSnapshot('p')).toEqual({ model: 'Opus 4.8', cost: 0.5 });
  });

  it('a later defined value overwrites the earlier one', () => {
    saveVitalsSnapshot('p', { contextPct: 0.3 });
    saveVitalsSnapshot('p', { contextPct: 0.7 });
    expect(getVitalsSnapshot('p').contextPct).toBe(0.7);
  });
});

describe('vitals snapshot — clear', () => {
  it('drops the snapshot from both the mirror and localStorage', () => {
    saveVitalsSnapshot('p', { model: 'Opus 4.8' });
    clearVitalsSnapshot('p');
    expect(getVitalsSnapshot('p')).toEqual({});
    expect(localStorage.getItem('solu.nativeVitals.p')).toBeNull();
  });
});

describe('vitals snapshot — clearVitalsContextPct (on /compact)', () => {
  it('drops ONLY contextPct, keeping model/cost/cwd', () => {
    saveVitalsSnapshot('p', { model: 'Opus 4.8', contextPct: 0.42, cost: 1.5, cwd: '…/x' });
    clearVitalsContextPct('p');
    const snap = getVitalsSnapshot('p');
    expect(snap.contextPct).toBeUndefined();
    expect(snap).toEqual({ model: 'Opus 4.8', cost: 1.5, cwd: '…/x' });
  });

  it('persists the cleared state across a module-mirror reset (reload)', () => {
    saveVitalsSnapshot('p', { model: 'Opus 4.8', contextPct: 0.42 });
    clearVitalsContextPct('p');
    __vitalsSnapshotTest.reset(); // simulate a reload — only localStorage survives
    expect(getVitalsSnapshot('p').contextPct).toBeUndefined();
    expect(getVitalsSnapshot('p').model).toBe('Opus 4.8');
  });

  it('is a no-op when there is no contextPct to clear', () => {
    saveVitalsSnapshot('p', { model: 'Opus 4.8' });
    clearVitalsContextPct('p'); // must not throw / must not wipe the rest
    expect(getVitalsSnapshot('p')).toEqual({ model: 'Opus 4.8' });
  });
});

describe('vitals snapshot — corrupt localStorage blob', () => {
  it('degrades to {} on a non-JSON blob', () => {
    localStorage.setItem('solu.nativeVitals.p', '{not json');
    expect(getVitalsSnapshot('p')).toEqual({});
  });
});
