#!/usr/bin/env node

// Deterministic light/dark evidence for local API-key credential editing. The
// production bundle is served unchanged against schema-complete daemon fixtures.

import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { ensureBrowserCanStart } from './browser-preflight.mjs';
import {
  createServer,
  dismissDaemonBanners,
  exitBrowserUnavailable,
  fixtureHealthVersion,
  launchChromium,
  resolvePlaywright,
} from './screens-lib.mjs';

const toolsDir = path.dirname(fileURLToPath(import.meta.url));
const webDir = path.resolve(toolsDir, '..');
const repoDir = path.resolve(webDir, '..');
export const CREDENTIALS_KEYS_LOCAL_ONLY_MESSAGE = 'Credential changes are local-only — do this on the machine itself.';

// This function is passed directly to Playwright's isolated browser world, so
// keep it self-contained. Credentials is shipped hidden until the user opts in;
// opening its accordion alone does not make the section part of the sidebar.
export function installCredentialsKeysState(theme) {
  localStorage.setItem('solu.appTheme', theme);
  localStorage.setItem('solu.seenWelcome', '1');
  localStorage.setItem('solu.seenChecklist', '1');
  localStorage.setItem('solu.sidebarOpenMiddle.v2', JSON.stringify(['credentials']));
  localStorage.setItem('solu.sidebarWorkspacesCollapsed.v1', '1');
  localStorage.setItem('solu.userPrefs.v1', JSON.stringify({
    prefs: { sidebar: { hidden: [] } },
    updatedAt: null,
    userId: null,
  }));
}

function runtime(id, provider, status, env, configured, required = true) {
  return {
    id,
    provider,
    signedIn: null,
    identity: { email: '', plan: '' },
    quota: [],
    lastProbeAtEpoch: 0,
    probeSource: 'cache',
    lastInvocation: null,
    status,
    installed: null,
    secrets: [{ env, required, configured }],
  };
}

export const CREDENTIALS_KEYS_HEALTH_FIXTURE = Object.freeze({
  runtimes: Object.freeze([
    runtime('deepseek-headless', 'deepseek', 'auth_error', 'DEEPSEEK_API_KEY', false),
    runtime('gemini-headless', 'google', 'unknown', 'GEMINI_API_KEY', false, false),
    runtime('grok-headless', 'xai', 'auth_error', 'XAI_API_KEY', false),
    runtime('openrouter-headless', 'openrouter', 'healthy', 'OPENROUTER_API_KEY', true),
  ]),
  resolution: Object.freeze([]),
});

export const credentialsKeysEvidenceScenarios = Object.freeze([
  { name: 'credentials-api-key-add' },
  { name: 'deepseek-pricing-balance-error-narrow' },
]);

export const DEEPSEEK_PRICING_CAPTURE_FIXTURE = Object.freeze({
  generatedAt: '2026-09-28T05:26:00Z',
  currency: 'CNY', unit: 'per 1M tokens',
  requestedModel: 'deepseek-v4-flash', canonicalModel: 'deepseek-flash', priced: true,
  state: 'off-peak', holidayVerified: true, holiday: false, scheduleWouldPeak: false,
  savingsPercent: 50,
  currentRates: { cachedInput: 0.02, uncachedInput: 1, output: 4 },
  peakRates: { cachedInput: 0.04, uncachedInput: 2, output: 8 },
  offPeakRates: { cachedInput: 0.02, uncachedInput: 1, output: 4 },
  nextChange: { state: 'peak', at: '2026-09-28T06:00:00Z' },
  catalog: [
    { model: 'deepseek-flash', peak: { cachedInput: 0.04, uncachedInput: 2, output: 8 }, offPeak: { cachedInput: 0.02, uncachedInput: 1, output: 4 } },
    { model: 'deepseek-v4-pro', peak: { cachedInput: 0.30, uncachedInput: 9, output: 27 }, offPeak: { cachedInput: 0.15, uncachedInput: 4.5, output: 13.5 } },
  ],
  policy: {
    timezone: 'Asia/Shanghai', windows: ['Monday–Friday 09:00–12:00', 'Monday–Friday 14:00–18:00'], halfOpen: true,
    source: { label: 'DeepSeek Models & Pricing (Chinese/CNY)', url: 'https://api-docs.deepseek.com/zh-cn/quick_start/pricing/' },
  },
  pricing: {
    verifiedDate: '2026-09-28',
    sources: [{ label: 'DeepSeek Models & Pricing (Chinese/CNY)', url: 'https://api-docs.deepseek.com/zh-cn/quick_start/pricing/' }],
  },
  calendar: {
    version: 'cn-public-holidays-2025-2026-v1', updatedDate: '2025-11-04', coverageFrom: 2025, coverageTo: 2026,
    sources: [{ label: 'State Council 2026 public-holiday notice', url: 'https://www.gov.cn/zhengce/zhengceku/202511/content_7047091.htm', issuedDate: '2025-11-04' }],
  },
});

