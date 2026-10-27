#!/usr/bin/env node

import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { ensureBrowserCanStart } from './browser-preflight.mjs';

function arg(name) {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : '';
}

const dist = path.resolve(arg('--dist'));
const out = path.resolve(arg('--out'));
if (!arg('--dist') || !arg('--out') || !fs.existsSync(path.join(dist, 'index.html'))) {
  process.stderr.write('usage: capture-documents-decisions.mjs --dist <webdist dir> --out <png dir>\n');
  process.exit(2);
}

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(scriptDir, '..', '..');
const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'solu-documents-evidence-'));
const child = spawn(process.env.SOLU_GO_BINARY || 'go', ['test', '-run', '^TestNeedsYouEvidenceServer$', '-count=1', '-v'], {
  cwd: path.join(repoRoot, 'daemon'),
  env: {
    ...process.env,
    GOCACHE: process.env.GOCACHE || path.join(os.tmpdir(), 'solu-go-cache'),
    SOLU_NEEDS_YOU_EVIDENCE_ADDR: '127.0.0.1:0',
    SOLU_NEEDS_YOU_EVIDENCE_DATA: dataDir,
    SOLU_NEEDS_YOU_EVIDENCE_DIST: dist,
    SOLU_NEEDS_YOU_EVIDENCE_SCENARIO: 'documents',
  },
  stdio: ['ignore', 'pipe', 'pipe'],
});

let output = '';
const ready = new Promise((resolve, reject) => {
  const timeout = setTimeout(() => reject(new Error(`evidence daemon timed out\n${output}`)), 30000);
  const consume = (chunk) => {
    output += chunk.toString();
    const match = output.match(/NEEDS_YOU_EVIDENCE_URL=(http:\/\/[^\s]+)/);
    if (match) {
      clearTimeout(timeout);
      resolve(match[1]);
    }
  };
  child.stdout.on('data', consume);
  child.stderr.on('data', consume);
  child.once('exit', (code, signal) => reject(new Error(`evidence daemon exited before ready (${code ?? signal})\n${output}`)));
  child.once('error', reject);
});

fs.mkdirSync(out, { recursive: true });
await ensureBrowserCanStart();
const browser = await chromium.launch({
  headless: true,
  chromiumSandbox: false,
  executablePath: process.env.PLAYWRIGHT_CHROMIUM_PATH || undefined,
  args: ['--allow-file-access-from-files', '--disable-crash-reporter', '--disable-crashpad'],
});
const shots = [];
try {
  const baseURL = await ready;
  for (const variant of ['light', 'dark', 'narrow']) {
    const selectedTheme = variant === 'dark' ? 'dark' : 'light';
    const viewport = variant === 'narrow' ? { width: 560, height: 1000 } : { width: 1440, height: 1000 };
    const context = await browser.newContext({ viewport, serviceWorkers: 'block' });
    try {
      const page = await context.newPage();
      await page.addInitScript((theme) => {
        localStorage.setItem('solu.appTheme', theme);
        localStorage.setItem('solu.seenWelcome', '1');
        localStorage.setItem('solu.seenChecklist', '1');
        localStorage.setItem('solu.tasks.view', 'engine');
      }, selectedTheme);
      await page.goto(`${baseURL}/#view=tasks`, { waitUntil: 'load' });
      await page.evaluate((theme) => {
        if (theme === 'light') document.documentElement.setAttribute('data-theme', 'light');
        else document.documentElement.removeAttribute('data-theme');
      }, selectedTheme);
      await page.getByRole('list', { name: 'Documents' }).waitFor({ timeout: 15000 });
      await page.getByRole('button', { name: /Accessibility review decision/ }).click();
      await page.getByText('Keep document rows keyboard navigable', { exact: false }).waitFor({ timeout: 15000 });
      await page.waitForTimeout(200);
      const file = `documents-decisions-${variant}.png`;
      await page.screenshot({ path: path.join(out, file), fullPage: true });
      shots.push({ label: `Documents & Decisions (${variant})`, file });
    } finally {
      await context.close();
    }
  }
} finally {
  await browser.close();
  if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM');
  await Promise.race([
    new Promise((resolve) => child.once('exit', resolve)),
    new Promise((resolve) => setTimeout(resolve, 3000)),
  ]);
  fs.rmSync(dataDir, { recursive: true, force: true });
}
process.stdout.write(`${JSON.stringify({ shots }, null, 2)}\n`);
