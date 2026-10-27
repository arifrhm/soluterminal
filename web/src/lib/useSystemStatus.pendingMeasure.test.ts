import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { SystemStatus } from '../api';
import { useStore } from '../store';
import { refreshSystemStatus, resetSystemStatusForTests } from './useSystemStatus';

const mocks = vi.hoisted(() => ({ systemStatus: vi.fn() }));

vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>();
  return { ...actual, api: { ...actual.api, systemStatus: mocks.systemStatus } };
});

const status = { sampledAt: '2026-01-01T00:00:00Z', level: { overall: 'ok' } } as SystemStatus;

beforeEach(() => {
  resetSystemStatusForTests();
  mocks.systemStatus.mockReset();
  useStore.setState({ updateStatus: null });
});

afterEach(() => resetSystemStatusForTests());

describe('useSystemStatus pending measurements', () => {
  it('retries with measure=1 when measurement is requested during a plain poll', async () => {
    let resolvePlain: (value: SystemStatus) => void = () => {};
    mocks.systemStatus
      .mockImplementationOnce(() => new Promise<SystemStatus>((resolve) => { resolvePlain = resolve; }))
      .mockResolvedValueOnce(status);

    const plainPoll = refreshSystemStatus();
    await refreshSystemStatus({ measure: true });

    expect(mocks.systemStatus).toHaveBeenCalledTimes(1);
    expect(mocks.systemStatus).toHaveBeenNthCalledWith(1, undefined);

    resolvePlain(status);
    await plainPoll;
    await vi.waitFor(() => expect(mocks.systemStatus).toHaveBeenCalledTimes(2));
    expect(mocks.systemStatus).toHaveBeenNthCalledWith(2, { measure: true });
  });
});
