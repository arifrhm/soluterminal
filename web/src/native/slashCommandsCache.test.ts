// Tests for the GLOBAL "last known slash commands" cache that lets a dormant
// Focus pane (transcript seed only, no live session_init) populate its slash
// menu with the last-known skills instead of the 7-command curated fallback.
// Pure module + localStorage (jsdom), no React render — mirrors vitalsSnapshot.test.

import { describe, it, expect, beforeEach } from 'vitest';
import {
  getKnownSlashCommands,
  rememberSlashCommands,
  sanitizeSlashCommands,
  __slashCommandsCacheTest,
} from './slashCommandsCache';

beforeEach(() => {
  localStorage.clear();
  __slashCommandsCacheTest.reset();
});

describe('slashCommandsCache — sanitizer', () => {
  it('returns [] for non-array / null / corrupt input', () => {
    expect(sanitizeSlashCommands(null)).toEqual([]);
    expect(sanitizeSlashCommands(undefined)).toEqual([]);
    expect(sanitizeSlashCommands('garbage')).toEqual([]);
    expect(sanitizeSlashCommands({ a: 1 })).toEqual([]);
  });

  it('keeps non-empty strings, drops non-strings + blanks, NORMALIZES to /name, dedups', () => {
    expect(
      sanitizeSlashCommands(['context', '  review  ', '', 'context', 7, null, 'supabase:supabase']),
    ).toEqual(['/context', '/review', '/supabase:supabase']);
  });

  it('normalizes before dedup so "context" and "/context" collapse to one entry', () => {
    expect(sanitizeSlashCommands(['context', '/context', 'review'])).toEqual(['/context', '/review']);
  });
});

describe('slashCommandsCache — get / remember', () => {
  it('returns [] when nothing is stored', () => {
    expect(getKnownSlashCommands()).toEqual([]);
  });

  it('remember persists a set and get reads it back', () => {
    rememberSlashCommands(['clear', 'compact', 'context', 'review']);
    expect(getKnownSlashCommands()).toEqual(['/clear', '/compact', '/context', '/review']);
  });

  it('survives a "reload" (mirror reset) by reading back from localStorage', () => {
    rememberSlashCommands(['context', 'review']);
    __slashCommandsCacheTest.reset(); // simulate a full reload — only localStorage survives
    expect(getKnownSlashCommands()).toEqual(['/context', '/review']);
  });

  it('returns a defensive copy (mutating the result does not corrupt the cache)', () => {
    rememberSlashCommands(['context', 'review']);
    const a = getKnownSlashCommands();
    a.push('INJECTED');
    const b = getKnownSlashCommands();
    expect(b).toEqual(['/context', '/review']);
    expect(b).not.toBe(a);
  });

  it('never wipes a good cache with an empty derive', () => {
    rememberSlashCommands(['context', 'review']);
    rememberSlashCommands([]);
    rememberSlashCommands(null);
    expect(getKnownSlashCommands()).toEqual(['/context', '/review']);
  });

  it('idempotency: remembering the same members in a different order is a no-op write', () => {
    rememberSlashCommands(['context', 'review', 'clear']);
    const before = localStorage.getItem('solu.knownSlashCommands');
    rememberSlashCommands(['clear', 'review', 'context']); // same set, reordered
    const after = localStorage.getItem('solu.knownSlashCommands');
    expect(after).toBe(before); // unchanged blob
    expect(getKnownSlashCommands().sort()).toEqual(['/clear', '/context', '/review']);
  });

  it('updates when a genuinely different set arrives', () => {
    rememberSlashCommands(['context', 'review']);
    rememberSlashCommands(['context', 'review', 'security-review', 'goal']);
    expect(getKnownSlashCommands().sort()).toEqual(['/context', '/goal', '/review', '/security-review']);
  });

  it('isolates Claude and Codex command sets', () => {
    rememberSlashCommands(['context', 'review'], 'claude');
    rememberSlashCommands(['status', 'permissions'], 'codex');
    expect(getKnownSlashCommands('claude')).toEqual(['/context', '/review']);
    expect(getKnownSlashCommands('codex')).toEqual(['/status', '/permissions']);
  });

  it('survives a corrupt localStorage blob by degrading to []', () => {
    localStorage.setItem('solu.knownSlashCommands', '{not valid json');
    __slashCommandsCacheTest.reset();
    expect(getKnownSlashCommands()).toEqual([]);
  });
});
