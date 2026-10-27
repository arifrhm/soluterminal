import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, configureApi, setBackend } from './api';

afterEach(() => {
  vi.unstubAllGlobals();
  configureApi({ prefix: '', token: null });
  setBackend(null);
});

describe('workflow settings revisions', () => {
  it('carries the reviewed GET revision in the PUT without adding settings fields', async () => {
    const settings = { resources: {maxAgentRuns:2} };
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => new Response(JSON.stringify(settings), {
      status: 200, headers: { 'content-type': 'application/json', ETag: init?.method === 'PUT' ? '"saved"' : '"reviewed"' },
    }));
    vi.stubGlobal('fetch', fetchMock);
    const current = await api.getWorkflowSettings();
    await api.setWorkflowSettings({ ...current, resources: {maxAgentRuns:3} }, current);
    const request = fetchMock.mock.calls[1][1];
    expect(new Headers(request?.headers).get('If-Match')).toBe('"reviewed"');
    expect(JSON.parse(String(request?.body))).toEqual({ resources: {maxAgentRuns:3} });
  });
  it('reports a concurrent write rejection instead of presenting the draft as saved', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'workflow settings changed' }), {
      status: 412, headers: { 'content-type': 'application/json' },
    })));
    await expect(api.setWorkflowSettings({ resources: {maxAgentRuns:2} })).rejects.toMatchObject({ status: 412 });
  });
});
