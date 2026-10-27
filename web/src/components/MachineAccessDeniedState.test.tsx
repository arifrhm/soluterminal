import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { Session } from '@supabase/supabase-js';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useCloud, type CloudMachine } from '../cloud';
import { reportMachineScopeStatus, resetMachineAccess } from '../lib/machineAccess';
import { CloudGate } from './CloudGate';
import { MachineAccessDeniedState } from './MachineAccessDeniedState';

const { retryMachineAccessMock } = vi.hoisted(() => ({
  retryMachineAccessMock: vi.fn(),
}));

vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>();
  return { ...actual, retryMachineAccess: retryMachineAccessMock };
});

const initialCloudState = useCloud.getInitialState();
const machine: CloudMachine = {
  id: 'mach_x',
  name: 'Personal PC',
  online: true,
  backends: ['host'],
};

beforeEach(async () => {
  resetMachineAccess();
  retryMachineAccessMock.mockReset();
  retryMachineAccessMock.mockResolvedValue(new Response('{}', { status: 200 }));
  useCloud.setState({ machines: [machine], selectedMachineId: machine.id });
  vi.stubGlobal('fetch', vi.fn(async () => new Response('[]', { status: 200 })));
  await reportMachineScopeStatus(401, { machineId: machine.id, token: 'jwt-token' });
  await reportMachineScopeStatus(401, { machineId: machine.id, token: 'jwt-token' });
  await reportMachineScopeStatus(401, { machineId: machine.id, token: 'jwt-token' });
});

afterEach(() => {
  useCloud.setState(initialCloudState, true);
  resetMachineAccess();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('MachineAccessDeniedState', () => {
  it('names the selected machine and explains that polling is paused', () => {
    render(<MachineAccessDeniedState />);

    expect(screen.getByRole('heading', { name: /you don't have access to personal pc/i })).toBeInTheDocument();
    expect(screen.getByText(/polling is paused/i)).toBeInTheDocument();
  });

  it('uses the existing deselect action to switch machines', () => {
    const deselectMachine = vi.fn();
    useCloud.setState({ deselectMachine });
    render(<MachineAccessDeniedState />);

    fireEvent.click(screen.getByRole('button', { name: /switch machine/i }));

    expect(deselectMachine).toHaveBeenCalledOnce();
  });

  it('shows the existing machine picker after switching', async () => {
    useCloud.setState({
      mode: 'cloud',
      isLocal: false,
      session: { access_token: 'jwt-token' } as Session,
      authorised: true,
      bootstrap: vi.fn(async () => {}),
      deselectMachine: initialCloudState.deselectMachine,
    });
    render(
      <CloudGate>
        <MachineAccessDeniedState />
      </CloudGate>,
    );

    fireEvent.click(screen.getByRole('button', { name: /switch machine/i }));

    expect(await screen.findByRole('heading', { name: /your machines/i })).toBeInTheDocument();
  });

  it('runs Retry with a disabled pending state', async () => {
    let finishRetry!: () => void;
    retryMachineAccessMock.mockImplementationOnce(() => new Promise<Response>((resolve) => {
      finishRetry = () => resolve(new Response('{}', { status: 200 }));
    }));
    render(<MachineAccessDeniedState />);

    fireEvent.click(screen.getByRole('button', { name: /^retry$/i }));

    await waitFor(() => expect(screen.getByRole('button', { name: /retrying/i })).toBeDisabled());
    expect(retryMachineAccessMock).toHaveBeenCalledOnce();

    await act(async () => { finishRetry(); });
    await waitFor(() => expect(screen.getByRole('button', { name: /^retry$/i })).toBeEnabled());
  });
});
