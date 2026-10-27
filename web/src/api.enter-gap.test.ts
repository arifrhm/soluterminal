import { afterEach, expect, it, vi } from 'vitest';

// Importing the API is deliberately separate from starting the app. Preview
// transport and cloud/router routing must be ready before the first request.
it('does not fetch daemon timings while importing API helpers', async () => {
  vi.resetModules();
  const fetcher = vi.fn<typeof fetch>();
  vi.stubGlobal('fetch', fetcher);
  await import('./api');
  expect(fetcher).not.toHaveBeenCalled();
});

it('loads timing configuration through the mounted preview transport', async () => {
  const { configureApi, loadEnterGapConfig } = await import('./api');
  const { createPreviewFetch } = await import('./previewBoot');
  const { enterGapMs } = await import('./ptySubmit');
  configureApi({ prefix: '', token: null });
  const nativeFetch = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({ claudeMs: 700, defaultMs: 200 })));
  vi.stubGlobal('fetch', createPreviewFetch(nativeFetch, 'http://localhost/preview/candidate/'));
  await loadEnterGapConfig();
  expect(nativeFetch.mock.calls[0][0]).toBe('http://localhost/preview/candidate/api/enter-gap');
  expect(enterGapMs(false)).toBe(700);
  expect(enterGapMs(true)).toBe(200);
});

it('ignores a timing response from a machine that is no longer selected', async () => {
  const { configureApi, loadEnterGapConfig } = await import('./api');
  const { enterGapMs, setEnterGapConfig } = await import('./ptySubmit');
  setEnterGapConfig({ claudeMs: 400, defaultMs: 100 });
  configureApi({ prefix: '/m/first' });
  let respond!: (response: Response) => void;
  vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((resolve) => { respond = resolve; })));
  const pending = loadEnterGapConfig();
  configureApi({ prefix: '/m/second' });
  respond(new Response(JSON.stringify({ claudeMs: 900, defaultMs: 300 })));
  await pending;
  expect(enterGapMs(false)).toBe(400);
  expect(enterGapMs(true)).toBe(100);
});

afterEach(async () => {
  const { configureApi, setBackend } = await import('./api');
  const { setEnterGapConfig } = await import('./ptySubmit');
  configureApi({ prefix: '', token: null });
  setBackend(null);
  setEnterGapConfig({ claudeMs: 400, defaultMs: 100 });
  vi.unstubAllGlobals();
});
