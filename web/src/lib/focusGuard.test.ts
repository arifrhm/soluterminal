import { afterEach, describe, expect, it } from 'vitest';
import { isTypingElsewhere } from './focusGuard';

// jsdom provides document + focus(); we mount throwaway elements and read
// document.activeElement just like the real focus effects do.

function mount<T extends HTMLElement>(el: T): T {
  document.body.appendChild(el);
  return el;
}

afterEach(() => {
  document.body.innerHTML = '';
});

describe('isTypingElsewhere', () => {
  it('false when nothing is focused', () => {
    expect(isTypingElsewhere(null)).toBe(false);
  });

  it('false when the focused editable IS the target (focusing it is a no-op steal)', () => {
    const ta = mount(document.createElement('textarea'));
    ta.focus();
    expect(document.activeElement).toBe(ta);
    expect(isTypingElsewhere(ta)).toBe(false);
  });

  it('true when a DIFFERENT textarea holds focus (user is typing elsewhere)', () => {
    const typing = mount(document.createElement('textarea'));
    const target = mount(document.createElement('textarea'));
    typing.focus();
    expect(isTypingElsewhere(target)).toBe(true);
  });

  it('true for a focused <input> that is not the target', () => {
    const input = mount(document.createElement('input'));
    const target = mount(document.createElement('textarea'));
    input.focus();
    expect(isTypingElsewhere(target)).toBe(true);
  });

  // (contenteditable is handled in production via isContentEditable, but jsdom
  // doesn't model contenteditable focusability, so it can't be exercised here.)

  it('false when focus is on a non-editable element (e.g. a button)', () => {
    const btn = mount(document.createElement('button'));
    const target = mount(document.createElement('textarea'));
    btn.focus();
    expect(isTypingElsewhere(target)).toBe(false);
  });

  // Reflow blur-to-<body> window: a layout reflow can blur the typed composer to
  // <body> in the same commit a background pane is selected. The recently-typed
  // tracker must keep reporting "typing elsewhere" so the background pane can't
  // read the transient blur as "stopped typing" and steal the caret.
  it('true when a different editable was just typed in but the reflow blurred it to <body>', () => {
    const typed = mount(document.createElement('textarea'));
    const target = mount(document.createElement('textarea'));
    // Simulate the user typing into `typed` (fires the capture-phase input listener).
    typed.focus();
    typed.dispatchEvent(new Event('input', { bubbles: true }));
    // Reflow blurs everything to <body>.
    typed.blur();
    expect(document.activeElement).toBe(document.body);
    // The grab from a background pane must still be treated as a steal.
    expect(isTypingElsewhere(target)).toBe(true);
    // …but refocusing the SAME element the user was typing in is fine (not "elsewhere").
    expect(isTypingElsewhere(typed)).toBe(false);
  });
});
