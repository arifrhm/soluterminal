import { afterEach, describe, expect, it, vi } from 'vitest';
import { copyToClipboard } from './clipboard';

const originalClipboard = Object.getOwnPropertyDescriptor(navigator, 'clipboard');
const originalExec = Object.getOwnPropertyDescriptor(document, 'execCommand');
afterEach(() => {
  if (originalClipboard) Object.defineProperty(navigator, 'clipboard', originalClipboard);
  else Reflect.deleteProperty(navigator, 'clipboard');
  if (originalExec) Object.defineProperty(document, 'execCommand', originalExec);
  else Reflect.deleteProperty(document, 'execCommand');
  document.body.replaceChildren();
  window.getSelection()?.removeAllRanges();
});

function install(clipboard: unknown, exec = vi.fn(() => true)) {
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: clipboard });
  Object.defineProperty(document, 'execCommand', { configurable: true, value: exec });
  return exec;
}

describe('copyToClipboard', () => {
  it('uses the secure API without creating a fallback field', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    const exec = install({ writeText });
    expect(await copyToClipboard('日本語\ntext')).toBe(true);
    expect(writeText).toHaveBeenCalledWith('日本語\ntext');
    expect(exec).not.toHaveBeenCalled();
    expect(document.querySelector('textarea')).toBeNull();
  });

  it.each(['missing', 'rejected', 'throws'])('falls back when the API is %s', async (mode) => {
    const writeText = mode === 'throws' ? () => { throw new Error('denied'); } : vi.fn().mockRejectedValue(new Error('denied'));
    const exec = install(mode === 'missing' ? undefined : { writeText }, vi.fn(() => {
      expect((document.activeElement as HTMLTextAreaElement).value).toBe('copied text');
      return true;
    }));
    expect(await copyToClipboard('copied text')).toBe(true);
    expect(exec).toHaveBeenCalledWith('copy');
    expect(document.querySelector('textarea')).toBeNull();
  });

  it.each(['false', 'throws'])('preserves focus and selection and removes fallback when legacy copy %s', async (mode) => {
    const input = document.createElement('input');
    input.value = 'keep my cursor';
    document.body.appendChild(input);
    input.focus();
    input.setSelectionRange(2, 6, 'backward');
    install(undefined, vi.fn(() => { if (mode === 'throws') throw new Error('blocked'); return false; }));
    expect(await copyToClipboard('text')).toBe(false);
    expect(document.querySelector('textarea')).toBeNull();
    expect(document.activeElement).toBe(input);
    expect([input.selectionStart, input.selectionEnd, input.selectionDirection]).toEqual([2, 6, 'backward']);
  });

  it('restores a native document selection after fallback', async () => {
    const text = document.createTextNode('keep selected');
    document.body.appendChild(text);
    const range = document.createRange();
    range.selectNodeContents(text);
    window.getSelection()!.removeAllRanges();
    window.getSelection()!.addRange(range);
    install(undefined);
    expect(await copyToClipboard('new text')).toBe(true);
    expect(window.getSelection()!.toString()).toBe('keep selected');
  });
});