// The pre-verify evidence runner invokes declared tools with only their
// manifest args, so an empty args list must publish to the in-worktree paths.
export function defaultCredentialsKeysPaths() {
  return {
    dist: path.join(webDir, 'dist'),
    out: path.join(repoDir, 'docs', 'screenshots'),
  };
}

export function expectedCredentialsKeysCaptures() {
  return credentialsKeysEvidenceScenarios.flatMap(({ name }) => [
    `${name}-dark.png`,
    `${name}-light.png`,
  ]);
}

export function missingCredentialsKeysEvidence(out) {
  return expectedCredentialsKeysCaptures().filter((file) => !fs.existsSync(path.join(out, file)));
}

function arg(name) {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : '';
}

async function listen(server) {
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  const address = server.address();
  if (!address || typeof address === 'string') throw new Error('credentials capture server has no TCP address');
  return address.port;
}

async function closeServer(server) {
  await new Promise((resolve) => server.close(() => resolve())).catch(() => {});
}

export async function captureCredentialsKeysEvidence({ dist, out, browser: providedBrowser }) {
  const { waitForAppReady } = await import('./capture-screens.mjs');

  const builtDist = path.resolve(dist);
  const server = createServer(builtDist, {
    healthVersion: fixtureHealthVersion(builtDist),
    apiResponses: {
      '/api/workflow/runtimes/health': CREDENTIALS_KEYS_HEALTH_FIXTURE,
      '/api/harnesses/glm': { installed: true, authenticationStatus: 'no-account', accounts: [] },
      '/api/harnesses/pi': { installed: true, authenticationStatus: 'no-account', accounts: [] },
      '/api/harnesses/pi-glm': { installed: true, authenticationStatus: 'no-account', accounts: [] },
      '/api/harnesses/deepseek': {
        installed: true, authenticationStatus: 'not-tested', configured: true, keyPresent: true,
        accounts: [{ name: 'mocked-main', source: 'personal', selected: true, billing: 'api', model: 'deepseek-v4-flash' }],
      },
      '/api/harnesses/deepseek/usage': { importedAt: 0, sources: [], rowCount: 0, currency: 'CNY', costTotal: 0, keys: [] },
      '/api/harnesses/deepseek/balance': { status: 'unavailable', available: false, balances: [] },
      '/api/harnesses/deepseek/team': { available: false, members: [] },
      '/api/harnesses/deepseek/pricing': DEEPSEEK_PRICING_CAPTURE_FIXTURE,
    },
  });
  let browser = providedBrowser;
  let ownsBrowser = false;
  try {
    const port = await listen(server);
    fs.mkdirSync(out, { recursive: true });
    if (!browser) {
      const playwright = await resolvePlaywright();
      if (!playwright) process.exit(78);
      try {
        await ensureBrowserCanStart();
        browser = await launchChromium(playwright, { headless: true });
        ownsBrowser = true;
      } catch (error) {
        exitBrowserUnavailable(error instanceof Error ? error.message : String(error));
      }
    }

    const context = await browser.newContext({
      viewport: { width: 1440, height: 1000 },
      serviceWorkers: 'block',
    });
    const captures = [];
    try {
      for (const theme of ['dark', 'light']) {
        const page = await context.newPage();
        const pageErrors = [];
        page.on('pageerror', (error) => pageErrors.push(error));
        try {
          await page.addInitScript(installCredentialsKeysState, theme);
          await page.goto(`http://127.0.0.1:${port}/`, { waitUntil: 'load' });
          await waitForAppReady(page);
          await dismissDaemonBanners(page);

          const handle = page.locator('.sidebar-resize-handle');
          const box = await handle.boundingBox();
          if (!box) throw new Error('sidebar resize handle has no box');
          await page.mouse.move(box.x + box.width / 2, box.y + 200);
          await page.mouse.down();
          await page.mouse.move(box.x + 220, box.y + 200, { steps: 12 });
          await page.mouse.up();

          const section = page.locator('#sidebar [data-section-key="credentials"]');
          await section.waitFor({ state: 'visible' });
          const row = section.locator('.cred-row', { hasText: 'DeepSeek' });
          await row.waitFor({ state: 'visible' });
          if (await row.getByText(CREDENTIALS_KEYS_LOCAL_ONLY_MESSAGE).count() > 0) {
            throw new Error('DeepSeek API-key row rendered local-only guidance in the local session');
          }
          const addKey = row.getByRole('button', { name: 'Add key' });
          if (!await addKey.isVisible()) {
            throw new Error('DeepSeek API-key row lacks a visible Add key button');
          }
          await addKey.click();
          await page.getByLabel('DeepSeek DEEPSEEK_API_KEY API key').waitFor({ state: 'visible' });
          await page.waitForTimeout(250);
          if (pageErrors.length > 0) {
            throw new Error(`credentials capture pageerror: ${pageErrors.map(String).join('; ')}`);
          }
          const file = `credentials-api-key-add-${theme}.png`;
          await section.screenshot({ path: path.join(out, file) });
          if (pageErrors.length > 0) {
            throw new Error(`credentials capture pageerror: ${pageErrors.map(String).join('; ')}`);
          }
          captures.push(file);

          // The provider/balance data below is intentionally mocked. Keep an
          // on-image label so the evidence can never be mistaken for a live
          // DeepSeek account result.
          await page.keyboard.press('Escape');
          const harness = section.getByLabel('DeepSeek Harness credentials');
          await harness.waitFor({ state: 'visible' });
          await harness.getByRole('button', { name: 'Toggle DeepSeek usage and spend' }).click();
          const pricing = page.getByRole('button', { name: /DeepSeek pricing: Off-peak/ });
          await pricing.waitFor({ state: 'visible' });

          const narrowHandle = page.locator('.sidebar-resize-handle');
          const narrowBox = await narrowHandle.boundingBox();
          if (!narrowBox) throw new Error('sidebar resize handle has no box for narrow pricing capture');
          await page.mouse.move(narrowBox.x + narrowBox.width / 2, narrowBox.y + 200);
          await page.mouse.down();
          await page.mouse.move(Math.max(250, narrowBox.x - 280), narrowBox.y + 200, { steps: 12 });
          await page.mouse.up();

          const sectionBox = await section.boundingBox();
          if (!sectionBox || sectionBox.width > 400) {
            throw new Error(`DeepSeek pricing evidence sidebar is not narrow (${sectionBox?.width ?? 'missing'}px)`);
          }
          await harness.getByText('Balance: unavailable').waitFor({ state: 'visible' });
          await harness.getByText(/Import the DeepSeek platform/).waitFor({ state: 'visible' });

          await pricing.focus();
          const details = page.getByRole('dialog', { name: 'DeepSeek pricing details' });
          await details.waitFor({ state: 'visible' });
          await page.keyboard.press('Escape');
          await details.waitFor({ state: 'hidden' });
          if (!await pricing.evaluate((node) => node === document.activeElement)) {
            throw new Error('Escape did not restore focus to the DeepSeek pricing badge');
          }
          await pricing.click();
          await details.waitFor({ state: 'visible' });
          await page.evaluate(() => {
            const label = document.createElement('div');
            label.dataset.evidenceLabel = 'mocked';
            label.textContent = 'MOCKED provider/balance state · built-app UI evidence';
            Object.assign(label.style, {
              position: 'fixed', top: '8px', right: '8px', zIndex: '2147483647',
              padding: '6px 9px', borderRadius: '5px', background: '#7c2d12', color: '#fff',
              font: '600 12px sans-serif', letterSpacing: '.02em',
            });
            document.body.append(label);
          });
          await page.waitForTimeout(250);
          const pricingFile = `deepseek-pricing-balance-error-narrow-${theme}.png`;
          await page.screenshot({ path: path.join(out, pricingFile) });
          captures.push(pricingFile);
        } finally {
          await page.close();
        }
      }
    } finally {
      await context.close();
    }
    return captures;
  } finally {
    if (ownsBrowser && browser) await browser.close().catch(() => {});
    await closeServer(server);
  }
}

