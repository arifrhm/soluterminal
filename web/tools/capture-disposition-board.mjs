#!/usr/bin/env node

// Deterministic light/dark evidence capture for the disposition-tier Tasks
// board. Every request is fulfilled in-process, so the same committed fixture
// can drive a base bundle and a repaired bundle without touching a live daemon.

import fs from 'node:fs';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { chromium } from 'playwright';
import { ensureBrowserCanStart } from './browser-preflight.mjs';

function arg(name) {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : '';
}

const dist = path.resolve(arg('--dist'));
const out = path.resolve(arg('--out'));
const prefix = arg('--prefix');
const fixturePath = path.resolve(arg('--fixture') || 'testdata/board-2026-08-24.json');
const verifyDisposition = process.argv.includes('--verify-disposition');
const pdfRaster = process.argv.includes('--pdf-raster');
if (!dist || !out || !prefix) {
  process.stderr.write('usage: capture-disposition-board.mjs --dist <dir> --out <dir> --prefix <before|after> [--fixture <json>] [--verify-disposition] [--pdf-raster]\n');
  process.exit(2);
}

const fixture = JSON.parse(fs.readFileSync(fixturePath, 'utf8'));
const mime = {
  '.css': 'text/css; charset=utf-8', '.html': 'text/html; charset=utf-8',
  '.ico': 'image/x-icon', '.js': 'text/javascript; charset=utf-8',
  '.json': 'application/json; charset=utf-8', '.png': 'image/png',
  '.svg': 'image/svg+xml', '.woff': 'font/woff', '.woff2': 'font/woff2',
};

async function fulfill(route) {
  const request = route.request();
  const url = new URL(request.url());
  const pathname = url.pathname;
  const json = (value, status = 200) => route.fulfill({
    status, contentType: 'application/json; charset=utf-8', body: JSON.stringify(value),
  });
  if (request.method() === 'GET' && ['/api/ui/events', '/api/events'].includes(pathname)) {
    return route.fulfill({ status: 200, contentType: 'text/event-stream', body: ': disposition board fixture\n\n' });
  }
  if (pathname === '/api/health') return json({ status: 'ok', version: 'board-fixture', webVersion: 'board-fixture', panes: 0 });
  if (pathname === '/api/workflow/tasks') return json(fixture.workflowTasks);
  if (pathname === '/api/workflow/inbox') return json(fixture.workflowInbox);
  if (pathname === '/api/system/status') return json(fixture.systemStatus);
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
}

function rasterize(pdf, png) {
  execFileSync('python3', ['-c', [
    'import fitz,sys',
    'doc=fitz.open(sys.argv[1])',
    'pix=doc[0].get_pixmap(dpi=96, alpha=False)',
    'pix.save(sys.argv[2])',
  ].join(';'), pdf, png]);
}

fs.mkdirSync(out, { recursive: true });
let browser;
try {
  await ensureBrowserCanStart();
  browser = await chromium.launch({
    headless: true,
    executablePath: process.env.PLAYWRIGHT_CHROMIUM_PATH || undefined,
    args: pdfRaster ? ['--disable-crash-reporter', '--disable-crashpad', '--single-process', '--no-zygote'] : undefined,
  });
  const captures = [];
  for (const theme of ['light', 'dark']) {
    const context = await browser.newContext({
      viewport: { width: 1440, height: 1600 },
      colorScheme: theme,
      serviceWorkers: 'block',
    });
    try {
      const page = await context.newPage();
      await page.route('**/*', fulfill);
      await page.addInitScript((selectedTheme) => {
        localStorage.setItem('solu.appTheme', selectedTheme);
        localStorage.setItem('solu.seenWelcome', '1');
        localStorage.setItem('solu.seenChecklist', '1');
        localStorage.setItem('solu.tasks.seenCrewExplainer', '1');
        localStorage.setItem('solu.tasks.view', 'board');
        localStorage.setItem('solu.tasks.board.mode', 'tree');
        localStorage.setItem('solu.tasks.historyOpen', '1');
      }, theme);
      await page.goto('http://localhost/#view=tasks', { waitUntil: 'load' });
      await page.emulateMedia({ media: 'screen', colorScheme: theme });
      await page.locator('.tasks-surface .tasks-board').waitFor({ timeout: 15_000 });
      await page.getByText('Tasks board disposition tiers', { exact: true }).first().waitFor({ timeout: 15_000 });
      await page.waitForTimeout(500);

      if (verifyDisposition) {
        const judgment = page.locator('.tasks-group.is-decide');
        if ((await judgment.locator('.tasks-group-label > span').textContent())?.trim() !== '3') {
          throw new Error('Needs your judgment did not contain exactly three tasks');
        }
        for (const title of [
          'Choose product behavior for ambiguous recovery',
          'Review deny-listed merge approval',
          'Resolve dead successor conflict',
        ]) {
          if (await judgment.getByText(title, { exact: true }).count() !== 1) {
            throw new Error(`judgment fixture row missing or duplicated: ${title}`);
          }
        }
        const automation = page.locator('.tasks-group.is-automation');
        if ((await automation.locator('.tasks-group-label > span').textContent())?.trim() !== '7') {
          throw new Error('Automation in progress did not contain exactly seven tasks');
        }
        for (const title of [
          'Resolve after proof-backed engine closure',
          'Accept refile by policy',
        ]) {
          if (await automation.getByText(title, { exact: true }).count() !== 1) {
            throw new Error(`automation fixture row missing or duplicated: ${title}`);
          }
          if (await judgment.getByText(title, { exact: true }).count() !== 0) {
            throw new Error(`automation countdown leaked into judgment: ${title}`);
          }
        }
        const muted = page.locator('.tasks-group.is-history .tasks-chip.is-muted').first();
        await muted.waitFor();
        const colors = await muted.evaluate((chip) => {
          const sample = document.createElement('span');
          document.body.appendChild(sample);
          sample.style.color = 'var(--danger)';
          const danger = getComputedStyle(sample).color;
          sample.style.color = 'var(--fg-dim)';
          const dim = getComputedStyle(sample).color;
          sample.remove();
          return { actual: getComputedStyle(chip).color, danger, dim };
        });
        if (colors.actual !== colors.dim || colors.actual === colors.danger) {
          throw new Error(`history chip is not visually muted: ${JSON.stringify(colors)}`);
        }
      }

      const file = `${prefix}-${theme}.png`;
      const output = path.join(out, file);
      if (pdfRaster) {
        const pdf = path.join(out, `.${prefix}-${theme}.pdf`);
        await page.pdf({ path: pdf, width: '1440px', height: '1600px', printBackground: true, timeout: 120_000 });
        try { rasterize(pdf, output); } finally { fs.rmSync(pdf, { force: true }); }
      } else {
        await page.screenshot({ path: output, fullPage: true, timeout: 120_000 });
      }
      captures.push(file);
    } finally {
      await context.close();
    }
  }
  process.stdout.write(`${JSON.stringify({ captures, fixture: fixturePath })}\n`);
} catch (error) {
  process.stderr.write(`${error instanceof Error ? error.stack || error.message : String(error)}\n`);
  process.exitCode = 1;
} finally {
  if (browser) await browser.close().catch(() => {});
}
