#!/usr/bin/env node

// Deterministic built-app evidence for the local support-bundle journey.
// Every response is fulfilled in-process from the committed no-live-data
// fixture; no live daemon, terminal, account, or filesystem data is touched.

import fs from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { chromium } from 'playwright';
import { ensureBrowserCanStart } from './browser-preflight.mjs';
import { fixtureHealthVersion, systemStatusFixture } from './screens-lib.mjs';

const PNG_MAGIC = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);

function arg(argv, name) {
  const index = argv.indexOf(name);
  return index >= 0 ? argv[index + 1] : '';
}

export const supportBundleCaptureFiles = Object.freeze([
  'support-bundle-dialog-light.png',
  'support-bundle-dialog-dark.png',
  'support-bundle-empty-light.png',
  'support-bundle-empty-dark.png',
]);

export function assertSupportBundleCaptures(out) {
  for (const file of supportBundleCaptureFiles) {
    const target = path.join(out, file);
    if (!fs.existsSync(target)) throw new Error(`missing support bundle capture: ${file}`);
    const fd = fs.openSync(target, 'r');
    try {
      const magic = Buffer.alloc(PNG_MAGIC.length);
      if (fs.readSync(fd, magic, 0, magic.length, 0) !== magic.length || !magic.equals(PNG_MAGIC)) {
        throw new Error(`support bundle capture is not a PNG: ${file}`);
      }
    } finally {
      fs.closeSync(fd);
    }
  }
}

function emptyFixture(fixture) {
  return {
    ...fixture,
    sections: fixture.sections.map((section) => ({
      ...section,
      text: '',
      bytes: 0,
      lines: 0,
      empty: true,
      note: 'No fixture data is available for this source.',
    })),
  };
}

export function fixtureResponder(dist, fixture, state = 'loaded') {
  const healthVersion = fixtureHealthVersion(dist);
  const mime = {
    '.css': 'text/css; charset=utf-8', '.html': 'text/html; charset=utf-8',
    '.ico': 'image/x-icon', '.js': 'text/javascript; charset=utf-8',
    '.json': 'application/json; charset=utf-8', '.png': 'image/png',
    '.svg': 'image/svg+xml', '.woff': 'font/woff', '.woff2': 'font/woff2',
  };
  return async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const pathname = url.pathname;
    const json = (value, status = 200) => route.fulfill({
      status, contentType: 'application/json; charset=utf-8', body: JSON.stringify(value),
    });
    if (request.method() === 'GET' && ['/api/ui/events', '/api/events'].includes(pathname)) {
      return route.fulfill({ status: 200, contentType: 'text/event-stream', body: ': support bundle fixture\n\n' });
    }
    if (pathname === '/api/health') {
      return json({
        status: 'ok', version: healthVersion, webVersion: healthVersion, panes: 0,
        claudeChecked: true, claudeInstalled: true, claudeAuthed: true,
      });
    }
    if (pathname === '/api/panes') return json([]);
    // The mounted app consumes the inbox even when this scene has no tasks.
    // Returning the generic [] fallback crashes canonicalizeTasks before the
    // command palette can render, hiding the real cause behind a timeout.
    if (pathname === '/api/workflow/inbox') return json({
      decide: [], recovering: [], waiting: [], history: [], rootBlockers: [],
      counts: { decide: 0 },
    });
    if (pathname === '/api/system/status') return json(systemStatusFixture(Date.parse('2026-09-06T10:00:00Z')));
    if (pathname === '/api/support/bundle') {
      if (state === 'remote') return route.fulfill({ status: 403, contentType: 'text/plain', body: 'support bundles are local-only' });
      return json(state === 'empty' ? emptyFixture(fixture) : fixture);
    }
    if (pathname === '/api/config') return json({ error: 'not found' }, 404);
    if (pathname.startsWith('/api/')) return json([]);
    if (!['GET', 'HEAD'].includes(request.method())) return route.fulfill({ status: 405, body: '' });

    const relative = decodeURIComponent(pathname === '/' ? '/index.html' : pathname);
    let file = path.resolve(dist, `.${relative}`);
    const root = `${dist}${path.sep}`;
    if (!file.startsWith(root)) return route.fulfill({ status: 403, body: '' });
    if (!fs.existsSync(file) || !fs.statSync(file).isFile()) file = path.join(dist, 'index.html');
    return route.fulfill({
      status: 200,
      contentType: mime[path.extname(file)] || 'application/octet-stream',
      body: request.method() === 'HEAD' ? '' : await fs.promises.readFile(file),
    });
  };
}

