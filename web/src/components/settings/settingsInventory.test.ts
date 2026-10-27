import { describe, expect, it } from 'vitest';
import { SETTINGS_SECTION_IDS } from '../../settings';
import {
  SETTINGS_CATEGORIES,
  SETTINGS_CONTROLS,
  SETTINGS_SECTIONS,
  controlById,
  sectionById,
} from '../../settingsRegistry';
import { SETTINGS_INVENTORY } from './settingsInventory';

function sortedUnique(values: string[]): string[] {
  return [...new Set(values)].sort();
}

describe('settings inventory', () => {
  it('preserves every visible setting label in its new category', () => {
    for (const category of SETTINGS_CATEGORIES) {
      const sectionIds = new Set(
        SETTINGS_SECTIONS.filter((section) => section.category === category.id).map(({ id }) => id),
      );
      const registryLabels = SETTINGS_CONTROLS
        .filter((control) => sectionIds.has(control.section))
        .map(({ label }) => label);
      expect(sortedUnique(registryLabels)).toEqual(sortedUnique(SETTINGS_INVENTORY[category.id]));
    }

    const inventoryLabels = new Set(Object.values(SETTINGS_INVENTORY).flat());
    const registryLabels = new Set(SETTINGS_CONTROLS.map(({ label }) => label));
    expect([...inventoryLabels].filter((label) => !registryLabels.has(label))).toEqual([]);
    expect([...registryLabels].filter((label) => !inventoryLabels.has(label))).toEqual([]);
  });

  it('keeps the two existing delayed-focus DOM ids stable', () => {
    expect(SETTINGS_SECTION_IDS['tasks.resourceLimits.maxHeavyOps']).toBe(
      controlById('workflows.resources.maxHeavyOps')?.domId,
    );
    expect(SETTINGS_SECTION_IDS['tasks.modelsFallbacks']).toBe(
      sectionById('workflows.modelsFallbacks')?.domId,
    );
  });
});
