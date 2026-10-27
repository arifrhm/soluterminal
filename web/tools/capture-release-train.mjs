#!/usr/bin/env node

import fs from 'node:fs';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { TEAM_ENTITLEMENTS, buildServiceFixtures, exitBrowserUnavailable, resolvePlaywright, routeServiceFixtures } from './screens-lib.mjs';

function arg(name) {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : '';
}

const dist = arg('--dist');
const out = arg('--out');
const constrained = process.env.SOLU_CAPTURE_CONSTRAINED === '1';
const scenarioFilter = arg('--scenario');
const variantFilter = arg('--variant');
if (!dist || !out) {
  process.stderr.write('usage: capture-release-train.mjs --dist <dir> --out <dir>\n');
  process.exit(2);
}

const now = '2026-08-26T03:20:00Z';
const sourceSha = '134e4480f8187c2372d6ece9274a848efaf681ff';
const baseTrain = {
  policy: {
    enabled: true,
    minFunctionalLandings: 6,
    maxUnreleasedAgeHours: 12,
    quietRequired: true,
    notifyVetoMinutes: 10,
  },
  preview: null,
  events: [],
};
const run = (status) => ({
  id: 'rtr_89551c82',
  version: '0.0.1033',
  sourceSha,
  status,
  declaredAt: '2026-08-26T03:00:00Z',
  departAt: '2026-08-26T03:10:00Z',
});
const park = (overrides) => ({
  reason: 'publish failed: publish.sh exited 1',
  head: sourceSha,
  sourceSha,
  at: now,
  attempt: 0,
  maxAttempts: 1,
  ...overrides,
});
const scenarios = [
  {
    name: 'failed-package-timeout',
    train: {
      ...baseTrain,
      publishState: 'failed',
      state: {
        phase: 'parked', train: run('parked'), restartPending: null,
        parked: park({
          failureClass: 'package_timeout',
          evidence: 'FAIL soluterminal/daemon 600.187s',
          retryEligible: true,
          nextRetryAt: '2026-08-26T03:35:00Z',
        }),
      },
    },
    expect: 'Publish failed · package timeout',
  },
  {
    name: 'failed-test-assertion',
    train: {
      ...baseTrain,
      publishState: 'failed',
      state: {
        phase: 'parked', train: run('parked'), restartPending: null,
        parked: park({
          failureClass: 'test_assertion',
          evidence: '--- FAIL: TestReleaseManifestSource (0.02s)',
          retryEligible: false,
        }),
      },
    },
    expect: 'parked until a fixing commit lands',
  },
  {
    name: 'retrying',
    train: {
      ...baseTrain,
      publishState: 'retrying',
      state: {
        phase: 'parked', train: run('publishing'), restartPending: null,
        parked: park({ failureClass: 'package_timeout', retryEligible: true, attempt: 1 }),
      },
    },
    expect: 'full test gate running',
  },
  {
    name: 'published',
    train: {
      ...baseTrain,
      publishState: 'published',
      state: { phase: 'idle', train: run('published'), parked: null, restartPending: null },
    },
    expect: 'Published · v0.0.1033',
  },
];

const captureState = {
  healthVersion: '0.0.1033',
  releaseReadiness: {
    state: 'ok',
    lastReleaseVersion: '0.0.1032',
    lastReleaseAnchor: 'd7026e405597803602099da365afa65397a98a85',
    anchorSource: 'published-manifest',
    anchorProvenance: 'exact',
    unreleasedTotal: 7,
    functionalCount: 6,
    docsOnlyCount: 1,
    functionalPaths: ['daemon', 'web', 'scripts'],
    suggestedNextVersion: '0.0.1033',
    oldestUnreleasedAt: '2026-08-25T19:00:00Z',
    runningVersion: '0.0.1032',
    dirty: false,
    computedAt: now,
  },
  releaseCheck: {
    allowed: true,
    cut: {
      id: 'rc_89551c82', version: '0.0.1033', repoKey: '/home/user/soluterminal/.git',
      sourceSha, declaredBy: 'release-train', status: 'open',
      createdAt: '2026-08-26T03:00:00Z', updatedAt: now,
    },
    blockers: [], waived: [], included: [], warnings: [],
  },
  releaseTrain: scenarios[0].train,
};

