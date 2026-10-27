#!/usr/bin/env node
// Deterministic rendered-UI smoke gate. Exit 78 means the pinned Playwright
// module is missing/incompatible; exit 79 means Chromium is not installed.
// A default web/dist whose webdist.json sourceSha no longer matches HEAD is rebuilt before the run (the verify lane re-runs in the same worktree after review repairs and landing rebases); --dist copies are served as-is.
//
// App-shell breakage repro (run from web/):
// cp -r dist /tmp/broken && sed -i 's/id="root"/id="broken"/' /tmp/broken/index.html && node tools/e2e-smoke.mjs --dist /tmp/broken
// Additional surface repros use a disposable checkout: rename id="sidebar"
// in App.tsx (Sidebar breakage repro), "settings-modal" in SettingsModal.tsx
// (Settings breakage repro), or "system-page" in SystemPage.tsx (System breakage repro), or "system-kpis" in
// SystemPage.tsx (System cockpit data breakage repro), then
// build and run this command against that checkout.
// Context-pack breakage repro: use the negative repro in capture-context-pack.mjs.

import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { createRequire } from 'node:module';
import { fileURLToPath, pathToFileURL } from 'node:url';
import {
  buildServiceFixtures,
  createServer,
  exitBrowserUnavailable,
  launchChromium,
  routeServiceFixtures,
} from './screens-lib.mjs';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const webDir = path.resolve(scriptDir, '..');
const repoDir = path.resolve(webDir, '..');
const defaultDist = path.join(webDir, 'dist');
const playwrightMajor = 1;

// Keep this empty unless a console error is confirmed to be harmless on main.
// Page errors are intentionally never allowlisted.
export const consoleErrorAllowlist = [];

export const smokeSurfaceSelectors = Object.freeze({
  appShell: '.app',
  sidebar: '#sidebar',
  sidebarSection: '.sidebar-section',
  settingsTrigger: 'button[aria-label="Settings"]',
  settingsModal: '.settings-modal[role="dialog"]',
  settingsTab: '.settings-nav [role="tab"]',
  systemPage: '.system-page[role="dialog"]',
  systemKpis: '.system-kpis',
  systemResources: '.system-resources',
  systemPlaceholder: '.system-state',
});

function usage(detail = '') {
  const message = `${detail ? `${detail}\n` : ''}usage: e2e-smoke.mjs [--dist <dir>] [--build] [--theme light|dark]\n`;
  fs.writeSync(process.stderr.fd, message);
  process.exit(2);
}

export function parseArgs(argv = process.argv.slice(2)) {
  let dist = defaultDist;
  let distExplicit = false;
  let build = false;
  let theme = '';
  for (let index = 0; index < argv.length; index += 1) {
    const current = argv[index];
    if (current === '--build') {
      build = true;
      continue;
    }
    if (current === '--dist' || current === '--theme') {
      const value = argv[index + 1];
      if (!value || value.startsWith('--')) usage(`missing value for ${current}`);
      index += 1;
      if (current === '--dist') {
        dist = path.resolve(value);
        distExplicit = true;
      }
      else theme = value;
      continue;
    }
    usage(`unknown argument: ${current}`);
  }
  if (theme && !['light', 'dark'].includes(theme)) usage(`unknown smoke theme: ${theme}`);
  if (build && distExplicit) usage('--build cannot be combined with --dist; build web/dist, then copy it to the custom directory');
  return { dist, distExplicit, build, theme };
}

function validatePlaywrightPackage(dir) {
  let raw;
  try {
    const info = fs.statSync(dir);
    if (!info.isDirectory()) return 'not a directory';
    raw = fs.readFileSync(path.join(dir, 'package.json'), 'utf8');
  } catch (error) {
    if (error && typeof error === 'object' && error.code === 'ENOENT') return 'not found';
    return 'no readable package.json';
  }
  let pkg;
  try {
    pkg = JSON.parse(raw);
  } catch {
    return 'invalid package.json';
  }
  if (!['playwright', 'playwright-core'].includes(pkg.name)) {
    return `package ${JSON.stringify(pkg.name)} is not playwright`;
  }
  const major = Number.parseInt(String(pkg.version).split('.')[0], 10);
  if (major !== playwrightMajor) {
    return `version ${JSON.stringify(pkg.version)} is not major ${playwrightMajor}`;
  }
  return '';
}

