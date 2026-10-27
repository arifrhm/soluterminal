// Insert text into a controlled <textarea> at the current cursor in a
// way React notices. React tracks the previous value internally so a
// plain `el.value = …` assignment is silently rolled back on the next
// commit; using the prototype's native value setter + dispatching a
// bubbling 'input' event makes React's onChange fire normally.
export function insertIntoTextarea(el: HTMLTextAreaElement, text: string): void {
  const start = el.selectionStart ?? el.value.length;
  const end = el.selectionEnd ?? el.value.length;
  const before = el.value.slice(0, start);
  const after = el.value.slice(end);
  // Add a space if we're inserting between existing words and there
  // isn't already whitespace on at least one side. Avoids "helloworld"
  // when dictating into the middle of a sentence.
  const needLeadingSpace = before.length > 0 && !/\s$/.test(before) && !/^\s/.test(text);
  const needTrailingSpace = after.length > 0 && !/^\s/.test(after) && !/\s$/.test(text);
  const insert = (needLeadingSpace ? ' ' : '') + text + (needTrailingSpace ? ' ' : '');
  const next = before + insert + after;
  const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')?.set;
  if (setter) setter.call(el, next);
  else el.value = next;
  el.dispatchEvent(new Event('input', { bubbles: true }));
  const caret = start + insert.length;
  el.focus();
  try {
    el.setSelectionRange(caret, caret);
  } catch {
    /* ignore */
  }
}
