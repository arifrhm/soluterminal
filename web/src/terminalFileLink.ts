import { openEmbedFile } from './embed';

/** Resolve against the clicked pane's current cwd before crossing the host bridge. */
export function openTerminalFileLink(
  text: string,
  resolve: (path: string) => string | null,
  openLocal: (path: string) => void,
): void {
  const suffix = /:(\d+)(?::(\d+))?$/.exec(text);
  const path = resolve(suffix ? text.slice(0, suffix.index) : text);
  if (!path) return;
  const line = suffix ? Number(suffix[1]) : undefined;
  const col = suffix?.[2] === undefined ? undefined : Number(suffix[2]);
  if (!openEmbedFile(path, line, col)) openLocal(path);
}
