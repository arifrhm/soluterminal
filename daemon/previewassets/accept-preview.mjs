#!/usr/bin/env node

import fs from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';
import { pathToFileURL } from 'node:url';

function arg(name) {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : '';
}

async function resolvePlaywright() {
  for (const candidate of ['playwright', 'playwright-core', process.env.PLAYWRIGHT_MODULE_PATH].filter(Boolean)) {
    const specifiers = [candidate];
    try {
      if (path.isAbsolute(candidate) && fs.statSync(candidate).isDirectory()) {
        specifiers.unshift(pathToFileURL(createRequire(import.meta.url).resolve(candidate)).href);
      }
    } catch { /* try raw candidate */ }
    for (const specifier of specifiers) {
      try {
        const loaded = await import(specifier);
        const value = loaded.chromium ? loaded : loaded.default;
        if (value?.chromium) return value;
      } catch { /* next candidate */ }
    }
  }
  return null;
}

function boundedPush(target, value) {
  if (target.length < 50) target.push(String(value).slice(0, 2048));
}

async function listenProbe(server, ...args) {
  try {
    await new Promise((resolve, reject) => {
      server.once('error', reject);
      server.listen(...args, resolve);
    });
    return 'ok';
  } catch (error) {
    return error?.code || 'UNKNOWN';
  } finally {
    await new Promise((resolve) => server.close(() => resolve())).catch(() => {});
  }
}

async function ensureBrowserCanStart() {
  if (process.env.SOLU_BROWSER_PREFLIGHT === 'skip') {
    process.stderr.write('browser-preflight: skipped by SOLU_BROWSER_PREFLIGHT=skip\n');
    return;
  }
  const socketPath = path.join(os.tmpdir(), `solu-preflight-${process.pid}.sock`);
  const tcp = await listenProbe(net.createServer(), 0, '127.0.0.1');
  const unix = await listenProbe(net.createServer(), socketPath);
  try { fs.unlinkSync(socketPath); } catch (error) { if (error?.code !== 'ENOENT') throw error; }
  if (tcp !== 'EPERM' && unix !== 'EPERM') return;
  let seccomp = null;
  try { seccomp = fs.readFileSync('/proc/self/status', 'utf8').match(/^Seccomp:\s*(\S+)/m)?.[1] ?? null; } catch {}
  const result = { blocked: true, reason: `${tcp === 'EPERM' ? 'socket(AF_INET)' : 'socket(AF_UNIX)'} returned EPERM inside a network-restricted sandbox`, tcp, unix, seccomp };
  process.stderr.write("Chromium cannot start inside this process's network-restricted seccomp sandbox (socket(AF_INET) -> EPERM; Seccomp: 2): chrome-headless-shell dies at startup with 'FATAL:content/browser/sandbox_host_linux.cc:41] Check failed: . shutdown: Operation not permitted (1)' (SIGTRAP) and on WSL every attempt is piped to %TEMP%\\wsl-crashes as a multi-GB dump. This is expected for Codex workspace-write workers: do not retry, do not LD_PRELOAD or --single-process around it, do not switch browsers. Screenshots and preview acceptance are produced by the workflow engine's verify stage outside the sandbox. See docs/preview-acceptance.md#chromium-cannot-start-inside-network-restricted-agent-sandboxes.\n");
  process.stderr.write(`browser-preflight: ${JSON.stringify(result)}\n`);
  process.exit(79);
}

const url = arg('--url');
const healthPath = arg('--health-path') || '/';
const out = arg('--out');
if (!url || !out) {
  process.stderr.write('usage: accept-preview.mjs --url <mounted-url> --health-path </path> --out <dir>\n');
  process.exit(2);
}

const playwright = await resolvePlaywright();
if (!playwright) {
  process.stderr.write('Playwright module not found\n');
  process.exit(78);
}
try {
  const executable = playwright.chromium.executablePath();
  if (!executable || !fs.existsSync(executable)) {
    process.stderr.write('Playwright Chromium browser is unavailable\n');
    process.exit(79);
  }
} catch (error) {
  process.stderr.write(`Playwright Chromium browser is unavailable: ${error instanceof Error ? error.message : String(error)}\n`);
  process.exit(79);
}

fs.mkdirSync(out, { recursive: true });
let browser;
try {
  await ensureBrowserCanStart();
  browser = await playwright.chromium.launch({ headless: true });
} catch (error) {
  process.stderr.write(`Playwright Chromium browser is unavailable: ${error instanceof Error ? error.message : String(error)}\n`);
  process.exit(79);
}

