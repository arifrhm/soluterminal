import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  api,
  ApiError,
  configureApi,
  setBackend,
  ccUsagePath,
  ccUsageTeamPath,
  ccUsageMemberPath,
  usageTimeZone,
} from './api';
import type { CCUsageSummary } from './api';

// A minimal Headers-like stub so usageFetch's r.headers?.get(...) (stale +
// Retry-After sniffing) has something to read on a mocked Response.
function hdrs(map: Record<string, string> = {}) {
  const lower: Record<string, string> = {};
  for (const k of Object.keys(map)) lower[k.toLowerCase()] = map[k]!;
  return { get: (k: string) => lower[k.toLowerCase()] ?? null };
}

// Capture the URL + headers each call constructs, returning an OK empty-ish JSON
// body. Mirrors the fetch-spy pattern in cloud.test.ts / BrowserPane.test.tsx.
function spyOkFetch() {
  return vi.spyOn(globalThis, 'fetch').mockResolvedValue({
    ok: true,
    status: 200,
    headers: hdrs(),
    json: async () => ({}),
  } as unknown as Response);
}

// Build a Response-like stub for the retry tests. `ok` is derived from status.
function res(status: number, opts: { body?: string; headers?: Record<string, string>; json?: unknown } = {}) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: '',
    headers: hdrs(opts.headers),
    text: async () => opts.body ?? '',
    json: async () => opts.json ?? {},
  } as unknown as Response;
}

// Every usage path ends with the browser zone. Pin it (a zone with a "/" so the
// encoding is exercised) so the URL assertions don't depend on the runner's TZ.
const TZ = '&tz=Asia%2FSingapore';
const realResolvedOptions = Intl.DateTimeFormat.prototype.resolvedOptions;
function stubZone(timeZone: string | undefined) {
  const real = realResolvedOptions;
  return vi
    .spyOn(Intl.DateTimeFormat.prototype, 'resolvedOptions')
    .mockImplementation(function (this: Intl.DateTimeFormat) {
      return { ...real.call(this), timeZone } as Intl.ResolvedDateTimeFormatOptions;
    });
}

beforeEach(() => {
  stubZone('Asia/Singapore');
  // Retry backoffs are real timers; fake them so the retry tests don't sleep
  // 0.5s/2s/5s for real. We advance them manually after kicking the call off.
  vi.useFakeTimers();
});

afterEach(() => {
  vi.runOnlyPendingTimers();
  vi.useRealTimers();
  // Force an auth-context transition so the module-level Usage cache and any
  // in-flight request are cleared even when the test already used local mode.
  configureApi({ token: '__usage_test_reset__' });
  configureApi({ prefix: '', token: null });
  setBackend(null);
  window.history.replaceState(null, '', '/');
  vi.restoreAllMocks();
});

// Drive a promise to settle while advancing fake timers so the internal
// sleep()/AbortController setTimeout()s fire. Loops a few macrotask ticks.
async function flush() {
  for (let i = 0; i < 12; i++) {
    await vi.advanceTimersByTimeAsync(6_000);
  }
}

describe('api.ccUsageMemberSummary URL construction', () => {
  it('builds the member-summary URL with user_id + window (local mode, no team)', async () => {
    const f = spyOkFetch();
    await api.ccUsageMemberSummary('user-123', '30d');
    expect(f).toHaveBeenCalledTimes(1);
    const url = (f.mock.calls[0]![0] as string);
    expect(url).toBe('/api/cc-usage/member-summary?user_id=user-123&window=30d' + TZ);
  });

  it('appends team_id when provided', async () => {
    const f = spyOkFetch();
    await api.ccUsageMemberSummary('user-123', 'all', 'team-abc');
    const url = (f.mock.calls[0]![0] as string);
    expect(url).toBe(
      '/api/cc-usage/member-summary?user_id=user-123&window=all&team_id=team-abc' + TZ,
    );
  });

  it('omits team_id when undefined (does not emit a stray param)', async () => {
    const f = spyOkFetch();
    await api.ccUsageMemberSummary('u', '7d', undefined);
    const url = (f.mock.calls[0]![0] as string);
    expect(url).not.toContain('team_id');
  });

  it('URL-encodes the user_id, window, and team_id values', async () => {
    const f = spyOkFetch();
    await api.ccUsageMemberSummary('a b/c+d', '30d', 'x y&z');
    const url = (f.mock.calls[0]![0] as string);
    expect(url).toBe(
      '/api/cc-usage/member-summary?user_id=a%20b%2Fc%2Bd&window=30d&team_id=x%20y%26z' + TZ,
    );
  });

  it('prefixes the machine path and sends the bearer token in cloud mode', async () => {
    configureApi({ prefix: '/m/machine-1', token: 'jwt-token' });
    const f = spyOkFetch();
    await api.ccUsageMemberSummary('user-123', '90d', 'team-9');
    const url = (f.mock.calls[0]![0] as string);
    expect(url).toBe(
      '/m/machine-1/api/cc-usage/member-summary?user_id=user-123&window=90d&team_id=team-9' + TZ,
    );
    const init = f.mock.calls[0]![1] as RequestInit;
    const headers = new Headers(init.headers);
    expect(headers.get('Authorization')).toBe('Bearer jwt-token');
  });

  it('surfaces a 403 (the privacy boundary) as an ApiError with status 403', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      res(403, { body: 'not authorized', headers: {} }),
    );
    await expect(api.ccUsageMemberSummary('user-x', '30d', 'team-1')).rejects.toMatchObject({
      name: 'ApiError',
      status: 403,
    });
    // And it really is the exported ApiError class (callers branch on instanceof).
    await api
      .ccUsageMemberSummary('user-x', '30d', 'team-1')
      .catch((e) => expect(e).toBeInstanceOf(ApiError));
  });
});

