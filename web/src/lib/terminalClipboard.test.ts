import { beforeEach, describe, expect, it, vi } from 'vitest';
import { copyToClipboard } from './clipboard';
import { handleTerminalCopyKey } from './terminalClipboard';

vi.mock('./clipboard', () => ({ copyToClipboard: vi.fn() }));
const copy = vi.mocked(copyToClipboard);
const chord = (overrides: Partial<KeyboardEvent> = {}) => ({ type: 'keydown', key: 'c', ctrlKey: true, shiftKey: false, metaKey: false, altKey: false, ...overrides });
beforeEach(() => { copy.mockReset(); copy.mockResolvedValue(true); });

describe('terminal copy keyboard contract', () => {
  it.each([{ ctrlKey: true }, { ctrlKey: false, metaKey: true }, { ctrlKey: true, shiftKey: true, key: 'C' }])('copies a selected platform chord %j without sending it to the PTY', async (modifiers) => {
    const terminal = { getSelection: () => 'selected', clearSelection: vi.fn() };
    const failure = vi.fn();
    expect(handleTerminalCopyKey(chord(modifiers), terminal, failure)).toBe(false);
    await Promise.resolve();
    expect(copy).toHaveBeenCalledWith('selected');
    expect(terminal.clearSelection).toHaveBeenCalledTimes(modifiers.shiftKey ? 0 : 1);
    expect(failure).not.toHaveBeenCalled();
  });

  it('passes bare Ctrl+C through with no selection but never turns copy-only chords into SIGINT', () => {
    const terminal = { getSelection: () => '', clearSelection: vi.fn() };
    expect(handleTerminalCopyKey(chord(), terminal, vi.fn())).toBe(true);
    expect(handleTerminalCopyKey(chord({ shiftKey: true }), terminal, vi.fn())).toBe(false);
    expect(handleTerminalCopyKey(chord({ ctrlKey: false, metaKey: true }), terminal, vi.fn())).toBe(false);
    expect(copy).not.toHaveBeenCalled();
  });

  it('retains selection and reports failure if both clipboard paths fail', async () => {
    copy.mockResolvedValue(false);
    const terminal = { getSelection: () => 'selected', clearSelection: vi.fn() };
    const failure = vi.fn();
    expect(handleTerminalCopyKey(chord(), terminal, failure)).toBe(false);
    await Promise.resolve();
    expect(terminal.clearSelection).not.toHaveBeenCalled();
    expect(failure).toHaveBeenCalledOnce();
  });

  it('never clears a newer selection while clipboard permission is pending', async () => {
    let finish!: (ok: boolean) => void;
    copy.mockReturnValue(new Promise(resolve => { finish = resolve; }));
    let selection = 'first';
    const terminal = { getSelection: () => selection, clearSelection: vi.fn() };
    handleTerminalCopyKey(chord(), terminal, vi.fn());
    expect(terminal.clearSelection).not.toHaveBeenCalled();
    selection = 'second';
    finish(true);
    await Promise.resolve();
    expect(terminal.clearSelection).not.toHaveBeenCalled();
  });

  it.each([{ type: 'keyup' }, { key: 'v' }, { altKey: true }, { ctrlKey: false }])('leaves unrelated input alone: %j', overrides => {
    expect(handleTerminalCopyKey(chord(overrides), { getSelection: () => 'selected', clearSelection: vi.fn() }, vi.fn())).toBe(true);
    expect(copy).not.toHaveBeenCalled();
  });
});
