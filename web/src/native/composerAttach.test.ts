import { describe, it, expect } from 'vitest';
import { firstImageFile, insertAttachToken, insertPlainPathToken, hasImagePayload } from './composerAttach';

// Minimal File/DataTransfer stand-ins — jsdom has File, but we build lists by
// hand (indexed object + length) to exercise both the FileList and
// DataTransferItemList probe branches without spreading an array (whose own
// `length` would clash with the explicit one).
function fileList(files: File[]): FileList {
  const obj: Record<number, File> = {};
  files.forEach((f, i) => {
    obj[i] = f;
  });
  return { ...obj, length: files.length, item: (i: number) => files[i] ?? null } as unknown as FileList;
}
function itemList(files: File[]): DataTransferItemList {
  const obj: Record<number, unknown> = {};
  files.forEach((f, i) => {
    obj[i] = { kind: 'file' as const, type: f.type, getAsFile: () => f };
  });
  return { ...obj, length: files.length } as unknown as DataTransferItemList;
}

const png = () => new File(['x'], 'a.png', { type: 'image/png' });
const txt = () => new File(['x'], 'a.txt', { type: 'text/plain' });

describe('firstImageFile', () => {
  it('returns the first image in a FileList', () => {
    expect(firstImageFile(fileList([txt(), png()]))?.type).toBe('image/png');
  });

  it('falls back to DataTransferItemList when files are empty', () => {
    expect(firstImageFile(fileList([]), itemList([png()]))?.type).toBe('image/png');
  });

  it('returns null when there is no image (text only)', () => {
    expect(firstImageFile(fileList([txt()]), itemList([txt()]))).toBeNull();
  });

  it('tolerates null/undefined inputs', () => {
    expect(firstImageFile(null, null)).toBeNull();
    expect(firstImageFile(undefined)).toBeNull();
  });
});

describe('insertAttachToken', () => {
  it('appends @path to an empty draft with no leading space', () => {
    expect(insertAttachToken('', '/p/a.png')).toBe('@/p/a.png ');
  });

  it('inserts a separating space after non-whitespace', () => {
    expect(insertAttachToken('look at', '/p/a.png')).toBe('look at @/p/a.png ');
  });

  it('does not double the space when the draft already ends in whitespace', () => {
    expect(insertAttachToken('look at ', '/p/a.png')).toBe('look at @/p/a.png ');
    expect(insertAttachToken('look at\n', '/p/a.png')).toBe('look at\n@/p/a.png ');
  });
});

describe('insertPlainPathToken', () => {
  it('appends the bare path with no @ prefix', () => {
    expect(insertPlainPathToken('', '/p/a.png')).toBe('/p/a.png ');
  });

  it('inserts a separating space after non-whitespace', () => {
    expect(insertPlainPathToken('look at', '/p/a.png')).toBe('look at /p/a.png ');
  });

  it('does not double the space when the draft already ends in whitespace', () => {
    expect(insertPlainPathToken('look at ', '/p/a.png')).toBe('look at /p/a.png ');
  });
});

describe('hasImagePayload', () => {
  it('true when an image file is present', () => {
    expect(
      hasImagePayload({
        files: fileList([png()]),
        items: itemList([png()]),
        types: [],
      } as unknown as DataTransfer),
    ).toBe(true);
  });

  it('true when only the Files type marker is set (drag-over, no file list yet)', () => {
    expect(
      hasImagePayload({
        files: fileList([]),
        items: itemList([]),
        types: ['Files'],
      } as unknown as DataTransfer),
    ).toBe(true);
  });

  it('false for a text-only payload', () => {
    expect(
      hasImagePayload({
        files: fileList([]),
        items: itemList([txt()]),
        types: ['text/plain'],
      } as unknown as DataTransfer),
    ).toBe(false);
  });

  it('false for null', () => {
    expect(hasImagePayload(null)).toBe(false);
  });
});