describe('api.health router fallback', () => {
  it('prefers the active backend when the primary health request needs fallback', async () => {
    window.history.replaceState(null, '', '/?solu_router=1');
    setBackend('win');
    const f = vi
      .spyOn(globalThis, 'fetch')
      .mockRejectedValueOnce(new TypeError('primary failed'))
      .mockResolvedValueOnce(res(200, { json: { status: 'ok', backendID: 'win', version: '0.0.785' } }))
      .mockResolvedValueOnce(res(200, { json: { status: 'ok', backendID: 'wsl', version: '0.0.785' } }));

    await expect(api.health()).resolves.toMatchObject({ backendID: 'win' });
    expect(f).toHaveBeenCalledTimes(3);
    expect(f.mock.calls.map((c) => c[0])).toEqual([
      '/b/win/api/health',
      '/b/win/api/health',
      '/b/wsl/api/health',
    ]);
  });
});

describe('cc-usage resilient fetch — retry / backoff / no-retry-on-4xx', () => {
  it('dedupes cached summary calls and serves the warm payload without refetching', async () => {
    const f = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValue(res(200, { json: { window: '30d', generated_at: 'now' } }));

    const [a, b] = await Promise.all([
      api.ccUsageSummaryCached('30d'),
      api.ccUsageSummaryCached('30d'),
    ]);
    expect(a).toBe(b);
    expect(f).toHaveBeenCalledTimes(1);

    const c = await api.ccUsageSummaryCached('30d');
    expect(c).toBe(a);
    expect(f).toHaveBeenCalledTimes(1);
    expect(api.ccUsageSummaryPeek('30d')).toBe(a);
  });

  it('keeps a coalesced request alive when only one subscriber leaves', async () => {
    let resolveFetch!: (value: Response) => void;
    let networkSignal: AbortSignal | undefined;
    const f = vi.spyOn(globalThis, 'fetch').mockImplementation((_url, init) => {
      networkSignal = init?.signal as AbortSignal | undefined;
      return new Promise<Response>((resolve) => { resolveFetch = resolve; });
    });
    const first = new AbortController();
    const second = new AbortController();

    const a = api.ccUsageSummaryCached('30d', { signal: first.signal });
    const b = api.ccUsageSummaryCached('30d', { signal: second.signal });
    first.abort();

    await expect(a).rejects.toMatchObject({ name: 'AbortError' });
    expect(networkSignal?.aborted).toBe(false);
    resolveFetch(res(200, { json: { window: '30d', generated_at: 'shared' } }));
    await expect(b).resolves.toMatchObject({ generated_at: 'shared' });
    expect(f).toHaveBeenCalledTimes(1);
  });

  it('aborts the underlying request after every coalesced subscriber leaves', async () => {
    let networkSignal: AbortSignal | undefined;
    const f = vi.spyOn(globalThis, 'fetch').mockImplementation((_url, init) => {
      networkSignal = init?.signal as AbortSignal | undefined;
      return new Promise<Response>((_resolve, reject) => {
        networkSignal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
      });
    });
    const first = new AbortController();
    const second = new AbortController();
    const a = api.ccUsageSummaryCached('90d', { signal: first.signal });
    const b = api.ccUsageSummaryCached('90d', { signal: second.signal });

    first.abort();
    expect(networkSignal?.aborted).toBe(false);
    second.abort();

    await expect(a).rejects.toMatchObject({ name: 'AbortError' });
    await expect(b).rejects.toMatchObject({ name: 'AbortError' });
    expect(networkSignal?.aborted).toBe(true);

    // StrictMode can resubscribe in the same tick as the last cleanup. The
    // aborted flight must already be gone so this starts a healthy new GET.
    f.mockResolvedValueOnce(res(200, { json: { window: '90d', generated_at: 'remounted' } }));
    await expect(api.ccUsageSummaryCached('90d')).resolves.toMatchObject({ generated_at: 'remounted' });
    expect(f).toHaveBeenCalledTimes(2);
  });

  it('shows stale cached data while refreshing after the five-minute fresh window', async () => {
    const f = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(res(200, { json: { window: '7d', generated_at: 'first' } }))
      .mockResolvedValueOnce(res(200, { json: { window: '7d', generated_at: 'refreshed' } }));
    const first = await api.ccUsageSummaryCached('7d');
    await vi.advanceTimersByTimeAsync(5 * 60_000 + 1);

    expect(api.ccUsageSummaryPeek('7d')).toBe(first);
    const refreshed = await api.ccUsageSummaryCached('7d');
    expect(refreshed.generated_at).toBe('refreshed');
    expect(f).toHaveBeenCalledTimes(2);
  });

  it('retries a transient 503 and resolves once the backend recovers', async () => {
    const f = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(res(503, { body: 'unavailable' }))
      .mockResolvedValueOnce(res(200, { json: { window: '30d' } }));
    const p = api.ccUsageSummary('30d');
    await flush();
    await expect(p).resolves.toMatchObject({ window: '30d' });
    expect(f).toHaveBeenCalledTimes(2);
  });

  it('retries 502 and 504 too (the transient class)', async () => {
    const f = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(res(502))
      .mockResolvedValueOnce(res(504))
      .mockResolvedValueOnce(res(200, { json: { window: '7d' } }));
    const p = api.ccUsageSummary('7d');
    await flush();
    await expect(p).resolves.toMatchObject({ window: '7d' });
    expect(f).toHaveBeenCalledTimes(3);
  });

  it('retries a network error (rejected fetch) then succeeds', async () => {
    const f = vi
      .spyOn(globalThis, 'fetch')
      .mockRejectedValueOnce(new TypeError('Failed to fetch'))
      .mockResolvedValueOnce(res(200, { json: { window: 'all' } }));
    const p = api.ccUsageSummary('all');
    await flush();
    await expect(p).resolves.toMatchObject({ window: 'all' });
    expect(f).toHaveBeenCalledTimes(2);
  });

  it('gives up after 4 attempts (3 retries) and throws the last ApiError', async () => {
    const f = vi.spyOn(globalThis, 'fetch').mockResolvedValue(res(503, { body: 'down' }));
    const p = api.ccUsageSummary('30d');
    // Attach a rejection handler synchronously so the eventual throw isn't an
    // unhandled rejection while we pump timers.
    const settled = p.then(
      () => ({ ok: true as const }),
      (e) => ({ ok: false as const, e }),
    );
    await flush();
    const out = await settled;
    expect(out.ok).toBe(false);
    if (!out.ok) {
      expect(out.e).toBeInstanceOf(ApiError);
      expect((out.e as ApiError).status).toBe(503);
    }
    expect(f).toHaveBeenCalledTimes(4); // 1 initial + 3 retries
  });

  it('does NOT retry a 403 — throws immediately (privacy boundary, callers branch)', async () => {
    const f = vi.spyOn(globalThis, 'fetch').mockResolvedValue(res(403, { body: 'not authorized' }));
    const p = api.ccUsageMemberSummary('user-x', '30d', 'team-1');
    const settled = p.then(
      () => ({ ok: true as const }),
      (e) => ({ ok: false as const, e }),
    );
    await flush();
    const out = await settled;
    expect(out.ok).toBe(false);
    if (!out.ok) {
      expect(out.e).toBeInstanceOf(ApiError);
      expect((out.e as ApiError).status).toBe(403);
    }
    expect(f).toHaveBeenCalledTimes(1); // no retry on 4xx
  });

  it('does NOT retry a 500 (only 502/503/504 are transient)', async () => {
    const f = vi.spyOn(globalThis, 'fetch').mockResolvedValue(res(500, { body: 'boom' }));
    const p = api.ccUsageSummary('30d');
    const settled = p.then(
      () => ({ ok: true as const }),
      (e) => ({ ok: false as const, e }),
    );
    await flush();
    const out = await settled;
    expect(out.ok).toBe(false);
    if (!out.ok) expect((out.e as ApiError).status).toBe(500);
    expect(f).toHaveBeenCalledTimes(1);
  });

  it('flags the parsed body with _stale when X-Solu-Usage-Stale: 1 is present', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      res(200, { json: { window: '30d' }, headers: { 'X-Solu-Usage-Stale': '1' } }),
    );
    const p = api.ccUsageSummary('30d');
    await flush();
    const d = await p;
    expect(d._stale).toBe(true);
  });

  it('leaves _stale unset on a fresh (non-stale) response', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(res(200, { json: { window: '30d' } }));
    const p = api.ccUsageSummary('30d');
    await flush();
    const d = await p;
    expect(d._stale).toBeUndefined();
  });

  it('honors a Retry-After header (capped) over the default backoff', async () => {
    const f = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(res(503, { headers: { 'Retry-After': '3' } }))
      .mockResolvedValueOnce(res(200, { json: { window: '30d' } }));
    const p = api.ccUsageSummary('30d');
    // Advance LESS than the honored 3s but MORE than the default first backoff
    // (0.5s): if Retry-After were ignored the call would already have retried.
    await vi.advanceTimersByTimeAsync(1_000);
    expect(f).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(2_500); // now past the 3s Retry-After
    await expect(p).resolves.toMatchObject({ window: '30d' });
    expect(f).toHaveBeenCalledTimes(2);
  });
});

