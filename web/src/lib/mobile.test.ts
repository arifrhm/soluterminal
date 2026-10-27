import { afterEach, describe, expect, it, vi } from 'vitest';
import { isMobileViewport, isMobileLandscapeViewport, isProbablyMobileDevice, syncVisualViewportCssVars } from './mobile';

function setViewport({
  innerWidth,
  innerHeight,
  visualWidth,
  visualHeight,
  offsetTop = 0,
  offsetLeft = 0,
  coarse = true,
  touchPoints = 5,
}: {
  innerWidth: number;
  innerHeight: number;
  visualWidth?: number;
  visualHeight?: number;
  offsetTop?: number;
  offsetLeft?: number;
  coarse?: boolean;
  touchPoints?: number;
}) {
  Object.defineProperty(window, 'innerWidth', { configurable: true, value: innerWidth });
  Object.defineProperty(window, 'innerHeight', { configurable: true, value: innerHeight });
  Object.defineProperty(window, 'visualViewport', {
    configurable: true,
    value: {
      width: visualWidth ?? innerWidth,
      height: visualHeight ?? innerHeight,
      offsetTop,
      offsetLeft,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    },
  });
  Object.defineProperty(navigator, 'maxTouchPoints', { configurable: true, value: touchPoints });
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: vi.fn((query: string) => ({
      matches: query === '(pointer: coarse)' ? coarse : false,
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  });
}

describe('mobile viewport helpers', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    document.documentElement.removeAttribute('style');
  });

  it('keeps mobile layout when a foreign Tauri browser hosts the page', () => {
    setViewport({ innerWidth: 700, innerHeight: 900, coarse: false, touchPoints: 0 });
    vi.stubGlobal('__SOLU_TAURI__', undefined);
    vi.stubGlobal('__TAURI_INTERNALS__', { invoke: vi.fn() });
    vi.stubGlobal('__TAURI__', {});
    vi.stubGlobal('__TAURI_METADATA__', {});
    expect(isMobileViewport()).toBe(true);
    setViewport({ innerWidth: 1200, innerHeight: 900, coarse: false, touchPoints: 0 });
    expect(isMobileViewport()).toBe(false);
  });

  it('keeps a genuine SoluTerminal desktop window in desktop layout at narrow widths', () => {
    setViewport({ innerWidth: 700, innerHeight: 900, coarse: false, touchPoints: 0 });
    vi.stubGlobal('__SOLU_TAURI__', true);
    expect(isMobileViewport()).toBe(false);
  });

  it('does not treat a portrait phone with an open keyboard as landscape', () => {
    setViewport({
      innerWidth: 390,
      innerHeight: 844,
      visualWidth: 390,
      visualHeight: 320,
    });

    expect(isMobileLandscapeViewport()).toBe(false);
  });

  it('still detects true phone landscape from the layout viewport', () => {
    setViewport({
      innerWidth: 844,
      innerHeight: 390,
      visualWidth: 844,
      visualHeight: 390,
    });

    expect(isMobileLandscapeViewport()).toBe(true);
  });

  it('requires touch or coarse pointer before hiding desktop-only mobile affordances', () => {
    setViewport({
      innerWidth: 500,
      innerHeight: 900,
      coarse: false,
      touchPoints: 0,
    });

    expect(isProbablyMobileDevice()).toBe(false);
  });

  it('syncs visual viewport CSS vars including keyboard inset', () => {
    setViewport({
      innerWidth: 390,
      innerHeight: 844,
      visualWidth: 390,
      visualHeight: 524,
      offsetTop: 0,
      offsetLeft: 2,
    });

    syncVisualViewportCssVars();

    const style = document.documentElement.style;
    expect(style.getPropertyValue('--vvw')).toBe('390px');
    expect(style.getPropertyValue('--vvh')).toBe('524px');
    expect(style.getPropertyValue('--vv-offset-left')).toBe('2px');
    expect(style.getPropertyValue('--keyboard-inset')).toBe('320px');
  });
});
