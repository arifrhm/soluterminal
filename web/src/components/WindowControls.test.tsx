import { readFileSync } from 'node:fs';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { isDesktopApp } from '../tauri-browser';
import { WindowControls, isTauriShell } from './WindowControls';

afterEach(() => {
  delete (window as unknown as { __SOLU_TAURI__?: unknown }).__SOLU_TAURI__;
  delete (window as unknown as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__;
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('WindowControls', () => {
  it('uses the canonical desktop-shell predicate', () => {
    expect(isTauriShell).toBe(isDesktopApp);
  });

  it('renders nothing outside the Tauri shell', () => {
    const { container } = render(<WindowControls />);
    expect(container).toBeEmptyDOMElement();
  });

  it('renders working Tauri window controls and announces backgrounding', () => {
    const invoke = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal('__SOLU_TAURI__', true);
    vi.stubGlobal('__TAURI_INTERNALS__', { invoke });
    const backgroundReasons: string[] = [];
    const onBackground = ((event: CustomEvent<{ reason: string }>) => {
      backgroundReasons.push(event.detail.reason);
    }) as EventListener;
    window.addEventListener('solu:desktop-will-background', onBackground);

    const { container } = render(<WindowControls />);
    const controls = container.querySelector('.window-controls');
    expect(controls).toHaveAttribute('data-tauri-drag-region', 'false');

    fireEvent.click(screen.getByRole('button', { name: 'Minimize' }));
    fireEvent.click(screen.getByRole('button', { name: 'Maximize' }));
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));

    expect(invoke).toHaveBeenNthCalledWith(1, 'plugin:window|minimize');
    expect(invoke).toHaveBeenNthCalledWith(2, 'plugin:window|toggle_maximize');
    expect(invoke).toHaveBeenNthCalledWith(3, 'plugin:window|close');
    expect(backgroundReasons).toEqual(['minimize', 'close']);
    window.removeEventListener('solu:desktop-will-background', onBackground);
  });

  it('renders children before the Minimize button', () => {
    vi.stubGlobal('__SOLU_TAURI__', true);
    vi.stubGlobal('__TAURI_INTERNALS__', { invoke: vi.fn().mockResolvedValue(undefined) });

    render(<WindowControls><button type="button">New window</button></WindowControls>);

    const controls = screen.getByRole('button', { name: 'Minimize' }).parentElement!;
    expect(controls.firstElementChild).toBe(screen.getByRole('button', { name: 'New window' }));
    expect(controls.children[1]).toBe(screen.getByRole('button', { name: 'Minimize' }));
  });

  it('keeps window-control styles host-scoped and stronger than generic button hover', () => {
    const styles = readFileSync('src/styles.css', 'utf8');
    expect(styles).toContain('.topbar .window-controls');
    expect(styles).toContain('.topbar .window-control:hover');
    expect(styles).toContain('.topbar .window-control-close:hover');
    expect(styles).not.toMatch(/(^|\n)\.window-control\s*\{/);

  });

  it('swallows rejected window commands while preserving background intent', async () => {
    const invoke = vi.fn().mockRejectedValue(new Error('window command rejected'));
    vi.stubGlobal('__SOLU_TAURI__', true);
    vi.stubGlobal('__TAURI_INTERNALS__', { invoke });
    const backgroundReasons: string[] = [];
    const onBackground = ((event: CustomEvent<{ reason: string }>) => {
      backgroundReasons.push(event.detail.reason);
    }) as EventListener;
    window.addEventListener('solu:desktop-will-background', onBackground);

    render(<WindowControls />);
    fireEvent.click(screen.getByRole('button', { name: 'Minimize' }));
    fireEvent.click(screen.getByRole('button', { name: 'Maximize' }));
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));

    await waitFor(() => expect(invoke).toHaveBeenCalledTimes(3));
    expect(backgroundReasons).toEqual(['minimize', 'close']);
    window.removeEventListener('solu:desktop-will-background', onBackground);
  });
});