export async function ensureCredentialsKeysEvidence({
  currentDist,
  out = path.join(repoDir, 'docs', 'screenshots'),
  browser,
  capture = captureCredentialsKeysEvidence,
} = {}) {
  const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'solu-credentials-keys-evidence-'));
  try {
    await capture({ dist: currentDist, out: tempDir, browser });
    fs.mkdirSync(out, { recursive: true });
    let created = false;
    for (const file of expectedCredentialsKeysCaptures()) {
      const source = path.join(tempDir, file);
      const destination = path.join(out, file);
      if (!fs.existsSync(destination) && fs.existsSync(source)) {
        fs.copyFileSync(source, destination);
        created = true;
      }
    }
    const stillMissing = missingCredentialsKeysEvidence(out);
    if (stillMissing.length > 0) {
      throw new Error(`credentials API-key evidence capture missed: ${stillMissing.join(', ')}`);
    }
    return { captures: expectedCredentialsKeysCaptures(), created };
  } finally {
    fs.rmSync(tempDir, { recursive: true, force: true });
  }
}

async function main() {
  const defaults = defaultCredentialsKeysPaths();
  const dist = path.resolve(arg('--dist') || defaults.dist);
  const out = path.resolve(arg('--out') || defaults.out);
  const captures = await captureCredentialsKeysEvidence({ dist, out });
  process.stdout.write(`${JSON.stringify({ captures }, null, 2)}\n`);
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) {
  await main();
}
