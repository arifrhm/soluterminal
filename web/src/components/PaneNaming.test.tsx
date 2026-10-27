import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { PaneInfo } from '../types';
import { api } from '../api';
import { PaneIdentityDialog } from './PaneIdentityDialog';
import { PanesTab } from './settings/PanesTab';

function pane(extra: Partial<PaneInfo>): PaneInfo {
  return { id: 'p1', cwd: '/tmp', cmd: ['codex'], title: 'codex', status: 'running', createdAt: '', cols: 80, rows: 24, pid: 1, lastActivityMs: 0, state: 'idle', hasClaude: false, ...extra } as PaneInfo;
}

afterEach(() => { vi.restoreAllMocks(); });

describe('AI pane names', () => {
  it('Suggest fills the name field and changes nothing until you save', async () => {
    const suggest = vi.spyOn(api, 'suggestPaneName').mockResolvedValue({ name: 'Login redirect fix' });
    render(<PaneIdentityDialog pane={pane({})} onClose={() => {}} />);
    fireEvent.click(screen.getByText('Suggest'));
    await waitFor(() => expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('Login redirect fix'));
    expect(suggest).toHaveBeenCalledWith('p1');
  });

  it('locks the name and Save while a suggestion loads', async () => {
    let resolve!: (v: { name: string }) => void;
    vi.spyOn(api, 'suggestPaneName').mockReturnValue(new Promise((r) => { resolve = r; }));
    render(<PaneIdentityDialog pane={pane({})} onClose={() => {}} />);
    fireEvent.click(screen.getByText('Suggest'));
    expect((screen.getByLabelText('Name') as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByText('Thinking…') as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(true);
    resolve({ name: 'Done' });
    await waitFor(() => expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('Done'));
    expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(false);
  });

  it('says why there is no suggestion', async () => {
    vi.spyOn(api, 'suggestPaneName').mockRejectedValue(new Error("Claude Code isn't installed or signed in, so it can't suggest a name"));
    render(<PaneIdentityDialog pane={pane({})} onClose={() => {}} />);
    fireEvent.click(screen.getByText('Suggest'));
    expect((await screen.findByRole('alert')).textContent).toMatch(/Claude Code isn't installed/);
    expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('codex');
  });

  it('the Settings toggle is off until turned on, and rolls back a failed save', async () => {
    vi.spyOn(api, 'getPaneNaming').mockResolvedValue({ autoName: false });
    const put = vi.spyOn(api, 'setPaneNaming').mockRejectedValueOnce(new Error('this setting can only be changed on the computer itself'));
    render(<PanesTab />);
    const box = await screen.findByLabelText('Name new agent panes with AI') as HTMLInputElement;
    expect(box.checked).toBe(false);
    fireEvent.click(box);
    expect(put).toHaveBeenCalledWith(true);
    await screen.findByText('this setting can only be changed on the computer itself');
    expect(box.checked).toBe(false);
  });
});