describe('CCUsageSummary top_projects repo tie-in', () => {
  it('carries the optional repo + repo_url on a parsed top_projects entry', () => {
    // A daemon-shaped fixture: a project whose cwd resolved to a git remote.
    const summary: CCUsageSummary = {
      window: '30d',
      window_days: 30,
      generated_at: '2026-06-21T00:00:00Z',
      currency: 'USD',
      notional: true,
      hero: {
        cost_usd: 1,
        turns: 3,
        sessions: 1,
        active_days: 1,
        fresh_tokens: 1_000_000,
        cached_tokens: 0,
        models_used: 1,
      },
      by_model: [],
      top_projects: [
        {
          project: 'aria',
          cost_usd: 1,
          turns: 3,
          cost_share: 1,
          repo: 'example/app',
          repo_url: 'https://github.com/example/app',
        },
      ],
      daily: [],
      in_out: {
        in_app: { turns: 0, cost_usd: 0 },
        out_app: { turns: 0, cost_usd: 0 },
      },
      subagent_share: { cost_usd: 0, cost_share: 0 },
    };

    expect(summary.top_projects[0]!.repo).toBe('example/app');
    expect(summary.top_projects[0]!.repo_url).toBe('https://github.com/example/app');
  });

  it('treats repo + repo_url as optional (a project with no git remote omits them)', () => {
    const entry: CCUsageSummary['top_projects'][number] = {
      project: 'scratch',
      cost_usd: 0.5,
      turns: 2,
      cost_share: 0.5,
    };
    // Optional: absent is a valid shape; reading them yields undefined.
    expect(entry.repo).toBeUndefined();
    expect(entry.repo_url).toBeUndefined();
  });
});

