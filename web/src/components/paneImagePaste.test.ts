import { beforeEach, describe, expect, it, vi } from 'vitest';
import { attachPaneImagePasteHandlers, imageInjectText } from './paneImagePaste';

const { toastMock } = vi.hoisted(() => ({ toastMock: vi.fn() }));
vi.mock('../store', () => ({ useStore: { getState: () => ({ toast: toastMock }) } }));
const { pasteMock, pasteFileMock } = vi.hoisted(() => ({ pasteMock: vi.fn(), pasteFileMock: vi.fn() }));
vi.mock('../api', () => ({ api: { pasteImage: pasteMock, pasteFile: pasteFileMock } }));

if (typeof WebSocket === 'undefined' || (WebSocket as unknown as { OPEN?: number }).OPEN !== 1) {
  (globalThis as Record<string, unknown>).WebSocket = { OPEN: 1 };
}

// Minimal FileList/DataTransferItemList stand-ins (same shape the handler
// probes: length + indexed access + item()).
function fileList(files: File[]): FileList {
  const obj: Record<number, File> = {};
  files.forEach((f, i) => { obj[i] = f; });
  return { ...obj, length: files.length, item: (i: number) => files[i] ?? null } as unknown as FileList;
}
function itemList(files: File[]): DataTransferItemList {
  const obj: Record<number, unknown> = {};
  files.forEach((f, i) => { obj[i] = { kind: 'file' as const, type: f.type, getAsFile: () => f }; });
  return { ...obj, length: files.length } as unknown as DataTransferItemList;
}

const png = () => new File(['x'], 'a.png', { type: 'image/png' });
const pdf = () => new File(['x'], 'report.pdf', { type: 'application/pdf' });
const PATH = '/home/u/.soluterminal/pastes/solu-paste-abc.png';
const FILE_PATH = '/home/u/.soluterminal/pastes/solu-paste-xyz.pdf';

function setup(atMention?: boolean) {
  const host = document.createElement('div');
  const send = vi.fn();
  const wsRef = { current: { readyState: 1, send } as unknown as WebSocket };
  const cleanup = attachPaneImagePasteHandlers({
    hostEl: host,
    paneId: 'pane-1',
    wsRef,
    ownedRef: { current: true },
    encoder: new TextEncoder(),
    ...(atMention === undefined ? {} : { atMention }),
  });
  return { host, send, cleanup };
}

function pasteImage(host: HTMLElement): boolean {
  const files = [png()];
  const event = new Event('paste', { bubbles: true, cancelable: true });
  Object.defineProperty(event, 'clipboardData', {
    value: { files: fileList(files), items: itemList(files), types: ['Files'] },
  });
  return host.dispatchEvent(event);
}

function dropItem(host: HTMLElement, files: File[]): boolean {
  const event = new Event('drop', { bubbles: true, cancelable: true });
  Object.defineProperty(event, 'dataTransfer', {
    value: { files: fileList(files), items: itemList(files), types: ['Files'] },
  });
  return host.dispatchEvent(event);
}

beforeEach(() => {
  vi.clearAllMocks();
  pasteMock.mockResolvedValue({ path: PATH, bytes: 1 });
  pasteFileMock.mockResolvedValue({ path: FILE_PATH, bytes: 10 });
});

describe('imageInjectText', () => {
  it('keeps the @-mention form by default', () => {
    expect(imageInjectText(PATH, true)).toBe(`@${PATH} `);
  });

  it('emits the plain path when @-mentions are off', () => {
    expect(imageInjectText(PATH, false)).toBe(`${PATH} `);
  });
});

describe('pane image paste injection', () => {
  it('injects @path for default (Claude-style) panes', async () => {
    const { host, send, cleanup } = setup();
    try {
      expect(pasteImage(host)).toBe(false); // preventDefault claimed the paste
      await vi.waitFor(() => expect(send).toHaveBeenCalledOnce());
      expect(new TextDecoder().decode(send.mock.calls[0][0] as Uint8Array)).toBe(`@${PATH} `);
    } finally { cleanup(); }
  });

  it('injects the plain path for Muse panes (atMention: false)', async () => {
    const { host, send, cleanup } = setup(false);
    try {
      expect(pasteImage(host)).toBe(false);
      await vi.waitFor(() => expect(send).toHaveBeenCalledOnce());
      expect(new TextDecoder().decode(send.mock.calls[0][0] as Uint8Array)).toBe(`${PATH} `);
    } finally { cleanup(); }
  });

  it('toasts instead of injecting when the upload fails', async () => {
    pasteMock.mockRejectedValueOnce(new Error('pane not found'));
    const { host, send, cleanup } = setup(false);
    try {
      pasteImage(host);
      await vi.waitFor(() => expect(toastMock).toHaveBeenCalledOnce());
      expect(toastMock.mock.calls[0][0]).toMatch(/Paste image failed/);
      expect(send).not.toHaveBeenCalled();
    } finally { cleanup(); }
  });
});

