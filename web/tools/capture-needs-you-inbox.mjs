#!/usr/bin/env node

// Capture the production Tasks bundle against a side-port Go test daemon.
// The daemon seeds the real workflow event store and serves the ordinary
// workflow list/inbox/detail handlers; this script never manufactures API
// payloads or patches the rendered DOM.

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
if (!arg('--dist') || !arg('--out')) {
  process.stderr.write('usage: capture-needs-you-inbox.mjs --dist <webdist dir> --out <png dir>\n');
  process.exit(2);
}
if (!fs.existsSync(path.join(dist, 'index.html'))) {
  process.stderr.write(`built web bundle not found at ${dist}\n`);
  process.exit(2);
}

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(scriptDir, '..', '..');
const daemonDir = path.join(repoRoot, 'daemon');
const scenarios = {
  'decide-now': { title: 'Decide now' },
  'auto-recovering-conflict': { title: 'Recovering automatically' },
  'dependency-wait': { title: 'Waiting' },
  'superseded-lineage': { title: 'History', expandLineage: true },
  'exhausted-recovery': { title: 'Decide now' },
};

function startEvidenceDaemon(scenario) {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), `solu-needs-you-${scenario}-`));
  const child = spawn('go', ['test', '-run', '^TestNeedsYouEvidenceServer$', '-count=1', '-v'], {
    cwd: daemonDir,
    env: {
      ...process.env,
      SOLU_NEEDS_YOU_EVIDENCE_ADDR: '127.0.0.1:0',
      SOLU_NEEDS_YOU_EVIDENCE_DATA: dataDir,
      SOLU_NEEDS_YOU_EVIDENCE_DIST: dist,
      SOLU_NEEDS_YOU_EVIDENCE_SCENARIO: scenario,
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
    child.once('exit', (code, signal) => {
      clearTimeout(timeout);
      reject(new Error(`evidence daemon exited before ready (${code ?? signal})\n${output}`));
    });
    child.once('error', (error) => {
      clearTimeout(timeout);
      reject(error);
    });
  });
  const stop = async () => {
    if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM');
    await Promise.race([
      new Promise((resolve) => child.once('exit', resolve)),
      new Promise((resolve) => setTimeout(resolve, 3000)),
    ]);
    fs.rmSync(dataDir, { recursive: true, force: true });
  };
  return { ready, stop };
}

fs.mkdirSync(out, { recursive: true });
await ensureBrowserCanStart();
const browser = await chromium.launch({
  headless: true,
  chromiumSandbox: false,
  executablePath: process.env.PLAYWRIGHT_CHROMIUM_PATH || undefined,
  // --single-process/--no-zygote crash on repeated newContext (GpuControl
  // transient failures) and never helped the sandboxed worker anyway (its
  // Chromium is blocked before startup entirely).
  args: ['--allow-file-access-from-files', '--disable-crash-reporter', '--disable-crashpad'],
});
const shots = [];
try {
  for (const [scenarioName, scenario] of Object.entries(scenarios)) {
    const server = startEvidenceDaemon(scenarioName);
    try {
      const baseURL = await server.ready;
      for (const variant of ['light', 'dark', 'narrow']) {
        const selectedTheme = variant === 'dark' ? 'dark' : 'light';
        const viewport = variant === 'narrow' ? { width: 560, height: 1000 } : { width: 1440, height: 1000 };
        const context = await browser.newContext({ viewport, serviceWorkers: 'block' });
        try {
          const page = await context.newPage();
          await page.addInitScript(({ theme, openHistory }) => {
            localStorage.setItem('solu.appTheme', theme);
            localStorage.setItem('solu.seenWelcome', '1');
            localStorage.setItem('solu.seenChecklist', '1');
            localStorage.setItem('solu.tasks.view', 'board');
            localStorage.setItem('solu.tasks.board.mode', 'tree');
            localStorage.setItem('solu.tasks.historyOpen', openHistory ? '1' : '0');
            // The lineage scenario's prior attempt is archived; chains hide
            // archived priors unless the archived filter is on.
            if (openHistory) localStorage.setItem('solu.tasks.showArchived', '1');
          }, { theme: selectedTheme, openHistory: !!scenario.expandLineage });
          await page.goto(`${baseURL}/#view=tasks`, { waitUntil: 'load' });
          await page.evaluate((theme) => {
            if (theme === 'light') document.documentElement.setAttribute('data-theme', 'light');
            else document.documentElement.removeAttribute('data-theme');
          }, selectedTheme);
          // Match only a section-header element, including its numeric count.
          // Raw text can also occur in task copy (notably "Waiting").
          const escapedTitle = scenario.title.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
          await page.locator('.tasks-group-label, .section-title')
            .filter({ hasText: new RegExp(`^${escapedTitle}\\s*\\d+$`, 'i') })
            .first()
            .waitFor({ timeout: 15000 });
          if (scenario.expandLineage) {
            await page.getByRole('button', { name: /earlier attempt/ }).click();
            await page.getByText('Original workflow inbox attempt', { exact: true }).waitFor();
          }
          await page.waitForTimeout(250);
          const file = `${scenarioName}-${variant}.png`;
          await page.screenshot({ path: path.join(out, file), fullPage: true });
          shots.push({ label: `${scenarioName} (${variant})`, file });
        } finally {
          await context.close();
        }
      }
    } finally {
      await server.stop();
    }
  }
} finally {
  await browser.close();
}
process.stdout.write(`${JSON.stringify({ shots }, null, 2)}\n`);