async function importPlaywrightDirectory(dir) {
  const require = createRequire(import.meta.url);
  const entry = require.resolve(dir);
  const imported = await import(pathToFileURL(entry).href);
  const playwright = imported.chromium ? imported : imported.default;
  if (!playwright?.chromium) throw new Error('package entry does not export chromium');
  return playwright;
}

export function playwrightCandidates() {
  const raw = [
    process.env.PLAYWRIGHT_MODULE_PATH,
    path.join(webDir, 'node_modules', 'playwright'),
    path.join(webDir, 'node_modules', 'playwright-core'),
  ].filter(Boolean);
  return [...new Set(raw.map((candidate) => (
    path.isAbsolute(candidate) ? path.normalize(candidate) : path.resolve(webDir, candidate)
  )))];
}

export async function resolveSmokePlaywright(candidates = playwrightCandidates()) {
  const attempts = [];
  for (const dir of candidates) {
    const reason = validatePlaywrightPackage(dir);
    if (reason) {
      attempts.push({ dir, reason });
      continue;
    }
    try {
      const playwright = await importPlaywrightDirectory(dir);
      return { playwright, dir, attempts };
    } catch (error) {
      attempts.push({
        dir,
        reason: `cannot load package: ${error instanceof Error ? error.message : String(error)}`,
      });
    }
  }
  return { playwright: null, dir: '', attempts };
}

function exitPlaywrightUnavailable(attempts) {
  const lines = [
    `Playwright module missing or incompatible (requires major ${playwrightMajor}); tried:`,
    ...attempts.map(({ dir, reason }) => `- ${dir}: ${reason}`),
  ];
  fs.writeSync(process.stderr.fd, `${lines.join('\n')}\n`);
  process.exit(78);
}

