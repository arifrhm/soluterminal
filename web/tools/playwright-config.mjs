import { devices } from 'playwright/test';
import { resolvePlaywrightRunPort } from './playwright-port.mjs';

export async function createPlaywrightConfig({
  env = process.env,
  allocate,
} = {}) {
  const port = await resolvePlaywrightRunPort({ env, allocate });

  return {
    globalSetup: './tools/browser-preflight-global-setup.mjs',
    testDir: './e2e',
    fullyParallel: false,
    workers: 1,
    retries: 0,
    forbidOnly: !!env.CI,
    reporter: [['html', { outputFolder: 'playwright-report', open: 'never' }], ['list']],
    use: {
      baseURL: `http://127.0.0.1:${port}`,
      serviceWorkers: 'block',
      trace: 'retain-on-failure',
      screenshot: 'only-on-failure',
    },
    webServer: {
      command: `node tools/screens-lib.mjs --serve --dist dist --port ${port} --strictPort`,
      url: `http://127.0.0.1:${port}`,
      reuseExistingServer: false,
      timeout: 30_000,
    },
    projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  };
}
