import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { WelcomeOverlay } from './WelcomeOverlay';
import { SetupChecklist } from './SetupChecklist';

vi.mock('../api', async (importOriginal) => ({ ...(await importOriginal<typeof import('../api')>()), api: {
  health: vi.fn().mockResolvedValue({backendID:'host'}),
  claudeRecheck: vi.fn().mockRejectedValue(new Error('unavailable')),
  museRecheck: vi.fn().mockRejectedValue(new Error('unavailable')),
  codexRecheck: vi.fn().mockRejectedValue(new Error('unavailable')),
} }));
vi.mock('../cloudPair', () => ({ isDesktopShell: () => false, usePhoneAccessStatus: () => ({status:null}) }));
vi.mock('../claudeBootstrap', () => ({
  useClaudeInstall: () => ({installing:false, pendingInstall:null}), ClaudeInstallConfirmCard: () => null,
}));

beforeEach(() => {
  // jsdom has no layout; make visible controls participate in the real trap.
  vi.spyOn(HTMLElement.prototype, 'offsetParent', 'get').mockImplementation(function(this: HTMLElement) { return this.parentElement; });
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

describe.each([
  ['Welcome to SoluTerminal', WelcomeOverlay],
  ['Setup checklist', SetupChecklist],
] as const)('%s dialog', (name, Component) => {
  it('moves focus inside, wraps Tab, closes with Escape and restores the opener', async () => {
    const opener = document.createElement('button');
    document.body.append(opener); opener.focus();
    const close = vi.fn();
    const {rerender, unmount} = render(<Component open onClose={close} />);
    const dialog = screen.getByRole('dialog', {name});
    expect(dialog).toHaveAttribute('aria-modal', 'true');
    expect(dialog.contains(document.activeElement)).toBe(true);
    const buttons = Array.from(dialog.querySelectorAll<HTMLButtonElement>('button:not(:disabled)'));
    buttons.at(-1)!.focus(); fireEvent.keyDown(document.activeElement!, {key:'Tab'});
    expect(document.activeElement).toBe(buttons[0]);
    fireEvent.keyDown(document.activeElement!, {key:'Tab', shiftKey:true});
    expect(document.activeElement).toBe(buttons.at(-1));
    fireEvent.keyDown(document.activeElement!, {key:'Escape'});
    expect(close).toHaveBeenCalledOnce();
    rerender(<Component open={false} onClose={close} />);
    await waitFor(() => expect(document.activeElement).toBe(opener));
    unmount(); opener.remove();
  });
});

it('Welcome points at the first agent and lets each focused button own Enter', () => {
  const close = vi.fn();
  render(<WelcomeOverlay open onClose={close} />);
  // Slide 1 leads with what it does; Back only appears once there is a slide to go back to.
  expect(screen.queryByRole('button', {name:'Back'})).toBeNull();
  fireEvent.click(screen.getByRole('button', {name:'Next'}));
  expect(screen.getByText('Start Claude Code')).toBeVisible();
  fireEvent.click(screen.getByRole('button', {name:'Next'}));
  expect(screen.queryByText(/Restarting your computer/)).toBeNull();
  fireEvent.click(screen.getByRole('button', {name:'Back'}));
  const back = screen.getByRole('button', {name:'Back'});
  back.focus();
  fireEvent.keyDown(back, {key:'Enter'});
  // jsdom does not synthesize the browser's button click. The key must reach
  // that default action instead of a global Enter listener advancing the tour.
  expect(screen.getByRole('dialog', {name:'Start your first agent'})).toBeVisible();
  fireEvent.click(back);
  expect(screen.getByRole('dialog', {name:'Welcome to SoluTerminal'})).toBeVisible();
  expect(close).not.toHaveBeenCalled();
});