const consoleErrors = [];
const failedRequests = [];
const loadResults = [];
let pass = false;
try {
  const mountedURL = new URL(url);
  const mountedOrigin = mountedURL.origin;
  const mountedPrefix = mountedURL.pathname.endsWith('/') ? mountedURL.pathname : `${mountedURL.pathname}/`;
  const runTheme = async (theme, screenshotName) => {
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
    let page;
    let documentStatus = 0;
    let healthStatus = 0;
    let bodyTextLength = 0;
    let previewMarker = false;
    let navigationCount = 0;
    let staleBundleBanner = false;
    const consoleStart = consoleErrors.length;
    const requestStart = failedRequests.length;
    // Response-body checks below are asynchronous; settle them before the
    // per-theme pass verdict reads failedRequests.
    const pendingResponseChecks = [];
    try {
      if (theme === 'light') {
        await context.addInitScript?.(() => localStorage.setItem('solu.appTheme', 'light'));
      }
      page = await context.newPage();
      const mainFrame = page.mainFrame();
      page.on('framenavigated', (frame) => {
        if (frame === mainFrame) navigationCount += 1;
      });
      page.on('console', (message) => {
        if (message.type() !== 'error') return;
        const text = message.text();
        // Backend-less mounted previews answer every /api poll with the honest
        // "preview backend unavailable" JSON 404 (see preview_static.go); the
        // app's preview fetch shim handles those via deterministic fixtures.
        // The browser still logs an automatic resource-load error for each such
        // 404. Ignore those here to avoid double-counting, but only for
        // same-origin /api URLs: the body-gated response handler below stays
        // loud for the smoke harness's "unknown api path" 404 and genuine
        // asset 404s, and non-404 resource errors still count.
        if (/Failed to load resource[^]*\b404\b/.test(text)) {
          let locationURL = '';
          try {
            locationURL = message.location?.()?.url || '';
          } catch {
            locationURL = '';
          }
          try {
            const resource = new URL(locationURL, url);
            if (locationURL !== '' && resource.origin === mountedOrigin
              && /(^|\/)api(\/|$)/.test(resource.pathname)) return;
          } catch {
            // Unparseable location: fail closed and record below.
          }
        }
        boundedPush(consoleErrors, `${theme}: ${text}`);
      });
      page.on('pageerror', (error) => {
        boundedPush(consoleErrors, `${theme}: ${error instanceof Error ? error.message : String(error)}`);
      });
      page.on('request', (request) => {
        try {
          const resource = new URL(request.url());
          if (resource.origin === mountedOrigin && resource.pathname !== mountedURL.pathname
              && !resource.pathname.startsWith(mountedPrefix)) {
            boundedPush(failedRequests, `${theme}: ${request.method()} ${request.url()} escaped mounted preview prefix`);
          }
        } catch { /* requestfailed/response will report malformed URLs */ }
      });
      page.on('requestfailed', (request) => {
        // A request cancelled by the page or context (net::ERR_ABORTED) is not a
        // failure: the live app aborts superseded polls and in-flight pane
        // resizes when the acceptance context closes. Counting those made the
        // strict probe fail for every live-upstream preview. Genuine transport
        // failures still count.
        const errorText = request.failure()?.errorText || '';
        if (errorText.includes('ERR_ABORTED')) return;
        if (new URL(request.url()).origin === mountedOrigin) {
          boundedPush(failedRequests, `${theme}: ${request.method()} ${request.url()} failed: ${errorText || 'failed'}`);
        }
      });
      page.on('response', (response) => {
        const check = (async () => {
          let status = 0;
          let resource = null;
          let method = '';
          let href = '';
          try {
            status = response.status();
            href = response.url();
            resource = new URL(href);
            method = response.request().method();
          } catch {
            return;
          }
          if (status < 400 || resource.origin !== mountedOrigin) return;
          // Backend-less static previews answer every /api poll with the honest
          // "preview backend unavailable" JSON 404 (see preview_static.go); the
          // app's preview fetch shim (web/src/previewBoot.ts) handles those via
          // deterministic fixtures, so only that exact stand-in is benign.
          // Mirror previewLiveResponseIsUsable and gate on the body, not the
          // path: any other same-origin failure — including the smoke
          // harness's "smoke stub: unknown api path" 404 for a newly added
          // poll — stays loud so the probe fails with its URL. Unreadable
          // bodies fail closed. Asset and non-API 404s still count, as do
          // non-404 API errors.
          if (status === 404 && /(^|\/)api(\/|$)/.test(resource.pathname)) {
            let body = null;
            try {
              body = typeof response.text === 'function' ? await response.text() : null;
            } catch {
              body = null;
            }
            if (typeof body === 'string' && body.includes('preview backend unavailable')) return;
          }
          boundedPush(failedRequests, `${theme}: ${status} ${method} ${href}`);
        })();
        pendingResponseChecks.push(check);
      });
      const response = await page.goto(url, { waitUntil: 'load', timeout: 30_000 });
      documentStatus = response?.status() || 0;
      await page.waitForLoadState('networkidle', { timeout: 10_000 }).catch(() => undefined);
      if (await page.locator('[data-preview-acceptance-shell]').count()) {
        throw new Error('app_not_served: placeholder acceptance shell cannot verify the candidate UI');
      }
      if (await page.locator('html[data-solu-preview-fixture="1"]').count()) {
        // This marker belongs to SoluTerminal's isolated fixture build. Require
        // real product controls so an empty surrogate cannot pass this smoke.
        await page.getByText('Isolated preview · sample data · changes disabled', { exact: true }).waitFor();
        await page.getByRole('button', { name: 'Settings', exact: true }).click();
        const settings = page.getByRole('dialog', { name: 'Settings', exact: true });
        await settings.getByRole('tab', { name: 'Tasks', exact: true }).click();
        await settings.getByText('Choose a crew for each task', { exact: true }).waitFor();
        await settings.getByRole('button', { name: 'Close', exact: true }).click();
      }
      const bodyText = (await page.locator('body').innerText({ timeout: 5_000 }).catch(() => '')).trim();
      bodyTextLength = bodyText.length;
      previewMarker = await page.evaluate(
        () => document.documentElement.getAttribute('data-solu-preview-build') === '1',
      );
      // A mounted preview deliberately runs a differently-versioned bundle than
      // the host daemon it borrows: its /api/health and /webdist.json can be
      // answered by that live upstream, so the "SoluTerminal updated to X —
      // reloading did not pick it up" banner appears by design. Counting it made
      // the strict probe fail for every live-upstream preview. Only a
      // non-preview document can show a genuine version mismatch, so scope the
      // check there.
      staleBundleBanner = !previewMarker && (bodyText.includes('reloading did not pick it up')
        || bodyText.includes('quit and reopen SoluTerminal to finish the update'));
      const healthURL = new URL(String(healthPath).replace(/^\/+/, ''), url).href;
      healthStatus = await page.evaluate(async (target) => {
        try { return (await fetch(target, { cache: 'no-store' })).status; } catch { return 0; }
      }, healthURL);
      await page.screenshot({ path: path.join(out, screenshotName), fullPage: true, timeout: 30_000 });
      await Promise.allSettled(pendingResponseChecks);
      return {
        theme, documentStatus, healthStatus, bodyTextLength, previewMarker, navigationCount, staleBundleBanner,
        pass: documentStatus > 0 && documentStatus < 400
          && healthStatus > 0 && healthStatus < 400
          && bodyTextLength > 0
          && navigationCount === 1
          && !staleBundleBanner
          && consoleErrors.length === consoleStart
          && failedRequests.length === requestStart,
      };
    } catch (error) {
      boundedPush(consoleErrors, `${theme}: probe failed: ${error instanceof Error ? error.message : String(error)}`);
      if (page) {
        try {
          await page.screenshot({ path: path.join(out, screenshotName), fullPage: true, timeout: 5_000 });
        } catch (screenshotError) {
          boundedPush(consoleErrors, `${theme}: screenshot failed: ${screenshotError instanceof Error ? screenshotError.message : String(screenshotError)}`);
        }
      }
      return { theme, documentStatus, healthStatus, bodyTextLength, previewMarker, navigationCount, staleBundleBanner, pass: false };
    } finally {
      try {
        await context.close();
      } catch (error) {
        boundedPush(consoleErrors, `${theme}: context close failed: ${error instanceof Error ? error.message : String(error)}`);
      }
    }
  };
  loadResults.push(await runTheme('dark', 'screenshot.png'));
  loadResults.push(await runTheme('light', 'screenshot-light.png'));
} catch (error) {
  boundedPush(consoleErrors, `probe failed: ${error instanceof Error ? error.message : String(error)}`);
} finally {
  try {
    await browser.close();
  } catch (error) {
    boundedPush(consoleErrors, `browser close failed: ${error instanceof Error ? error.message : String(error)}`);
  }
  const firstFailure = loadResults.find((result) => !result.pass) || loadResults[0]
    || { documentStatus: 0, healthStatus: 0, bodyTextLength: 0 };
  const { documentStatus, healthStatus, bodyTextLength } = firstFailure;
  const previewMarker = loadResults.length === 2 && loadResults.every((result) => result.previewMarker);
  const navigationCount = loadResults.reduce((total, result) => total + result.navigationCount, 0);
  const staleBundleBanner = loadResults.some((result) => result.staleBundleBanner);
  pass = loadResults.length === 2 && loadResults.every((result) => result.pass)
    && consoleErrors.length === 0 && failedRequests.length === 0;
  fs.writeFileSync(path.join(out, 'result.json'), JSON.stringify({
    pass, consoleErrors, failedRequests, bodyTextLength, healthStatus, documentStatus, previewMarker,
    navigationCount, staleBundleBanner,
  }));
}
process.exit(pass ? 0 : 1);
