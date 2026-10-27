import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  api,
  ApiError,
  configureApi,
  noteUiEventsSseFailure,
  retryMachineAccess,
  setBackend,
} from './api';
import {
  getMachineAccessState,
  MACHINE_ACCESS_PROBE_INTERVAL_MS,
  resetMachineAccess,
} from './lib/machineAccess';

function response(status: number): Response {
  return new Response(status >= 200 && status < 300 ? '{}' : 'denied', {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

function machineFetch(machineStatuses: number[], accountStatus = 200) {
  let machineIndex = 0;
  const fetchMock = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
    if (String(input) === '/api/machines') return response(accountStatus);
    return response(machineStatuses[machineIndex++] ?? 200);
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

async function failHealth(times: number): Promise<void> {
  for (let i = 0; i < times; i += 1) {
    await expect(api.health()).rejects.toBeInstanceOf(ApiError);
  }
}

beforeEach(() => {
  vi.useFakeTimers();
  window.history.replaceState(null, '', '/');
  configureApi({ prefix: '', token: null });
  setBackend(null);
  resetMachineAccess();
});

afterEach(() => {
  configureApi({ prefix: '', token: null });
  setBackend(null);
  resetMachineAccess();
  vi.clearAllTimers();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('machine access classification', () => {
  it('blocks after three machine 401s when account scope remains healthy', async () => {
    const fetchMock = machineFetch([401, 401, 401], 200);
    configureApi({ prefix: '/m/mach_x', token: 'jwt-token' });

    await failHealth(3);

    expect(getMachineAccessState()).toEqual({
      blocked: true,
      machineId: 'mach_x',
      checking: false,
    });
    expect(fetchMock).toHaveBeenCalledTimes(4);
    expect(fetchMock.mock.calls[3]?.[0]).toBe('/api/machines');
    expect(new Headers(fetchMock.mock.calls[3]?.[1]?.headers).get('Authorization')).toBe('Bearer jwt-token');
  });

  it('does not block when account scope is also unauthorized', async () => {
    machineFetch([401, 401, 401], 401);
    configureApi({ prefix: '/m/mach_x', token: 'expired-token' });

    await failHealth(3);

    expect(getMachineAccessState().blocked).toBe(false);
    expect(getMachineAccessState().checking).toBe(false);
  });

  it('resets the consecutive count on a non-401 machine response', async () => {
    const fetchMock = machineFetch([401, 401, 200, 401]);
    configureApi({ prefix: '/m/mach_x', token: 'jwt-token' });

    await failHealth(2);
    await expect(api.health()).resolves.toEqual({});
    await failHealth(1);

    expect(getMachineAccessState().blocked).toBe(false);
    expect(fetchMock).toHaveBeenCalledTimes(4);
    expect(fetchMock.mock.calls.some((call) => call[0] === '/api/machines')).toBe(false);
  });

  it('fast-rejects machine requests without network I/O while blocked', async () => {
    const fetchMock = machineFetch([401, 401, 401]);
    configureApi({ prefix: '/m/mach_x', token: 'jwt-token' });
    await failHealth(3);
    const callsAtBlock = fetchMock.mock.calls.length;

    await expect(api.health()).rejects.toMatchObject({
      status: 401,
      message: 'no access to this machine (client-blocked)',
    });

    expect(fetchMock).toHaveBeenCalledTimes(callsAtBlock);
  });

  it('allows one periodic probe and recovers on its non-401 response', async () => {
    const fetchMock = machineFetch([401, 401, 401, 200, 200]);
    configureApi({ prefix: '/m/mach_x', token: 'jwt-token' });
    await failHealth(3);
    const callsAtBlock = fetchMock.mock.calls.length;

    await expect(api.health()).rejects.toMatchObject({ status: 401 });
    expect(fetchMock).toHaveBeenCalledTimes(callsAtBlock);

    await vi.advanceTimersByTimeAsync(MACHINE_ACCESS_PROBE_INTERVAL_MS);
    await expect(api.health()).resolves.toEqual({});
    expect(fetchMock).toHaveBeenCalledTimes(callsAtBlock + 1);
    expect(getMachineAccessState().blocked).toBe(false);

    await expect(api.health()).resolves.toEqual({});
    expect(fetchMock).toHaveBeenCalledTimes(callsAtBlock + 2);
  });

  it('resets immediately when the machine prefix changes', async () => {
    machineFetch([401, 401, 401]);
    configureApi({ prefix: '/m/mach_x', token: 'jwt-token' });
    await failHealth(3);

    configureApi({ prefix: '/m/mach_y' });

    expect(getMachineAccessState()).toEqual({ blocked: false, machineId: null, checking: false });
  });

  it('resets immediately when the token value changes', async () => {
    machineFetch([401, 401, 401]);
    configureApi({ prefix: '/m/mach_x', token: 'jwt-token' });
    await failHealth(3);

    configureApi({ token: 'refreshed-jwt' });

    expect(getMachineAccessState()).toEqual({ blocked: false, machineId: null, checking: false });
  });

  it('never classifies or gates unprefixed local requests', async () => {
    const fetchMock = machineFetch([401, 401, 401, 401]);

    await failHealth(4);

    expect(getMachineAccessState().blocked).toBe(false);
    expect(fetchMock).toHaveBeenCalledTimes(4);
    expect(fetchMock.mock.calls.some((call) => call[0] === '/api/machines')).toBe(false);
  });

  it('Retry forces an immediate probe and clears the block on success', async () => {
    const fetchMock = machineFetch([401, 401, 401, 200]);
    configureApi({ prefix: '/m/mach_x', token: 'jwt-token' });
    await failHealth(3);
    const callsAtBlock = fetchMock.mock.calls.length;

    await expect(retryMachineAccess()).resolves.toMatchObject({ status: 200 });

    expect(fetchMock).toHaveBeenCalledTimes(callsAtBlock + 1);
    expect(fetchMock.mock.calls.at(-1)?.[0]).toBe('/m/mach_x/api/health');
    expect(getMachineAccessState().blocked).toBe(false);
  });

  it('feeds closed UI-event streams through the same classifier', async () => {
    const fetchMock = machineFetch([401, 401, 401], 200);
    configureApi({ prefix: '/m/mach_x', token: 'jwt-token' });

    noteUiEventsSseFailure();
    await vi.advanceTimersByTimeAsync(1);
    await vi.advanceTimersByTimeAsync(10_000);
    noteUiEventsSseFailure();
    await vi.advanceTimersByTimeAsync(1);
    await vi.advanceTimersByTimeAsync(10_000);
    noteUiEventsSseFailure();
    await vi.advanceTimersByTimeAsync(1);

    expect(fetchMock).toHaveBeenCalledTimes(4);
    expect(getMachineAccessState()).toEqual({ blocked: true, machineId: 'mach_x', checking: false });
  });
});