describe('api.ccUsageTeamSummary URL construction', () => {
  it('builds the lightweight context URL on the existing team route', async () => {
    const f = spyOkFetch();
    await api.ccUsageTeamContext('30d');
    expect((f.mock.calls[0]![0] as string)).toBe(
      '/api/cc-usage/team-summary?window=30d&context=1' + TZ,
    );
  });

  it('builds the team-summary URL with window only', async () => {
    const f = spyOkFetch();
    await api.ccUsageTeamSummary('30d');
    expect((f.mock.calls[0]![0] as string)).toBe('/api/cc-usage/team-summary?window=30d' + TZ);
  });

  it('appends team_id when provided', async () => {
    const f = spyOkFetch();
    await api.ccUsageTeamSummary('7d', 'team-abc');
    expect((f.mock.calls[0]![0] as string)).toBe(
      '/api/cc-usage/team-summary?window=7d&team_id=team-abc' + TZ,
    );
  });
});

describe('cc-usage provider query param', () => {
  it('leaves the URL byte-identical for "all" / omitted (old clouds + cache keys)', () => {
    expect(ccUsagePath('30d')).toBe('/api/cc-usage/summary?window=30d' + TZ);
    expect(ccUsagePath('30d', 'all')).toBe('/api/cc-usage/summary?window=30d' + TZ);
    expect(ccUsageTeamPath('7d', 'team-1', 'all')).toBe('/api/cc-usage/team-summary?window=7d&team_id=team-1' + TZ);
    expect(ccUsageMemberPath('u', '90d', undefined, 'all')).toBe(
      '/api/cc-usage/member-summary?user_id=u&window=90d' + TZ,
    );
  });

  it('appends &provider= when narrowed', () => {
    expect(ccUsagePath('30d', 'codex')).toBe('/api/cc-usage/summary?window=30d&provider=codex' + TZ);
    expect(ccUsageTeamPath('7d', undefined, 'claude')).toBe(
      '/api/cc-usage/team-summary?window=7d&provider=claude' + TZ,
    );
    expect(ccUsageTeamPath('7d', 'team-1', 'codex')).toBe(
      '/api/cc-usage/team-summary?window=7d&team_id=team-1&provider=codex' + TZ,
    );
    expect(ccUsageMemberPath('u', '30d', 'team-1', 'codex')).toBe(
      '/api/cc-usage/member-summary?user_id=u&window=30d&team_id=team-1&provider=codex' + TZ,
    );
  });

  it('threads provider through the fetch wrappers', async () => {
    const f = spyOkFetch();
    await api.ccUsageSummaryCached('30d', { provider: 'codex' });
    await api.ccUsageTeamSummaryCached('7d', 'team-1', { provider: 'claude' });
    await api.ccUsageMemberSummaryCached('u', '90d', undefined, { provider: 'codex' });
    const urls = f.mock.calls.map((c) => c[0] as string);
    expect(urls).toEqual([
      '/api/cc-usage/summary?window=30d&provider=codex' + TZ,
      '/api/cc-usage/team-summary?window=7d&team_id=team-1&provider=claude' + TZ,
      '/api/cc-usage/member-summary?user_id=u&window=90d&provider=codex' + TZ,
    ]);
  });

  it('caches filtered and unfiltered payloads under different keys', async () => {
    spyOkFetch();
    await api.ccUsageSummaryCached('30d', { provider: 'codex' });
    expect(api.ccUsageSummaryPeek('30d', 'codex')).not.toBeNull();
    expect(api.ccUsageSummaryPeek('30d')).toBeNull();
  });
});

