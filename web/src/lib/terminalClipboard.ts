import { copyToClipboard } from './clipboard';

type CopyTerminal = { getSelection(): string; clearSelection(): void };

/** Return false only when the copy chord must not reach the shell as SIGINT. */
export function handleTerminalCopyKey(
  event: Pick<KeyboardEvent, 'type' | 'key' | 'ctrlKey' | 'metaKey' | 'shiftKey' | 'altKey'>,
  terminal: CopyTerminal,
  onFailure: () => void,
): boolean {
  if (event.type !== 'keydown' || event.key.toLowerCase() !== 'c' || event.altKey
    || (!event.ctrlKey && !event.metaKey)) return true;
  const text = terminal.getSelection();
  if (!text) return !(event.metaKey || event.shiftKey); // bare Ctrl+C remains SIGINT
  void copyToClipboard(text).then((copied) => {
    if (!copied) { onFailure(); return; }
    // Do not clear a newer selection made while an asynchronous copy was pending.
    if (!event.shiftKey && terminal.getSelection() === text) terminal.clearSelection();
  });
  return false;
}
