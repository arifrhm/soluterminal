// @vitest-environment node
import { describe, expect, it } from 'vitest';
import { createPlaywrightConfig } from './playwright-config.mjs';
import { allocateLoopbackPort, resolvePlaywrightRunPort } from './playwright-port.mjs';

describe('Playwright server port allocation', () => {
  it('reuses the run port inherited by replacement workers', async () => {
    const env = {};
    let allocations = 0;
    const allocate = async () => {
      allocations++;
      return 43123;
    };

    await expect(resolvePlaywrightRunPort({ env, allocate })).resolves.toBe(43123);
    expect(env.SOLU_PLAYWRIGHT_RUN_PORT).toBe('43123');
    await expect(resolvePlaywrightRunPort({ env, allocate })).resolves.toBe(43123);
    expect(allocations).toBe(1);
  });

  it('evaluates config without opening a loopback socket when the run port is inherited', async () => {
    let allocations = 0;
    const config = await createPlaywrightConfig({
      env: { SOLU_PLAYWRIGHT_RUN_PORT: '43124' },
      allocate: async () => {
        allocations++;
        return 0;
      },
    });

    expect(allocations).toBe(0);
    expect(config.use.baseURL).toBe('http://127.0.0.1:43124');
    expect(config.webServer.url).toBe('http://127.0.0.1:43124');
    expect(config.webServer.command).toContain('--port 43124');
  });

  it('allocates distinct ports for concurrent independent runs', async () => {
    let nextPort = 43125;
    const allocate = async () => nextPort++;
    const environments = [{}, {}];

    const ports = await Promise.all(environments.map((env) => (
      resolvePlaywrightRunPort({ env, allocate })
    )));

    expect(ports).toEqual([43125, 43126]);
    expect(environments.map((env) => env.SOLU_PLAYWRIGHT_RUN_PORT)).toEqual(['43125', '43126']);
  });

  it('allocates distinct ports through the production allocator path', async () => {
    let nextPort = 43127;
    const createServer = () => {
      const port = nextPort++;
      return {
        unref() {},
        once(event) {
          expect(event).toBe('error');
        },
        listen(requestedPort, host, onListening) {
          expect(requestedPort).toBe(0);
          expect(host).toBe('127.0.0.1');
          queueMicrotask(onListening);
        },
        address() {
          return { address: '127.0.0.1', family: 'IPv4', port };
        },
        close(onClosed) {
          queueMicrotask(() => onClosed());
        },
      };
    };

    await expect(Promise.all([
      allocateLoopbackPort({ createServer }),
      allocateLoopbackPort({ createServer }),
    ])).resolves.toEqual([43127, 43128]);
  });

  it('evaluates concurrent runs with distinct loopback base URLs', async () => {
    let nextPort = 43129;
    const allocate = async () => nextPort++;
    const configurations = await Promise.all([
      createPlaywrightConfig({ env: {}, allocate }),
      createPlaywrightConfig({ env: {}, allocate }),
    ]);
    const baseURLs = configurations.map((config) => config.use.baseURL);
    const urls = baseURLs.map((value) => new URL(value));

    expect(urls.map((url) => url.hostname)).toEqual(['127.0.0.1', '127.0.0.1']);
    expect(urls.map((url) => url.port)).toEqual(['43129', '43130']);
  });
});
