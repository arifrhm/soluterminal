import { describe, expect, it } from 'vitest';
import {
  LEGACY_SECTION_TARGETS,
  LEGACY_TAB_TARGETS,
  SETTINGS_CATEGORIES,
  SETTINGS_CONTROLS,
  SETTINGS_SECTIONS,
  controlById,
  formatSettingsHash,
  parseSettingsHash,
  resolveSettingsTarget,
  searchSettings,
  sectionById,
} from './settingsRegistry';

describe('settingsRegistry', () => {
  it('has unique ids and DOM ids with valid parents', () => {
    expect(new Set(SETTINGS_CATEGORIES.map(({ id }) => id)).size).toBe(SETTINGS_CATEGORIES.length);
    expect(new Set(SETTINGS_SECTIONS.map(({ id }) => id)).size).toBe(SETTINGS_SECTIONS.length);
    expect(new Set(SETTINGS_SECTIONS.map(({ domId }) => domId)).size).toBe(SETTINGS_SECTIONS.length);
    expect(new Set(SETTINGS_CONTROLS.map(({ id }) => id)).size).toBe(SETTINGS_CONTROLS.length);
    expect(new Set(SETTINGS_CONTROLS.map(({ domId }) => domId)).size).toBe(SETTINGS_CONTROLS.length);

    const categories = new Set(SETTINGS_CATEGORIES.map(({ id }) => id));
    const sections = new Set(SETTINGS_SECTIONS.map(({ id }) => id));
    for (const section of SETTINGS_SECTIONS) expect(categories.has(section.category)).toBe(true);
    for (const control of SETTINGS_CONTROLS) expect(sections.has(control.section)).toBe(true);

    expect(sectionById('workflows.modelsFallbacks')?.domId).toBe('settings-section-models-fallbacks');
    expect(controlById('workflows.resources.maxHeavyOps')?.domId).toBe('settings-row-maxHeavyOps');
  });

  it('distinguishes canonical ids from compatibility ids', () => {
    expect(Object.keys(LEGACY_TAB_TARGETS)).toEqual([
      'appearance', 'panes', 'claude', 'sidebar', 'team', 'devices', 'warden', 'tasks', 'agent', 'credentials',
    ]);
    for (const id of ['panes', 'claude', 'sidebar', 'warden', 'tasks', 'agent']) {
      expect(resolveSettingsTarget(id).legacy).toBe(true);
    }
    for (const id of ['appearance', 'team', 'devices', ...SETTINGS_CATEGORIES.map(({ id }) => id)]) {
      expect(resolveSettingsTarget(id).legacy).toBe(false);
    }
    for (const id of Object.keys(LEGACY_SECTION_TARGETS)) {
      expect(resolveSettingsTarget(id).legacy).toBe(true);
    }
    expect(resolveSettingsTarget('workflows.resources.maxHeavyOps').target).toEqual({
      category: 'workflows',
      section: 'workflows.resources',
      control: 'workflows.resources.maxHeavyOps',
    });
  });

  it('round-trips canonical hashes and accepts legacy hashes', () => {
    for (const target of [
      { category: 'credentials' as const },
      { category: 'workflows' as const, section: 'workflows.resources' },
      {
        category: 'workflows' as const,
        section: 'workflows.resources',
        control: 'workflows.resources.maxHeavyOps',
      },
    ]) {
      expect(parseSettingsHash(`#${formatSettingsHash(target)}`)).toEqual(target);
    }
    expect(parseSettingsHash('#settings=tasks.modelsFallbacks')).toEqual({
      category: 'workflows', section: 'workflows.modelsFallbacks',
    });
    expect(parseSettingsHash('#view=tasks')).toBeNull();
  });

  it.each([
    ['dark mode', 'appearance.theme', undefined],
    ['font size', 'appearance.text', undefined],
    ['fallback', 'workflows.modelsFallbacks', undefined],
    ['yolo', 'workflows.automation', undefined],
    ['heavy', 'workflows.resources', 'workflows.resources.maxHeavyOps'],
    ['worktree', 'workflows.resources', undefined],
    ['api key', 'credentials.keys', undefined],
    ['screen share', 'credentials.privacy', undefined],
    ['phone', 'devices.pairing', undefined],
    ['update', 'devices.updates', undefined],
  ])('routes alias %s to %s', (query, section, firstControl) => {
    const first = searchSettings(query, { mobile: false })[0];
    expect(first?.section.id).toBe(section);
    if (firstControl) expect(first?.control?.id).toBe(firstControl);
  });

  it('returns no results for an empty query or a hidden mobile category', () => {
    expect(searchSettings('', { mobile: false })).toEqual([]);
    expect(searchSettings('phone', { mobile: true })).toEqual([]);
  });
});