function currentSourceSha() {
  const result = spawnSync('git', ['rev-parse', '--verify', 'HEAD'], {
    cwd: repoDir,
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  if (result.error) throw new Error(`cannot determine current HEAD: ${result.error.message}`);
  const sha = result.stdout.trim();
  if (result.status !== 0 || !/^[0-9a-f]+$/i.test(sha)) {
    throw new Error(`cannot determine current HEAD: ${shortError(result.stderr) || `git exited ${result.status ?? 'without a status'}`}`);
  }
  return sha;
}

export function assertCurrentDistProvenance(dist, expectedSha = currentSourceSha()) {
  const manifest = path.join(dist, 'webdist.json');
  let provenance;
  try {
    provenance = JSON.parse(fs.readFileSync(manifest, 'utf8'));
  } catch (error) {
    throw new Error(`web bundle provenance mismatch: cannot read ${manifest}: ${shortError(error)}; rebuild with "npm run build" or pass --build`);
  }
  const actualSha = typeof provenance.sourceSha === 'string' ? provenance.sourceSha.trim() : '';
  if (!actualSha || actualSha.toLowerCase() !== expectedSha.toLowerCase()) {
    throw new Error(`web bundle provenance mismatch: ${manifest} sourceSha is ${JSON.stringify(actualSha || '(missing)')}, current HEAD is ${expectedSha}; rebuild with "npm run build" or pass --build`);
  }
  return actualSha;
}

export function staleDefaultDistReason(dist, distExplicit, build) {
  if (build || distExplicit || !fs.existsSync(path.join(dist, 'index.html'))) return '';
  try {
    assertCurrentDistProvenance(dist);
    return '';
  } catch (error) {
    if (error instanceof Error && error.message.startsWith('web bundle provenance mismatch')) {
      return shortError(error);
    }
    throw error;
  }
}

export function ensureDist(dist, forceBuild, distExplicit) {
  const index = path.join(dist, 'index.html');
  const manifest = path.join(dist, 'webdist.json');
  if (!forceBuild && fs.existsSync(index)) {
    if (!fs.existsSync(manifest)) {
      if (distExplicit) {
        usage(`custom --dist must contain webdist.json: ${manifest}; build web/dist first, then copy the complete directory`);
      }
    } else {
      if (!distExplicit) assertCurrentDistProvenance(dist);
      process.stdout.write(`Reusing web build at ${dist}\n`);
      return;
    }
  }

  if (distExplicit) {
    usage(`custom --dist must contain index.html: ${index}; build web/dist first, then copy it to the custom directory`);
  }

  process.stdout.write(`Building web UI into ${defaultDist}\n`);
  const npm = process.platform === 'win32' ? 'npm.cmd' : 'npm';
  const result = spawnSync(npm, ['run', 'build'], { cwd: webDir, env: process.env, stdio: 'inherit' });
  if (result.error) throw new Error(`web build could not start: ${result.error.message}`);
  if (result.status !== 0) throw new Error(`web build failed with exit code ${result.status ?? 'unknown'}`);
  if (!fs.existsSync(index)) {
    throw new Error(`web build completed but ${index} does not exist`);
  }
  if (!fs.existsSync(manifest)) {
    throw new Error(`web build completed but ${manifest} does not exist`);
  }
  assertCurrentDistProvenance(dist);
}

export function readDistWebVersion(dist) {
  const manifest = path.join(dist, 'webdist.json');
  try {
    const value = JSON.parse(fs.readFileSync(manifest, 'utf8'));
    if (typeof value.webVersion === 'string' && value.webVersion.trim()) return value.webVersion.trim();
  } catch (error) {
    throw new Error(`cannot read webVersion from ${manifest}: ${shortError(error)}`);
  }
  throw new Error(`${manifest} does not contain a non-empty webVersion`);
}

export async function ensureCredentialsKeysSmokeEvidence(currentDist, {
  browser,
  loadCapture = () => import('./capture-credentials-keys.mjs'),
  write = (message) => process.stdout.write(message),
} = {}) {
  const { ensureCredentialsKeysEvidence } = await loadCapture();
  const options = browser ? { currentDist, browser } : { currentDist };
  const evidence = await ensureCredentialsKeysEvidence(options);
  write(
    `Credentials API-key evidence verified: ${evidence.captures.join(', ')}`
      + `${evidence.created ? ' (captured)' : ''}\n`,
  );
  return evidence;
}

function shortError(error) {
  const message = error instanceof Error ? error.message : String(error);
  return message.replace(/\s+/g, ' ').trim();
}

async function recordAssertion(failures, theme, surface, assertion) {
  try {
    const detail = await assertion();
    process.stdout.write(`PASS [${theme}] ${surface}: ${detail}\n`);
  } catch (error) {
    const detail = shortError(error);
    failures.push({ theme, surface, detail });
    process.stderr.write(`FAIL [${theme}] ${surface}: ${detail}\n`);
  }
}

function attachPageErrors(context, errors) {
  const attached = new WeakSet();
  const attach = (page) => {
    if (attached.has(page)) return;
    attached.add(page);
    page.on('console', (message) => {
      if (message.type() === 'error') errors.push({ kind: 'console', detail: message.text() });
    });
    page.on('pageerror', (error) => {
      errors.push({ kind: 'page', detail: error instanceof Error ? error.message : String(error) });
    });
  };
  context.on('page', attach);
  return attach;
}

export async function installStoragePreferences(page, theme) {
  await page.addInitScript((selectedTheme) => {
    localStorage.setItem('solu.appTheme', selectedTheme);
    localStorage.setItem('solu.seenWelcome', '1');
    localStorage.setItem('solu.seenChecklist', '1');
  }, theme);
}

export async function installTauriShell(page) {
  await page.addInitScript(() => {
    window.__SOLU_TAURI__ = true;
    window.__TAURI_INTERNALS__ = { invoke: async () => null };
  });
}

export async function openSmokePage(context, attach, theme, url, tauriShell = false) {
  const page = await context.newPage();
  attach(page);
  await installStoragePreferences(page, theme);
  if (tauriShell) await installTauriShell(page);
  page.setDefaultTimeout(5_000);
  page.setDefaultNavigationTimeout(15_000);
  await page.goto(url, { waitUntil: 'load' });
  return page;
}

export async function assertSystemCockpit(page) {
  const chip = page.locator('button.statusbar-system');
  await visible(chip, 15_000);
  await chip.click();
  const dialog = page.locator(smokeSurfaceSelectors.systemPage);
  await visible(dialog);
  const kpis = dialog.locator(smokeSurfaceSelectors.systemKpis);
  const resources = dialog.locator(smokeSurfaceSelectors.systemResources);

  async function visibleCockpitPart(locator, selector, timeout = 5_000) {
    try {
      await locator.first().waitFor({ state: 'visible', timeout });
    } catch (error) {
      const placeholder = await dialog
        .locator(`${smokeSurfaceSelectors.systemPlaceholder} h3`)
        .textContent({ timeout: 500 })
        .catch(() => '');
      throw new Error(
        `System cockpit did not render ${selector} within ${timeout}ms; dialog shows ${placeholder ? JSON.stringify(placeholder) : 'no placeholder'}: ${shortError(error)}`,
      );
    }
  }

  await visibleCockpitPart(kpis, smokeSurfaceSelectors.systemKpis, 15_000);
  await visibleCockpitPart(resources, smokeSurfaceSelectors.systemResources);
  const producers = dialog.locator('.system-producers');
  const cleanup = dialog.locator('.system-cleanup');
  await visibleCockpitPart(producers, '.system-producers');
  await visibleCockpitPart(cleanup, '.system-cleanup');
  const producerText = await producers.innerText();
  for (const token of ['Workflow worktrees', 'Legacy or external Go build cache', 'Owned Go build cache', 'External user cache', 'Protected recovery data']) {
    if (!producerText.includes(token)) throw new Error(`System producers surface is missing ${token}`);
  }
  // innerText reflects the badge's uppercase CSS transformation. Assert the
  // exact semantic policy and cleanup owner per row, independently of casing.
  for (const [label, policy, owner] of [
    ['Workflow worktrees', 'managed', 'workflow_gc'],
    ['Legacy or external Go build cache', 'read-only', null],
    ['Owned Go build cache', 'managed', 'generated_storage_janitor'],
    ['External user cache', 'read-only', null],
    ['Protected recovery data', 'protected', null],
  ]) {
    const row = producers.locator('tbody tr').filter({ has: page.getByRole('cell', { name: label, exact: true }) });
    const badge = row.locator(`.system-policy.is-${policy}`);
    await visible(badge);
    if ((await badge.textContent())?.trim() !== policy) throw new Error(`Wrong storage policy for ${label}`);
    const policyText = (await row.locator('td').nth(1).textContent())?.trim();
    const expected = owner ? `${policy} · ${owner}` : policy;
    if (policyText !== expected) throw new Error(`Wrong cleanup owner for ${label}: ${policyText}`);
  }
  const cleanupText = await cleanup.innerText();
  for (const token of ['Linux space freed; Windows image still allocated; maintenance needed', 'preview']) {
    if (!cleanupText.includes(token)) throw new Error(`Cleanup surface is missing ${token}`);
  }
  const desktopWidth = await dialog.evaluate((element) => element.getBoundingClientRect().width);
  if (desktopWidth < 900) throw new Error(`desktop cockpit width ${desktopWidth}px is unexpectedly narrow`);
  await page.setViewportSize({ width: 700, height: 900 });
  const narrow = await dialog.evaluate((element, selectors) => {
    const pick = (selector) => {
      const node = element.querySelector(selector);
      if (!(node instanceof Element)) {
        throw new Error(`${selector} is missing inside .system-page[role="dialog"]`);
      }
      return node;
    };
    const columns = (selector) => getComputedStyle(pick(selector)).gridTemplateColumns.split(' ').length;
    return {
      width: element.getBoundingClientRect().width,
      shellOverflow: element.scrollWidth > element.clientWidth,
      kpiColumns: columns(selectors.kpis),
      resourceColumns: columns(selectors.resources),
    };
  }, {
    kpis: smokeSurfaceSelectors.systemKpis,
    resources: smokeSurfaceSelectors.systemResources,
  });
  if (narrow.width > 700 || narrow.shellOverflow || narrow.kpiColumns !== 1 || narrow.resourceColumns !== 1) {
    throw new Error(`700px layout is not a single non-overflowing sheet: ${JSON.stringify(narrow)}`);
  }
  await page.keyboard.press('Escape');
  await dialog.waitFor({ state: 'hidden' });
  return `cockpit rendered at ${Math.round(desktopWidth)}px and collapsed cleanly at 700px`;
}

export function unexpectedRuntimeErrors(errors) {
  return errors.filter(({ kind, detail }) => (
    kind === 'page' || !consoleErrorAllowlist.some((pattern) => pattern.test(detail))
  ));
}

function visible(locator, timeout = 5_000) {
  return locator.waitFor({ state: 'visible', timeout });
}

async function smokeTheme(browser, baseUrl, theme, fixtures, failures) {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
    serviceWorkers: 'block',
  });
  const runtimeErrors = [];
  try {
    await routeServiceFixtures(context, fixtures);
    const attach = attachPageErrors(context, runtimeErrors);
    let page;

    await recordAssertion(failures, theme, 'app shell', async () => {
      page = await openSmokePage(context, attach, theme, baseUrl);
      await visible(page.locator(smokeSurfaceSelectors.appShell), 15_000);
      return '.app mounted and is visible';
    });

    await recordAssertion(failures, theme, 'sidebar', async () => {
      const sidebar = page.locator(smokeSurfaceSelectors.sidebar);
      await visible(sidebar);
      const sections = sidebar.locator(smokeSurfaceSelectors.sidebarSection);
      await sections.first().waitFor({ state: 'attached' });
      const count = await sections.count();
      if (count < 1) throw new Error('expected at least one .sidebar-section');
      return `#sidebar is visible with ${count} section(s)`;
    });

    await recordAssertion(failures, theme, 'settings', async () => {
      const trigger = page.locator(smokeSurfaceSelectors.settingsTrigger);
      await visible(trigger);
      await trigger.click();
      const modal = page.locator(smokeSurfaceSelectors.settingsModal);
      await visible(modal);
      const tabs = modal.locator(smokeSurfaceSelectors.settingsTab);
      await tabs.first().waitFor({ state: 'visible' });
      const count = await tabs.count();
      if (count < 1) throw new Error('expected at least one tab in .settings-nav');
      await page.keyboard.press('Escape');
      await modal.waitFor({ state: 'hidden' });
      return `modal opened with ${count} tab(s) and closed on Escape`;
    });

    await recordAssertion(failures, theme, 'system', async () => assertSystemCockpit(
      await openSmokePage(context, attach, theme, baseUrl),
    ));

    await recordAssertion(failures, theme, 'console', async () => {
      await Promise.all(context.pages().map((currentPage) => currentPage.waitForTimeout(250)));
      const unexpected = unexpectedRuntimeErrors(runtimeErrors);
      if (unexpected.length > 0) {
        throw new Error(unexpected.map(({ kind, detail }) => `${kind}: ${detail}`).join(' | '));
      }
      return 'no console errors or page errors';
    });
  } finally {
    await context.close().catch(() => {});
  }
}

