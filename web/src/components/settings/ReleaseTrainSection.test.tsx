import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { ReleaseTrainStatus, WorkflowSettings } from '../../api';

const mocks = vi.hoisted(() => ({
  getSettings: vi.fn<() => Promise<WorkflowSettings>>(),
  setSettings: vi.fn<(next: WorkflowSettings) => Promise<WorkflowSettings>>(),
  getTrain: vi.fn<(refresh?: boolean) => Promise<ReleaseTrainStatus>>(),
  vetoTrain: vi.fn<(note?: string) => Promise<ReleaseTrainStatus>>(),
}));

vi.mock('../../api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api')>();
  return {
    ...real,
    api: {
      ...real.api,
      getWorkflowSettings: mocks.getSettings,
      setWorkflowSettings: mocks.setSettings,
      getReleaseTrain: mocks.getTrain,
      vetoReleaseTrain: mocks.vetoTrain,
    },
  };
});

import { ApiError } from '../../api';
import { ReleaseTrainSection } from './ReleaseTrainSection';

let current: WorkflowSettings;

function status(overrides: Partial<ReleaseTrainStatus> = {}): ReleaseTrainStatus {
  return {
    policy: {
      enabled: false, minFunctionalLandings: 6, maxUnreleasedAgeHours: 12,
      quietRequired: true, notifyVetoMinutes: 10,
    },
    state: { phase: 'idle', train: null, parked: null, restartPending: null },
    preview: {
      evaluatedAt: '2026-08-15T12:00:00Z', ref: 'origin/main', headSha: 'abc1234',
      version: '0.0.991', lastReleaseVersion: '0.0.990',
      functionalLandings: 3, minFunctionalLandings: 6,
      oldestFunctionalAt: '2026-08-15T09:00:00Z', ageHours: 3, maxAgeHours: 12,
      engine: { running: 1, pending: 0, awaitingApproval: 0, quiet: false },
      eligible: false,
      reasons: ['3 of 6 functional landings and oldest 3.0h of 12h'],
    },
    events: [],
    ...overrides,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  current = { resources: {maxAgentRuns:3} };
  mocks.getSettings.mockImplementation(async () => structuredClone(current));
  mocks.setSettings.mockImplementation(async (next) => {
    current = structuredClone(next);
    return structuredClone(current);
  });
  mocks.getTrain.mockImplementation(async () => status());
  mocks.vetoTrain.mockImplementation(async () => status({
    state: { phase: 'parked', train: null, parked: { reason: 'vetoed by tasks-ui', head: 'abc1234', at: '2026-08-15T12:05:00Z' }, restartPending: null },
  }));
});

