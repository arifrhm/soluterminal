import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  exportSettingsTelemetry,
  readSettingsTelemetry,
  recordSettingsEvent,
  resetSettingsTelemetry,
} from './settingsTelemetry';

const STORAGE_KEY = 'solu.settings.telemetry.v1';

describe('settings telemetry', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    localStorage.clear();
    resetSettingsTelemetry();
    vi.setSystemTime(new Date('2026-09-03T01:00:00.000Z'));
  });

  afterEach(() => {
    resetSettingsTelemetry();
    vi.useRealTimers();
  });

  it('starts a fresh aggregate when storage is empty', () => {
    expect(readSettingsTelemetry()).toEqual({
      version: 1,
      legacyTab: {},
      legacySection: {},
      legacyReveal: 0,
      searchOpens: 0,
      searchNavigations: 0,
      firstAt: '2026-09-03T01:00:00.000Z',
      lastAt: '2026-09-03T01:00:00.000Z',
    });
  });

  it('increments keyed counters and updates lastAt', () => {
    recordSettingsEvent('legacyTab', 'tasks');
    vi.setSystemTime(new Date('2026-09-03T01:02:00.000Z'));
    recordSettingsEvent('legacyTab', 'tasks');
    expect(readSettingsTelemetry()).toMatchObject({
      legacyTab: { tasks: 2 },
      lastAt: '2026-09-03T01:02:00.000Z',
    });
  });

  it('treats a keyed event without a key as a no-op', () => {
    const before = readSettingsTelemetry();
    recordSettingsEvent('legacySection');
    expect(readSettingsTelemetry()).toEqual(before);
    vi.advanceTimersByTime(1000);
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull();
  });

  it('tolerates corrupt stored JSON', () => {
    localStorage.setItem(STORAGE_KEY, '{not-json');
    expect(readSettingsTelemetry()).toMatchObject({
      version: 1, legacyTab: {}, legacySection: {}, legacyReveal: 0,
    });
  });

  it('debounces persistence for one second', () => {
    recordSettingsEvent('searchOpens');
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull();
    vi.advanceTimersByTime(999);
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull();
    vi.advanceTimersByTime(1);
    expect(JSON.parse(localStorage.getItem(STORAGE_KEY) ?? '{}')).toMatchObject({ searchOpens: 1 });
  });

  it('resets persisted and cached counts', () => {
    recordSettingsEvent('legacyReveal');
    vi.advanceTimersByTime(1000);
    expect(localStorage.getItem(STORAGE_KEY)).not.toBeNull();
    resetSettingsTelemetry();
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull();
    expect(readSettingsTelemetry()).toMatchObject({ legacyReveal: 0, legacyTab: {}, legacySection: {} });
  });

  it('exports only the declared aggregate fields', () => {
    recordSettingsEvent('legacyTab', 'tasks');
    const exported = JSON.parse(exportSettingsTelemetry()) as Record<string, unknown>;
    expect(Object.keys(exported).sort()).toEqual([
      'firstAt',
      'lastAt',
      'legacyReveal',
      'legacySection',
      'legacyTab',
      'searchNavigations',
      'searchOpens',
      'version',
    ]);
  });
});