const playwright = await resolvePlaywright();
if (!playwright) exitBrowserUnavailable('Playwright module not found');
// A --single-process browser ends with its first context, so each capture
// gets a browser of its own.
const launch = () => playwright.chromium.launch({
  headless: true,
  args: ['--disable-crash-reporter', '--disable-crashpad', '--in-process-gpu', '--single-process', '--no-zygote'],
});
let browser;
try {
  fs.mkdirSync(out, { recursive: true });
  const variants = [
    { name: 'light', theme: 'light', width: 1440, height: 1000 },
    { name: 'dark', theme: 'dark', width: 1440, height: 1000 },
    { name: 'narrow', theme: 'light', width: 400, height: 1200 },
  ];
  const shots = [];
  for (const scenario of scenarios.filter((item) => !scenarioFilter || item.name === scenarioFilter)) {
    captureState.releaseTrain = scenario.train;
    for (const variant of variants.filter((item) => !variantFilter || item.name === variantFilter)) {
      // Constrained mode keeps everything open for inspection: one browser.
      if (!browser || !constrained) browser = await launch();
      const context = await browser.newContext({ viewport: { width: variant.width, height: variant.height } });
      try {
        await context.route('**/*', async (route) => {
          const request = route.request();
          const url = new URL(request.url());
          const json = (value, status = 200) => route.fulfill({
            status,
            contentType: 'application/json; charset=utf-8',
            body: JSON.stringify(value),
          });
          if (url.pathname === '/api/health') return json({
            status: 'ok', version: captureState.healthVersion,
            system: { level: 'ok', sampledAt: now },
            releaseReadiness: captureState.releaseReadiness,
          });
          // The release chip is an operator surface; a team install has it.
          if (url.pathname === '/api/entitlements') return json(TEAM_ENTITLEMENTS);
          if (url.pathname === '/api/workflow/release-check') return json(captureState.releaseCheck);
          if (url.pathname === '/api/workflow/release-train') return json(captureState.releaseTrain);
          if (url.pathname === '/api/system/status') return json({ level: 'ok', sampledAt: now, memory: {}, disk: {} });
          if (url.pathname === '/api/ui/events' || url.pathname === '/api/events') {
            return route.fulfill({ status: 200, contentType: 'text/event-stream', body: ': release train evidence\n\n' });
          }
          if (url.pathname.startsWith('/api/')) return json([]);
          let requestPath = decodeURIComponent(url.pathname);
          if (requestPath === '/') requestPath = '/index.html';
          let file = path.resolve(dist, `.${requestPath}`);
          const root = path.resolve(dist) + path.sep;
          if (!file.startsWith(root) || !fs.existsSync(file) || !fs.statSync(file).isFile()) {
            file = path.join(path.resolve(dist), 'index.html');
          }
          const contentType = {
            '.html': 'text/html; charset=utf-8',
            '.js': 'text/javascript; charset=utf-8',
            '.css': 'text/css; charset=utf-8',
            '.json': 'application/json; charset=utf-8',
            '.svg': 'image/svg+xml',
            '.png': 'image/png',
            '.woff2': 'font/woff2',
          }[path.extname(file)] || 'application/octet-stream';
          return route.fulfill({ path: file, contentType });
        });
        // Routes registered later win in Playwright, so the shared fixtures
        // must not carry the endpoints this scenario answers above — their
        // disabled release train would hide the one being captured.
        const fixtures = buildServiceFixtures(Date.parse(now));
        for (const owned of ['/api/health', '/api/entitlements', '/api/workflow/release-check', '/api/workflow/release-train', '/api/system/status']) delete fixtures[owned];
        await routeServiceFixtures(context, fixtures);
        const page = await context.newPage();
        page.on('pageerror', (error) => process.stderr.write(`page error: ${error.message}\n`));
        page.on('console', (message) => {
          if (message.type() === 'error') process.stderr.write(`browser console: ${message.text()}\n`);
        });
        await page.addInitScript((theme) => {
          localStorage.setItem('solu.appTheme', theme);
          localStorage.setItem('solu.seenWelcome', '1');
          localStorage.setItem('solu.seenChecklist', '1');
          localStorage.setItem('solu.debugOpenRelease', '1');
          localStorage.setItem('solu.sidebarWorkspacesCollapsed.v1', '1');
        }, variant.theme);
        await page.goto('http://localhost/', { waitUntil: 'load' });
        await page.waitForSelector('.app', { timeout: 10_000 });
        const dialog = page.getByRole('dialog', { name: 'Release readiness' });
        if (!(await dialog.isVisible().catch(() => false))) {
          await page.locator('button.statusbar-release').click();
        }
        await dialog.waitFor({ state: 'visible', timeout: 10_000 });
        await dialog.getByText(scenario.expect, { exact: false }).waitFor({ state: 'visible', timeout: 5_000 });
        if (variant.name === 'narrow') {
          await dialog.evaluate((element) => {
            element.style.position = 'absolute';
            element.style.top = '48px';
            element.style.bottom = 'auto';
          });
        }
        await page.waitForTimeout(250);
        const file = `${scenario.name}-${variant.name}.png`;
        const outputPath = path.join(out, file);
        const pdfPath = path.join(out, `.${file}.pdf`);
        await page.emulateMedia({ media: 'screen' });
        await page.pdf({
          path: pdfPath,
          width: `${variant.width}px`,
          height: `${variant.height}px`,
          printBackground: true,
          timeout: 120_000,
        });
        try {
          execFileSync('python3', ['-c', [
            'import fitz,sys',
            'doc=fitz.open(sys.argv[1])',
            'pix=doc[0].get_pixmap(dpi=96, alpha=False)',
            'pix.save(sys.argv[2])',
          ].join(';'), pdfPath, outputPath]);
        } finally {
          fs.rmSync(pdfPath, { force: true });
        }
        shots.push(file);
      } finally {
        if (!constrained) {
          await context.close();
          await browser.close();
          browser = undefined;
        }
      }
    }
  }
  process.stdout.write(`${JSON.stringify({ shots })}\n`);
} finally {
  if (browser && !constrained) await browser.close();
}
