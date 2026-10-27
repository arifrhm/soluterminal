import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import type { WorkflowSettings } from '../../api';
import { ResourceLimitsSection } from './ResourceLimitsSection';
import { ModelGatewaySection } from './ModelGatewaySection';

const settings = (over: Partial<WorkflowSettings> = {}) => ({ resources: { maxAgentRuns: 3 }, localOpenAI: { baseUrl: 'http://a', model: 'm1' }, ...over }) as WorkflowSettings;

function updater(fail = false) {
  let current = settings();
  const fn = vi.fn(async (change: (s: WorkflowSettings) => WorkflowSettings) => {
    if (fail) throw new Error('daemon said no');
    current = change(current);
  });
  return { fn, get current() { return current; } };
}

describe('Resource limits', () => {
  it('rejects an out-of-range value, shows why and puts the saved value back', () => {
    const u = updater();
    render(<ResourceLimitsSection sharedSettings={settings()} updateSharedSettings={u.fn as never} />);
    const box = screen.getByLabelText('Max agent runs') as HTMLInputElement;
    fireEvent.change(box, { target: { value: '0' } });
    fireEvent.blur(box);
    expect(u.fn).not.toHaveBeenCalled();
    expect(box.value).toBe('3');
    expect(screen.getByRole('alert').textContent).toMatch(/from 1 to 8; kept 3/);
  });

  it('saves only an actual edit, and a failed save shows the saved value again', async () => {
    const u = updater(true);
    render(<ResourceLimitsSection sharedSettings={settings()} updateSharedSettings={u.fn as never} />);
    const box = screen.getByLabelText('Max agent runs') as HTMLInputElement;
    fireEvent.blur(box);
    expect(u.fn).not.toHaveBeenCalled();
    fireEvent.change(box, { target: { value: '5' } });
    fireEvent.blur(box);
    await waitFor(() => expect(box.value).toBe('3'));
    expect(screen.getByRole('alert').textContent).toMatch(/daemon said no/);
  });

  it('shows a value changed elsewhere', () => {
    const u = updater();
    const { rerender } = render(<ResourceLimitsSection sharedSettings={settings()} updateSharedSettings={u.fn as never} />);
    rerender(<ResourceLimitsSection sharedSettings={settings({ resources: { maxAgentRuns: 6 } })} updateSharedSettings={u.fn as never} />);
    expect((screen.getByLabelText('Max agent runs') as HTMLInputElement).value).toBe('6');
  });

  it('opt-ins can follow the environment or be turned on or off here', async () => {
    const u = updater();
    render(<ResourceLimitsSection sharedSettings={settings()} updateSharedSettings={u.fn as never} />);
    const select = screen.getByLabelText('Rotate WSL crash dumps') as HTMLSelectElement;
    expect(select.value).toBe('default');
    fireEvent.change(select, { target: { value: 'off' } });
    await waitFor(() => expect(u.current.resources?.rotateWSLCrashDumps).toBe(false));
    fireEvent.change(select, { target: { value: 'default' } });
    await waitFor(() => expect(u.current.resources?.rotateWSLCrashDumps).toBeUndefined());
  });
});

describe('Local model gateway', () => {
  it('never overwrites a newer value with an untouched box', () => {
    const u = updater();
    const { rerender } = render(<ModelGatewaySection settings={settings()} update={u.fn as never} />);
    rerender(<ModelGatewaySection settings={settings({ localOpenAI: { baseUrl: 'http://a', model: 'm2' } })} update={u.fn as never} />);
    const model = screen.getByLabelText('Local OpenAI model') as HTMLInputElement;
    expect(model.value).toBe('m2');
    fireEvent.focus(model);
    fireEvent.blur(model);
    expect(u.fn).not.toHaveBeenCalled();
  });
});
