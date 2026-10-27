import { render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useRef } from 'react';
import { useClampedMenuPosition } from './useClampedMenuPosition';

// jsdom has no layout, so give the menu a fake measured size and a fake
// viewport; the hook only consumes getBoundingClientRect + window.inner*.
const MENU_W = 200;
const MENU_H = 300;

function Menu({ pos, onPos }: { pos: { x: number; y: number } | null; onPos: (p: { left: number; top: number }) => void }) {
  const ref = useRef<HTMLDivElement>(null);
  const adjusted = useClampedMenuPosition(ref, pos);
  onPos(adjusted);
  return pos ? (
    <div ref={ref} role="menu" style={{ position: 'fixed', left: adjusted.left, top: adjusted.top }} />
  ) : null;
}

function renderAt(pos: { x: number; y: number }): { left: number; top: number } {
  let last = { left: -1, top: -1 };
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
    width: MENU_W,
    height: MENU_H,
    top: 0,
    left: 0,
    right: MENU_W,
    bottom: MENU_H,
    x: 0,
    y: 0,
    toJSON: () => ({}),
  } as DOMRect);
  render(<Menu pos={pos} onPos={(p) => { last = p; }} />);
  return last;
}

describe('useClampedMenuPosition', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('keeps a menu with room below at the cursor', () => {
    window.innerWidth = 1920;
    window.innerHeight = 1080;
    expect(renderAt({ x: 100, y: 100 })).toEqual({ left: 100, top: 100 });
  });

  it('flips upward when the menu would clip the bottom edge', () => {
    window.innerWidth = 1920;
    window.innerHeight = 1080;
    // 1000 + 300 > 1080 - margin → open upward: bottom edge at the cursor.
    expect(renderAt({ x: 100, y: 1000 })).toEqual({ left: 100, top: 1000 - MENU_H });
  });

  it('flips leftward when the menu would clip the right edge', () => {
    window.innerWidth = 1920;
    window.innerHeight = 1080;
    expect(renderAt({ x: 1900, y: 100 })).toEqual({ left: 1900 - MENU_W, top: 100 });
  });

  it('clamps to the margin when flipping would poke past the top', () => {
    window.innerWidth = 1920;
    window.innerHeight = 250; // shorter than the menu: flip lands above 0
    expect(renderAt({ x: 100, y: 240 }).top).toBe(8);
  });
});
