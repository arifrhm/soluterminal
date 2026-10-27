import { act, renderHook } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Terminal } from '@xterm/xterm';
import { usePaneReaderMode } from './usePaneReaderMode';

// Minimal xterm stand-in. jsdom has no canvas/layout, so the hook is driven
// through its event subscriptions and the buffer object it reads directly.
function makeTerminal() {
  const listeners: Record<'scroll' | 'lineFeed' | 'writeParsed' | 'render', Array<() => void>> = {
    scroll: [],
    lineFeed: [],
    writeParsed: [],
    render: [],
  };
  const buffer = { viewportY: 100, baseY: 100, length: 200 };
  const bufferService = { isUserScrolling: false };
  const element = document.createElement('div');
  const viewport = document.createElement('div');
  viewport.className = 'xterm-viewport';
  element.appendChild(viewport);

  const on = (key: keyof typeof listeners) => (cb: () => void) => {
    listeners[key].push(cb);
    return { dispose: () => { listeners[key] = listeners[key].filter((x) => x !== cb); } };
  };

  const term = {
    rows: 20,
    get buffer() {
      return { active: buffer };
    },
    element,
    focus: vi.fn(),
    scrollToBottom: vi.fn(() => {
      buffer.viewportY = buffer.baseY;
      for (const cb of [...listeners.scroll]) cb();
    }),
    onScroll: on('scroll'),
    onLineFeed: on('lineFeed'),
    onWriteParsed: on('writeParsed'),
    onRender: on('render'),
    _core: { _bufferService: bufferService },
  };
  return { term: term as unknown as Terminal, buffer, bufferService, element, viewport, listeners };
}

type Harness = ReturnType<typeof makeTerminal>;

async function attach(term: Terminal, actionGuard?:()=>boolean) {
  const termRef = { current: term };
  const rendered = renderHook(() => usePaneReaderMode({ paneId: 'pane-1', termRef, actionGuard }));
  let cleanup: (() => void) | undefined;
  act(() => {
    cleanup = rendered.result.current.attachReaderMode(term);
  });
  // Let the rAF that wires the viewport scroll listener land.
  await act(async () => {
    await new Promise((resolve) => requestAnimationFrame(() => resolve(null)));
  });
  return { ...rendered, cleanup: () => cleanup?.() };
}

function fireScroll(h: Harness) {
  act(() => {
    for (const cb of [...h.listeners.scroll]) cb();
  });
}

function fireLineFeed(h: Harness) {
  act(() => {
    for (const cb of [...h.listeners.lineFeed]) cb();
  });
}

describe('usePaneReaderMode follow-tail drift recovery', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('parked output and stale reader actions cannot move or focus the viewport',async()=>{
    const h=makeTerminal();let visible=true;const {result,cleanup}=await attach(h.term,()=>visible);
    try{
      act(()=>h.element.dispatchEvent(new WheelEvent('wheel',{deltaY:-120,bubbles:true})));h.buffer.viewportY=80;fireScroll(h);expect(result.current.scrolledAway).toBe(true);
      visible=false;fireLineFeed(h);act(()=>{for(const cb of h.listeners.writeParsed)cb();for(const cb of h.listeners.render)cb();result.current.jumpToLive();});
      expect(result.current.newLinesCount).toBe(0);expect(h.buffer.viewportY).toBe(80);expect(h.term.focus).not.toHaveBeenCalled();
      visible=true;act(()=>result.current.jumpToLive());expect(h.buffer.viewportY).toBe(100);expect(h.term.focus).toHaveBeenCalledOnce();
    }finally{cleanup();}
  });

  it('undoes a reflow drift instead of latching the reader away', async () => {
    const h = makeTerminal();
    const { result, cleanup } = await attach(h.term);
    try {
      // A browser scroll-anchor adjustment moved the viewport up with no user
      // gesture behind it. The hook must re-pin to the tail, not show the chip.
      h.buffer.viewportY = 80;
      fireScroll(h);

      expect(h.term.scrollToBottom).toHaveBeenCalled();
      expect(h.bufferService.isUserScrolling).toBe(false);
      expect(result.current.scrolledAway).toBe(false);
      expect(h.buffer.viewportY).toBe(h.buffer.baseY);
    } finally {
      cleanup();
    }
  });

  it('does not treat a click into the transcript as scroll intent', async () => {
    const h = makeTerminal();
    const { result, cleanup } = await attach(h.term);
    try {
      // A plain click (pointerdown/up on the terminal element, not the
      // scrollbar) must not latch the reader away from the tail.
      act(() => {
        h.element.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }));
        document.dispatchEvent(new PointerEvent('pointerup', { bubbles: true }));
      });
      h.buffer.viewportY = 80;
      fireScroll(h);
      expect(result.current.scrolledAway).toBe(false);
      expect(h.buffer.viewportY).toBe(h.buffer.baseY);
    } finally {
      cleanup();
    }
  });

  it('honors a scrollbar drag (viewport pointerdown) as user intent', async () => {
    const h = makeTerminal();
    const { result, cleanup } = await attach(h.term);
    try {
      act(() => {
        h.viewport.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }));
      });
      h.buffer.viewportY = 80;
      fireScroll(h);
      expect(result.current.scrolledAway).toBe(true);
      act(() => {
        document.dispatchEvent(new PointerEvent('pointerup', { bubbles: true }));
      });
    } finally {
      cleanup();
    }
  });

  it('honors a real wheel scroll-away and counts new lines below the fold', async () => {
    const h = makeTerminal();
    const { result, cleanup } = await attach(h.term);
    try {
      act(() => {
        h.element.dispatchEvent(new WheelEvent('wheel', { deltaY: -120, bubbles: true }));
      });
      h.buffer.viewportY = 80;
      fireScroll(h);
      expect(result.current.scrolledAway).toBe(true);

      fireLineFeed(h);
      fireLineFeed(h);
      expect(result.current.newLinesCount).toBe(2);
    } finally {
      cleanup();
    }
  });

  it('jumpToLive returns to the tail and clears the unread count', async () => {
    const h = makeTerminal();
    const { result, cleanup } = await attach(h.term);
    try {
      act(() => {
        h.element.dispatchEvent(new WheelEvent('wheel', { deltaY: -120, bubbles: true }));
      });
      h.buffer.viewportY = 80;
      fireScroll(h);
      fireLineFeed(h);
      expect(result.current.scrolledAway).toBe(true);
      expect(result.current.newLinesCount).toBe(1);

      act(() => {
        result.current.jumpToLive();
      });
      expect(result.current.scrolledAway).toBe(false);
      expect(result.current.newLinesCount).toBe(0);
      expect(h.buffer.viewportY).toBe(h.buffer.baseY);
    } finally {
      cleanup();
    }
  });
});