describe('cc-usage tz query param (Local-day grouping)', () => {
  it('reads the browser IANA zone', () => {
    expect(usageTimeZone()).toBe('Asia/Singapore');
  });

  it('falls back to UTC when Intl has no usable zone', () => {
    stubZone(undefined);
    expect(usageTimeZone()).toBe('UTC');
    stubZone('');
    expect(usageTimeZone()).toBe('UTC');
    vi.spyOn(Intl.DateTimeFormat.prototype, 'resolvedOptions').mockImplementation(() => {
      throw new Error('no Intl');
    });
    expect(usageTimeZone()).toBe('UTC');
    expect(ccUsagePath('30d')).toBe('/api/cc-usage/summary?window=30d&tz=UTC');
  });

  it('appends an encoded &tz= LAST on every usage path, after provider', () => {
    stubZone('America/Argentina/Buenos_Aires');
    const tz = '&tz=America%2FArgentina%2FBuenos_Aires';
    expect(ccUsagePath('7d')).toBe('/api/cc-usage/summary?window=7d' + tz);
    expect(ccUsagePath('7d', 'codex')).toBe('/api/cc-usage/summary?window=7d&provider=codex' + tz);
    expect(ccUsageTeamPath('30d', 'team-1', 'claude')).toBe(
      '/api/cc-usage/team-summary?window=30d&team_id=team-1&provider=claude' + tz,
    );
    expect(ccUsageMemberPath('u', '90d', 'team-1')).toBe(
      '/api/cc-usage/member-summary?user_id=u&window=90d&team_id=team-1' + tz,
    );
  });

  it('sends tz on the team context request too', async () => {
    const f = spyOkFetch();
    await api.ccUsageTeamContextCached('30d');
    expect(f.mock.calls[0]![0]).toBe('/api/cc-usage/team-summary?window=30d&context=1' + TZ);
  });

  it('keys the client cache by zone (a zone change refetches)', async () => {
    const f = spyOkFetch();
    await api.ccUsageSummaryCached('30d');
    expect(api.ccUsageSummaryPeek('30d')).not.toBeNull();
    stubZone('Europe/London');
    expect(api.ccUsageSummaryPeek('30d')).toBeNull();
    await api.ccUsageSummaryCached('30d');
    expect(f).toHaveBeenCalledTimes(2);
    expect(f.mock.calls[1]![0]).toBe('/api/cc-usage/summary?window=30d&tz=Europe%2FLondon');
  });
});
