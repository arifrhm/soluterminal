// composerAttach.ts — framework-free helpers for attaching a pasted/dropped/
// picked image to a native (Focus) pane's composer draft.
//
// The native message path sends a plain-text user turn to headless `claude`
// (stream-json stdin). Claude Code resolves an `@<path>` file mention INLINE in
// that turn — verified against a real PNG in -p --input-format stream-json mode,
// the model ingested the image with no Read tool call — so attaching an image is
// just: upload the bytes (→ ~/.soluterminal/pastes/<name> via /paste-image) and
// splice the returned `@<path>` into the draft. This is the SAME mechanism the
// terminal panes (paneImagePaste.ts) and MobileQuickReply already use; the
// helpers here keep the native composer's paste/drop/pick paths in one tested
// place. Kept out of the component so they're unit-testable and reusable.

// firstImageFile — the first image File in a paste/drop payload, or null. Checks
// the FileList first (drop / clipboard files), then the DataTransferItemList
// (some browsers only expose pasted images via items). Mirrors the terminal
// paste handler's probe so the two surfaces agree on what counts as an image.
export function firstImageFile(
  files: FileList | null | undefined,
  items?: DataTransferItemList | null,
): File | null {
  if (files) {
    for (let i = 0; i < files.length; i++) {
      const f = files[i];
      if (f && f.type.startsWith('image/')) return f;
    }
  }
  if (items) {
    for (let i = 0; i < items.length; i++) {
      const it = items[i];
      if (it.kind === 'file' && it.type.startsWith('image/')) {
        const f = it.getAsFile();
        if (f) return f;
      }
    }
  }
  return null;
}

// insertAttachToken — splice an `@<path>` mention into the draft. Appends a
// leading space when the draft is non-empty and doesn't already end in
// whitespace (so the token stays a separate word), and a trailing space so the
// caret sits ready for a caption. Claude reads the @-path as a file reference.
export function insertAttachToken(draft: string, path: string): string {
  const sep = draft.length > 0 && !/\s$/.test(draft) ? ' ' : '';
  return draft + sep + '@' + path + ' ';
}

// insertPlainPathToken - same spacing contract as insertAttachToken but with
// NO `@` prefix, for agents without Claude-style @-mention syntax. Native
// Muse panes receive the plain paste path (parity with terminal Muse panes -
// paneImagePaste.ts imageInjectText(path, false)); the daemon forwards the
// referenced file via `muse exec --image`, since muse never resolves `@path`
// mentions into vision bytes.
export function insertPlainPathToken(draft: string, path: string): string {
  const sep = draft.length > 0 && !/\s$/.test(draft) ? ' ' : '';
  return draft + sep + path + ' ';
}

// hasImagePayload — does a DataTransfer/clipboard carry an image (vs only text)?
// Used to decide whether a paste/drop should be intercepted. Checks both an
// explicit image file and the `Files` type marker (drag-over exposes types but
// not the file list yet).
export function hasImagePayload(dt: DataTransfer | null | undefined): boolean {
  if (!dt) return false;
  if (firstImageFile(dt.files, dt.items)) return true;
  return (dt.types ? Array.from(dt.types) : []).includes('Files');
}