it('delayed upload cannot inject or toast after a presentation switch and return',async()=>{
 let resolve!:(value:{path:string})=>void;pasteMock.mockReturnValue(new Promise(r=>{resolve=r;}));
 const host=document.createElement('div'),send=vi.fn();let generation=0;
 const cleanup=attachPaneImagePasteHandlers({hostEl:host,paneId:'pane-1',wsRef:{current:{readyState:1,send} as unknown as WebSocket},ownedRef:{current:true},encoder:new TextEncoder(),captureGuard:()=>{const captured=generation;return()=>captured===generation;}});
 pasteImage(host);expect(pasteMock).toHaveBeenCalledTimes(1);generation++;generation++;
 resolve({path:PATH});await Promise.resolve();await Promise.resolve();expect(send).not.toHaveBeenCalled();expect(toastMock).not.toHaveBeenCalled();cleanup();
});
it('cleanup invalidates an in-flight upload even when the same socket survives',async()=>{
 let resolve!:(value:{path:string})=>void;pasteMock.mockReturnValue(new Promise(r=>{resolve=r;}));const {host,send,cleanup}=setup();pasteImage(host);cleanup();resolve({path:PATH});await Promise.resolve();await Promise.resolve();expect(send).not.toHaveBeenCalled();expect(toastMock).not.toHaveBeenCalled();
});

describe('image upload interaction lease', () => {
  it('holds the retirement veto until the upload settles, including failure', async () => {
    let rejectUpload!: (error: Error) => void;
    pasteMock.mockReturnValue(new Promise((_resolve, reject) => { rejectUpload = reject; }));
    const release = vi.fn();
    const beginInteraction = vi.fn(() => release);
    const host = document.createElement('div');
    const cleanup = attachPaneImagePasteHandlers({
      hostEl: host, paneId: 'pane-1', wsRef: {current: {readyState: 1, send: vi.fn()} as unknown as WebSocket},
      ownedRef: {current: true}, encoder: new TextEncoder(), beginInteraction,
    });
    try {
      pasteImage(host);
      expect(beginInteraction).toHaveBeenCalledOnce();
      expect(release).not.toHaveBeenCalled();
      rejectUpload(new Error('fixture upload failed'));
      await vi.waitFor(() => expect(release).toHaveBeenCalledOnce());
    } finally { cleanup(); }
  });
});

describe('pane file drop handling', () => {
  it('uploads and injects @path when a non-image file is dropped', async () => {
    const { host, send, cleanup } = setup();
    try {
      const file = pdf();
      expect(dropItem(host, [file])).toBe(false);
      await vi.waitFor(() => expect(pasteFileMock).toHaveBeenCalledOnce());
      expect(pasteFileMock).toHaveBeenCalledWith('pane-1', file, 'report.pdf', undefined);
      await vi.waitFor(() => expect(send).toHaveBeenCalledOnce());
      expect(new TextDecoder().decode(send.mock.calls[0][0] as Uint8Array)).toBe(`@${FILE_PATH} `);
      expect(toastMock).toHaveBeenCalledWith('Pasted file: solu-paste-xyz.pdf', expect.any(Object));
    } finally { cleanup(); }
  });

  it('uploads via pasteImage when an image is dropped', async () => {
    const { host, send, cleanup } = setup();
    try {
      const file = png();
      expect(dropItem(host, [file])).toBe(false);
      await vi.waitFor(() => expect(pasteMock).toHaveBeenCalledOnce());
      expect(pasteMock).toHaveBeenCalledWith('pane-1', file, undefined);
      await vi.waitFor(() => expect(send).toHaveBeenCalledOnce());
      expect(new TextDecoder().decode(send.mock.calls[0][0] as Uint8Array)).toBe(`@${PATH} `);
      expect(toastMock).toHaveBeenCalledWith('Pasted image: solu-paste-abc.png', expect.any(Object));
    } finally { cleanup(); }
  });

  it('toasts error when dropping a non-image file fails', async () => {
    pasteFileMock.mockRejectedValueOnce(new Error('unsupported file type'));
    const { host, send, cleanup } = setup();
    try {
      dropItem(host, [pdf()]);
      await vi.waitFor(() => expect(toastMock).toHaveBeenCalledOnce());
      expect(toastMock.mock.calls[0][0]).toMatch(/Paste file failed: unsupported file type/);
      expect(send).not.toHaveBeenCalled();
    } finally { cleanup(); }
  });

  it('toasts warning when drop event has no files', async () => {
    const { host, send, cleanup } = setup();
    try {
      dropItem(host, []);
      await vi.waitFor(() => expect(toastMock).toHaveBeenCalledOnce());
      expect(toastMock.mock.calls[0][0]).toMatch(/Drop: no file found/);
      expect(pasteFileMock).not.toHaveBeenCalled();
      expect(send).not.toHaveBeenCalled();
    } finally { cleanup(); }
  });
});