export async function main() {
  const options = parseArgs();
  try {
    const stale = staleDefaultDistReason(options.dist, options.distExplicit, options.build);
    if (stale) process.stdout.write(`Stale web build at ${options.dist}: ${stale}; rebuilding\n`);
    ensureDist(options.dist, options.build || !!stale, options.distExplicit);
  } catch (error) {
    process.stderr.write(`UI smoke could not run: ${shortError(error)}\n`);
    process.exitCode = 1;
    return;
  }

  const resolved = await resolveSmokePlaywright();
  if (!resolved.playwright) exitPlaywrightUnavailable(resolved.attempts);

  let executable;
  try {
    executable = resolved.playwright.chromium.executablePath();
  } catch (error) {
    exitBrowserUnavailable(`chromium executablePath failed: ${shortError(error)}`);
  }
  if (!executable || !fs.existsSync(executable)) {
    exitBrowserUnavailable(`chromium executable not found at ${executable || '(unresolved)'}`);
  }

  let browser;
  let server;
  const failures = [];
  try {
    // Launch before opening the local server so missing-browser failures are
    // classified deterministically even when listeners are restricted.
    browser = await launchChromium(resolved.playwright, { headless: true });
    server = createServer(options.dist, { healthVersion: readDistWebVersion(options.dist) });
    await new Promise((resolve, reject) => {
      server.once('error', reject);
      server.listen(0, '127.0.0.1', resolve);
    });
    const address = server.address();
    if (!address || typeof address === 'string') throw new Error('static server did not allocate a TCP port');
    const baseUrl = `http://127.0.0.1:${address.port}/`;
    const fixtures = buildServiceFixtures(Date.now());
    const themes = options.theme ? [options.theme] : ['dark', 'light'];
    for (const theme of themes) {
      await smokeTheme(browser, baseUrl, theme, fixtures, failures);
    }

    if (failures.length > 0) {
      process.stderr.write(`UI smoke failed: ${failures.length} of ${themes.length * 5} assertions failed\n`);
      process.exitCode = 1;
    } else {
      const evidenceScript = path.join(scriptDir, 'capture-disposition-evidence.mjs');
      if (fs.existsSync(evidenceScript)) {
        const { ensureDispositionEvidence } = await import(pathToFileURL(evidenceScript).href);
        const evidence = ensureDispositionEvidence({ currentDist: options.dist });
        if (evidence.created) {
          process.stdout.write(`Disposition board evidence captured: ${evidence.captures.join(', ')}\n`);
        }
      }
      const packScript = path.join(scriptDir, 'capture-context-pack.mjs');
      if (fs.existsSync(packScript)) {
        const { ensureContextPackEvidence } = await import(pathToFileURL(packScript).href);
        const packEvidence = await ensureContextPackEvidence({ currentDist: options.dist });
        process.stdout.write(`Context pack evidence verified: ${packEvidence.captures.join(', ')}${packEvidence.created ? ' (captured)' : ''}\n`);
      }
      const credentialsKeysScript = path.join(scriptDir, 'capture-credentials-keys.mjs');
      if (fs.existsSync(credentialsKeysScript)) {
        await ensureCredentialsKeysSmokeEvidence(options.dist, { browser });
      }
      process.stdout.write(`UI smoke passed: ${themes.length * 5} assertions across ${themes.length} theme(s)\n`);
    }
  } catch (error) {
    const detail = shortError(error);
    if (detail.includes("Executable doesn't exist")) {
      exitBrowserUnavailable(detail);
    }
    process.stderr.write(`UI smoke could not run: ${detail}\n`);
    process.exitCode = 1;
  } finally {
    if (browser) await browser.close().catch(() => {});
    if (server) await new Promise((resolve) => server.close(resolve));
  }
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) {
  await main();
}
