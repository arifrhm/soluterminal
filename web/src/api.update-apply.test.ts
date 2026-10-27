import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, ApiError, configureApi, setBackend } from './api';

afterEach(() => {
  vi.unstubAllGlobals();
  configureApi({ prefix: '', token: null });
  setBackend(null);
});

describe('update apply errors', () => {
  it('preserves a typed JSON 409 body on ApiError', async () => {
    const payload = {
      busy: true,
      cause: 'workflow',
      reason: 'waiting for 2 in-flight workflow invocations',
      workflowInflight: 2,
      workflowDeferDeadlineMs: 123456,
    };
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(payload), {
      status: 409,
      headers: { 'content-type': 'application/json' },
    })));

    const error = await api.applyUpdate(false).catch((caught: unknown) => caught);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({ status: 409, body: payload });
  });

  it('leaves body undefined for a plain-text legacy error', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('busy', { status: 409 })));
    const error = await api.applyUpdate(false).catch((caught: unknown) => caught);
    expect(error).toMatchObject({ status: 409, body: undefined });
  });
});