describe('ReleaseTrainSection', () => {
  it('renders the documented defaults when no block has been saved', async () => {
    render(<ReleaseTrainSection />);
    const toggle = await screen.findByRole('checkbox', { name: /Auto-publish on cadence/ });
    expect(toggle).not.toBeChecked();
    expect(screen.getByRole('spinbutton', { name: /Functional landings before a train/ })).toHaveValue(6);
    expect(screen.getByRole('spinbutton', { name: /Max unreleased age/ })).toHaveValue(12);
    expect(screen.getByRole('spinbutton', { name: /Veto window/ })).toHaveValue(10);
  });

  it('enables the policy without disturbing other settings blocks', async () => {
    render(<ReleaseTrainSection />);
    const toggle = await screen.findByRole('checkbox', { name: /Auto-publish on cadence/ });
    fireEvent.click(toggle);
    await waitFor(() => expect(mocks.setSettings).toHaveBeenCalled());
    const sent = mocks.setSettings.mock.calls[0][0];
    expect(sent.releaseTrain).toEqual({ enabled: true });
    expect(sent.resources).toEqual({ maxAgentRuns:3 });
  });

  it('commits a threshold on blur as a number', async () => {
    render(<ReleaseTrainSection />);
    const input = await screen.findByRole('spinbutton', { name: /Functional landings before a train/ });
    fireEvent.change(input, { target: { value: '9' } });
    fireEvent.blur(input);
    await waitFor(() => expect(mocks.setSettings).toHaveBeenCalled());
    expect(mocks.setSettings.mock.calls[0][0].releaseTrain).toEqual({ minFunctionalLandings: 9 });
  });

  it('shows the live preview and why no train is departing', async () => {
    render(<ReleaseTrainSection />);
    expect(await screen.findByText(/v0\.0\.991 · 3\/6 functional landings/)).toBeInTheDocument();
    expect(screen.getByText('3 of 6 functional landings and oldest 3.0h of 12h')).toBeInTheDocument();
    // The first read forces a synchronous re-evaluation on the daemon.
    expect(mocks.getTrain).toHaveBeenCalledWith(true);
  });

  it('shows the updater gate reason while a published release waits to restart', async () => {
    mocks.getTrain.mockResolvedValue({
      ...status(),
      state: {
        phase: 'restart_pending', train: null, parked: null,
        restartPending: { version: '0.0.1046', since: '2026-09-02T08:45:00Z' },
      },
      preview: {
        ...status().preview!,
        engine: { running: 5, pending: 15, awaitingApproval: 2, quiet: false },
        restartNote: 'waiting for 2 in-flight workflow invocations to finish (drain deadline in 8m0s)',
      },
    });

    render(<ReleaseTrainSection />);

    expect(await screen.findByText(/Restart pending v0\.0\.1046/)).toHaveTextContent(
      'waiting for 2 in-flight workflow invocations to finish (drain deadline in 8m0s)',
    );
    expect(screen.getAllByText(/waiting for 2 in-flight workflow invocations/)).toHaveLength(1);
    expect(screen.queryByText(/waits for zero running\/pending tasks/i)).not.toBeInTheDocument();
  });

  it('falls back to a generic retry hint when the daemon has not reported a gate yet', async () => {
    mocks.getTrain.mockResolvedValue({
      ...status(),
      state: {
        phase: 'restart_pending', train: null, parked: null,
        restartPending: { version: '0.0.1046', since: '2026-09-02T08:45:00Z' },
      },
      preview: { ...status().preview!, restartNote: undefined },
    });

    render(<ReleaseTrainSection />);

    expect(await screen.findByText(/Restart pending v0\.0\.1046/)).toHaveTextContent(
      'the updater retries automatically once in-flight work and Focus panes allow.',
    );
  });

  it('explains a publish whose supervision was lost at restart', async () => {
    mocks.getTrain.mockResolvedValue({
      ...status(),
      publishState: 'published',
      state: {
        phase: 'idle', parked: null, restartPending: null,
        train: {
          id: 'rtr_unsupervised', version: '0.0.1047', status: 'published',
          declaredAt: '2026-09-02T16:00:00Z', departAt: '2026-09-02T16:10:00Z',
          supervisionLost: true,
          publishNote: 'publish completed; supervision lost at daemon restart — reconciled at boot from daemon is running v0.0.1047',
        },
      },
    });

    render(<ReleaseTrainSection />);

    expect(await screen.findByText(/Published v0\.0\.1047/)).toHaveTextContent('supervision lost at daemon restart');
    expect(screen.queryByText(/Parked:/)).toBeNull();
  });

  it('offers a veto while a train is declared and refreshes after it', async () => {
    mocks.getTrain.mockImplementation(async () => status({
      state: {
        phase: 'declared',
        train: {
          id: 'rtr_1', version: '0.0.991', status: 'declared',
          declaredAt: '2026-08-15T12:00:00Z', departAt: '2026-08-15T12:10:00Z',
        },
        parked: null, restartPending: null,
      },
    }));
    render(<ReleaseTrainSection />);
    const veto = await screen.findByRole('button', { name: 'Veto' });
    fireEvent.click(veto);
    await waitFor(() => expect(mocks.vetoTrain).toHaveBeenCalledTimes(1));
    expect(await screen.findByText(/Parked: vetoed by tasks-ui/)).toBeInTheDocument();
  });

  it('hides itself on a daemon without the release-train endpoint', async () => {
    mocks.getTrain.mockRejectedValue(new ApiError(404, 'not available'));
    const { container } = render(<ReleaseTrainSection />);
    await waitFor(() => expect(mocks.getSettings).toHaveBeenCalled());
    await waitFor(() => expect(mocks.getTrain).toHaveBeenCalled());
    // Settings loaded and the endpoint answered 404: the group must stay gone.
    expect(screen.queryByText('Release train')).not.toBeInTheDocument();
    expect(container).toBeEmptyDOMElement();
  });
});
