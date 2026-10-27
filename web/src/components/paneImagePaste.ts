import type { MutableRefObject } from 'react';
import { api, type RoutingScope } from '../api';
import { debugLog } from '../debug';
import { useStore } from '../store';

// Build the text injected into the PTY for an uploaded image. Claude panes
// keep the `@`-mention form Claude Code resolves natively; other CLIs get
// the plain path because `@`-syntax is meaningless to them (and the Muse TUI
// fails attaching it outright). Trailing space completes the token.
export function imageInjectText(path: string, atMention: boolean): string {
  return (atMention ? '@' : '') + path + ' ';
}

interface AttachPaneImagePasteHandlersOpts {
  hostEl: HTMLElement | null;
  paneId: string;
  wsRef: MutableRefObject<WebSocket | null>;
  ownedRef: MutableRefObject<boolean | null>;
  encoder: TextEncoder;
  atMention?: boolean;
  scope?: RoutingScope;
  captureGuard?: () => () => boolean;
  beginInteraction?: () => (() => void) | null;
}

export function attachPaneImagePasteHandlers({
  hostEl,
  paneId,
  wsRef,
  ownedRef,
  encoder,
  atMention = true, scope, captureGuard, beginInteraction,
}: AttachPaneImagePasteHandlersOpts): () => void {
  let disposed = false;
  const capture = () => { const valid = captureGuard?.() ?? (()=>true); return () => !disposed && ownedRef.current === true && valid(); };
  const uploadAndInjectImage = async (file: File, valid = capture()) => {
    if (!valid()) return;
    const release = beginInteraction?.();
    if (beginInteraction && !release) return;
    try {
      const { path } = await api.pasteImage(paneId, file, scope);
      if (!valid()) return;
      const ws = wsRef.current;
      if (ws && ws.readyState === WebSocket.OPEN) {
        // No newline: user composes the rest manually.
        ws.send(encoder.encode(imageInjectText(path, atMention)));
      }
      useStore.getState().toast(
        `Pasted image: ${path.split(/[\\/]/).pop()}`,
        { kind: 'info', ttlMs: 2500 },
      );
    } catch (err) {
      if (!valid()) return;
      useStore.getState().toast(
        `Paste image failed: ${(err as Error).message}`,
        { kind: 'error', ttlMs: 4000 },
      );
    } finally { release?.(); }
  };

  const firstImageFile = (
    files: FileList | null | undefined,
    items?: DataTransferItemList | null,
  ): File | null => {
    if (files) {
      for (let i = 0; i < files.length; i++) {
        const f = files[i];
        if (f.type.startsWith('image/')) return f;
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
  };

  const readImageFromAsyncClipboard = async (valid:()=>boolean): Promise<File | null> => {
    try {
      if (!valid() || !navigator.clipboard || !navigator.clipboard.read) return null;
      const items = await navigator.clipboard.read();
      if(!valid())return null;
      for (const item of items) {
        for (const type of item.types) {
          if (type.startsWith('image/')) {
            if(!valid())return null;
            const blob = await item.getType(type);
            if(!valid())return null;
            const ext = (type.split('/')[1] || 'png').split('+')[0];
            return new File([blob], `clipboard.${ext}`, { type });
          }
        }
      }
    } catch (err) {
      console.warn('[solu paste] async clipboard read failed:', err);
    }
    return null;
  };

  const onPaste = (e: ClipboardEvent) => {
    if (!capture()()) return;
    const cd = e.clipboardData;
    const syncFile = cd ? firstImageFile(cd.files, cd.items) : null;
    debugLog(
      '[solu paste] event fired. files=%d items=%d types=%s syncImage=%s',
      cd?.files?.length ?? 0,
      cd?.items?.length ?? 0,
      JSON.stringify(Array.from(cd?.types ?? [])),
      syncFile ? `${syncFile.type}/${syncFile.size}B` : 'none',
    );
    if (syncFile) {
      e.preventDefault();
      e.stopPropagation();
      void uploadAndInjectImage(syncFile);
      return;
    }
    const hasText = (cd?.types ?? []).some((t) => t === 'text/plain' || t === 'text');
    if (hasText) return;
    e.preventDefault();
    e.stopPropagation();
    const valid = capture();
    const release = beginInteraction?.();
    if (beginInteraction && !release) return;
    (async () => {
      try {
      if (!valid()) return;
      const file = await readImageFromAsyncClipboard(valid);
      if (!valid()) return;
      if (file) {
        await uploadAndInjectImage(file,valid);
      } else {
        useStore.getState().toast(
          'No image found on clipboard',
          { kind: 'warn', ttlMs: 2500 },
        );
      }
      } finally { release?.(); }
    })();
  };

  const hasFiles = (dt: DataTransfer | null): boolean =>
    !!dt && (dt.types?.includes('Files') ?? false);

  const onDragOverFiles = (e: DragEvent) => {
    if (!capture()()) return;
    if (!hasFiles(e.dataTransfer)) return;
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy';
  };

  const onDropFiles = (e: DragEvent) => {
    if (!capture()()) return;
    if (!hasFiles(e.dataTransfer)) return;
    e.preventDefault();
    e.stopPropagation();
    const file = firstImageFile(e.dataTransfer?.files, e.dataTransfer?.items);
    if (!file) {
      useStore.getState().toast(
        'Drop: no image file found',
        { kind: 'warn', ttlMs: 2500 },
      );
      return;
    }
    void uploadAndInjectImage(file);
  };

  // Attach at the host element in capture phase so we run before xterm's
  // own paste listener on the inner helper textarea.
  hostEl?.addEventListener('paste', onPaste, { capture: true });
  hostEl?.addEventListener('dragover', onDragOverFiles, { capture: true });
  hostEl?.addEventListener('drop', onDropFiles, { capture: true });

  return () => {
    disposed = true;
    hostEl?.removeEventListener('paste', onPaste, { capture: true });
    hostEl?.removeEventListener('dragover', onDragOverFiles, { capture: true });
    hostEl?.removeEventListener('drop', onDropFiles, { capture: true });
  };
}
