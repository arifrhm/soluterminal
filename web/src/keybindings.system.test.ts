import { describe, expect, it } from 'vitest';
import { actionById, comboFromEvent, resolveCombo } from './keybindings';

describe('system.toggle action', () => {
  it('uses Alt+O while leaving the voice-owned Alt+S chord free', () => {
    expect(resolveCombo('alt+o', {})).toBe('system.toggle');
    expect(resolveCombo('alt+s', {})).toBeNull();
    expect(actionById('system.toggle')?.defaultCombo).toBe('alt+o');
  });

  it('resolves a user override and frees Alt+O', () => {
    const overrides = { 'system.toggle': 'ctrl+e' };
    expect(resolveCombo('ctrl+e', overrides)).toBe('system.toggle');
    expect(resolveCombo('alt+o', overrides)).toBeNull();
  });

  it('serializes an Alt+O keyboard event', () => {
    const event = {
      key: 'o',
      ctrlKey: false,
      altKey: true,
      shiftKey: false,
      metaKey: false,
    } as KeyboardEvent;
    expect(comboFromEvent(event)).toBe('alt+o');
  });
});