export async function openSupportBundleFromPalette(page) {
  // The palette listener is installed by App's effects. Waiting for any button
  // raced the loading shell and could dispatch before that listener existed;
  // the real topbar is the stable signal used by the behavioural E2E lane.
  await page.locator('.app .topbar').waitFor({ timeout: 15_000 });
  await page.keyboard.press('Control+K');
  await page.getByRole('dialog', { name: 'Command palette' }).waitFor({ timeout: 15_000 });
  await page.getByText('Create support bundle…', { exact: true }).click();
  await page.getByRole('dialog', { name: 'Create support bundle' }).waitFor({ timeout: 15_000 });
}

async function preparePage(page, theme, fulfill) {
  await page.route('**/*', fulfill);
  await page.addInitScript((selectedTheme) => {
    localStorage.setItem('solu.appTheme', selectedTheme);
    localStorage.setItem('solu.seenWelcome', '1');
    localStorage.setItem('solu.seenChecklist', '1');
  }, theme);
  await page.emulateMedia({ media: 'screen', colorScheme: theme });
  await page.goto('http://localhost/', { waitUntil: 'load' });
  await openSupportBundleFromPalette(page);
}

export async function captureSupportBundle({ dist, out, fixturePath }) {
  const fixture = JSON.parse(fs.readFileSync(fixturePath, 'utf8'));
  fs.mkdirSync(out, { recursive: true });
  await ensureBrowserCanStart();
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.PLAYWRIGHT_CHROMIUM_PATH || undefined,
  });
  try {
    for (const theme of ['light', 'dark']) {
      for (const state of ['loaded', 'empty']) {
        const context = await browser.newContext({
          viewport: { width: 1180, height: 980 },
          colorScheme: theme,
          serviceWorkers: 'block',
        });
        try {
          const page = await context.newPage();
          await preparePage(page, theme, fixtureResponder(dist, fixture, state));
          if (state === 'loaded') {
            await page.getByText('Versions and runtime', { exact: true }).waitFor({ timeout: 15_000 });
            await page.getByRole('button', { name: 'Preview' }).first().click();
          } else {
            await page.getByText(/No diagnostic data is available yet/).waitFor({ timeout: 15_000 });
          }
          await page.waitForTimeout(250);
          const name = state === 'loaded' ? `support-bundle-dialog-${theme}.png` : `support-bundle-empty-${theme}.png`;
          await page.screenshot({ path: path.join(out, name), fullPage: true, timeout: 120_000 });
        } finally {
          await context.close();
        }
      }
    }
  } finally {
    await browser.close().catch(() => {});
  }
  assertSupportBundleCaptures(out);
  return supportBundleCaptureFiles;
}

async function main() {
  const argv = process.argv.slice(2);
  const dist = path.resolve(arg(argv, '--dist') || 'dist');
  const out = path.resolve(arg(argv, '--out') || '../docs/screenshots');
  const fixturePath = path.resolve(arg(argv, '--fixture') || 'testdata/support-bundle-2026-09-06.json');
  try {
    if (argv.includes('--check')) {
      assertSupportBundleCaptures(out);
      process.stdout.write(`${JSON.stringify({ captures: supportBundleCaptureFiles, checked: out })}\n`);
      return;
    }
    const captures = await captureSupportBundle({ dist, out, fixturePath });
    process.stdout.write(`${JSON.stringify({ captures, fixture: fixturePath })}\n`);
  } catch (error) {
    process.stderr.write(`${error instanceof Error ? error.stack || error.message : String(error)}\n`);
    process.exitCode = 1;
  }
}

if (import.meta.url === pathToFileURL(process.argv[1] || '').href) await main();
