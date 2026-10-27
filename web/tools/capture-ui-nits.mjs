#!/usr/bin/env node

// Deterministic light/dark evidence for the desktop UI-nits batch. The built
// app talks to screens-lib's canned API plus a minimal WebSocket handshake so
// the terminal registers its sender and mounts the pointer approval strip.
//
// usage: capture-ui-nits.mjs --dist <webdist dir> --out <png dir>
// Optional env: PLAYWRIGHT_MODULE_PATH, PLAYWRIGHT_CHROMIUM_PATH.

import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';
import { pathToFileURL } from 'node:url';
import { createServer, exitBrowserUnavailable, resolvePlaywright as resolveSharedPlaywright } from './screens-lib.mjs';
import { ensureBrowserCanStart } from './browser-preflight.mjs';

function arg(name) {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : '';
}

function npxCachedPlaywrightCores() {
  const cache = path.join(process.env.HOME || '', '.npm', '_npx');
  try {
    return fs.readdirSync(cache)
      .map((dir) => path.join(cache, dir, 'node_modules', 'playwright-core'))
      .filter((dir) => fs.existsSync(dir));
  } catch {
    return [];
  }
}

async function resolvePlaywright() {
  const candidates = [
    'playwright',
    'playwright-core',
    process.env.PLAYWRIGHT_MODULE_PATH,
    ...npxCachedPlaywrightCores(),
  ].filter(Boolean);
  // Resolve absolute package directories before handing them to the shared
  // loader, matching the other standalone capture scripts' fallback.
  const require = createRequire(import.meta.url);
  const resolved = candidates.flatMap((candidate) => {
    try {
      if (path.isAbsolute(candidate) && fs.statSync(candidate).isDirectory()) {
        return [pathToFileURL(require.resolve(candidate)).href, candidate];
      }
    } catch { /* fall through to the raw candidate */ }
    return [candidate];
  });
  return resolveSharedPlaywright(resolved);
}

function resolveChromiumExecutable() {
  if (process.env.PLAYWRIGHT_CHROMIUM_PATH) return process.env.PLAYWRIGHT_CHROMIUM_PATH;
  const cache = path.join(process.env.HOME || '', '.cache', 'ms-playwright');
  try {
    const revisions = fs.readdirSync(cache)
      .filter((dir) => dir.startsWith('chromium_headless_shell-'))
      .sort((a, b) => Number(b.split('-')[1]) - Number(a.split('-')[1]));
    for (const revision of revisions) {
      const executable = path.join(
        cache,
        revision,
        'chrome-headless-shell-linux64',
        'chrome-headless-shell',
      );
      if (fs.existsSync(executable)) return executable;
    }
  } catch { /* use Playwright's default launch path */ }
  return undefined;
}

const dist = arg('--dist');
const out = arg('--out');
if (!dist || !out) {
  process.stderr.write('usage: capture-ui-nits.mjs --dist <dir> --out <dir>\n');
  process.exit(2);
}

const playwright = await resolvePlaywright();
if (!playwright) {
  process.stderr.write('Playwright module not found (tried local, core, env, and npx cache)\n');
  process.exit(78);
}

const now = Date.now();
const filePath = '/home/user/project/main.go';
const fileLines = [
  'package main',
  '',
  'import (',
  '    "context"',
  '    "fmt"',
  '    "log"',
  '    "net/http"',
  '    "os"',
  '    "os/signal"',
  '    "time"',
  ')',
  '',
  'type buildStatus struct {',
  '    Revision string `json:"revision"`',
  '    Healthy  bool   `json:"healthy"`',
  '}',
  '',
  'func main() {',
  '    mux := http.NewServeMux()',
  '    mux.HandleFunc("/health", health)',
  '',
  '    server := &http.Server{',
  '        Addr:              ":8080",',
  '        Handler:           mux,',
  '        ReadHeaderTimeout: 5 * time.Second,',
  '    }',
  '',
  '    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)',
  '    defer stop()',
  '',
  '    go func() {',
  '        <-ctx.Done()',
  '        shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)',
  '        defer cancel()',
  '        _ = server.Shutdown(shutdown)',
  '    }()',
  '',
  '    log.Printf("serving build status on %s", server.Addr)',
  '    if err := server.ListenAndServe(); err != http.ErrServerClosed {',
  '        log.Fatal(err)',
  '    }',
  '}',
  '',
  'func health(w http.ResponseWriter, _ *http.Request) {',
  '    w.Header().Set("Content-Type", "application/json")',
  '    fmt.Fprintln(w, `{"revision":"a1b2c3d","healthy":true}`)',
  '}',
];

const pane = {
  id: 'pane_demo',
  title: 'Build',
  cwd: '/home/user/project',
  cmd: ['claude'],
  status: 'running',
  createdAt: new Date(now - 60 * 60_000).toISOString(),
  cols: 80,
  rows: 24,
  pid: 1234,
  lastActivityMs: now - 2_000,
  state: 'choice-prompt',
  hasClaude: true,
  autoMode: 'cautious',
  attention: 'auto-pending',
};
const state = {
  panes: [pane],
  workspaces: [{
    name: 'Main',
    layout: { type: 'pane', paneId: pane.id },
    paneIds: [pane.id],
    panes: [{ id: pane.id, cwd: pane.cwd, cmd: pane.cmd, title: pane.title, cols: 80, rows: 24 }],
    files: [],
    activeAt: new Date(now).toISOString(),
  }],
  schedules: [{
    id: 's1',
    name: 'Morning digest',
    cron: '0 9 * * *',
    paneId: pane.id,
    prompt: 'summarise the overnight runs',
    enabled: true,
    createdAt: new Date(now - 24 * 60 * 60_000).toISOString(),
    fireCount: 0,
    skipCount: 0,
    errorCount: 0,
    nextFire: new Date(now + 12 * 60_000).toISOString(),
  }],
  sseEvents: [
    { type: 'toast', text: '🤖 Fix agent: apply exit 0 in 2m14s', skipRecent: true },
    { type: 'open-file', path: filePath },
  ],
  fileFixtures: {
    [filePath]: {
      path: filePath,
      lines: fileLines,
      mtime: now - 5_000,
      size: Buffer.byteLength(fileLines.join('\n') + '\n'),
      isEnv: false,
      trailingNewline: true,
    },
  },
};

let browser;
let server;
const chromiumArgs = [
  '--disable-crash-reporter',
  '--disable-crashpad',
  '--in-process-gpu',
  '--single-process',
  '--no-zygote',
];
try {
  fs.mkdirSync(out, { recursive: true });
  try {
    await ensureBrowserCanStart();
    browser = await playwright.chromium.launch({ headless: true, args: chromiumArgs });
  } catch (error) {
    const executablePath = resolveChromiumExecutable();
    if (!executablePath) {
      exitBrowserUnavailable(error instanceof Error ? error.message : String(error));
    }
    await ensureBrowserCanStart();
    browser = await playwright.chromium.launch({ headless: true, executablePath, args: chromiumArgs });
  }

  server = createServer(path.resolve(dist), state);
  server.on('upgrade', (req, socket) => {
    const key = req.headers['sec-websocket-key'];
    if (!key) {
      socket.destroy();
      return;
    }
    const accept = crypto
      .createHash('sha1')
      .update(key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11')
      .digest('base64');
    socket.write([
      'HTTP/1.1 101 Switching Protocols',
      'Upgrade: websocket',
      'Connection: Upgrade',
      `Sec-WebSocket-Accept: ${accept}`,
      '',
      '',
    ].join('\r\n'));
  });
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  const address = server.address();
  if (!address || typeof address === 'string') throw new Error('capture server has no TCP address');

  const shots = [];
  for (const theme of ['dark', 'light']) {
    const context = await browser.newContext({
      viewport: { width: 1440, height: 1000 },
      serviceWorkers: 'block',
    });
    try {
      const page = await context.newPage();
      const consoleErrors = [];
      page.on('console', (message) => {
        if (message.type() === 'error') consoleErrors.push(message.text());
      });
      page.on('pageerror', (error) => consoleErrors.push(error.message));
      await page.addInitScript((selectedTheme) => {
        localStorage.setItem('solu.appTheme', selectedTheme);
        localStorage.setItem('solu.seenWelcome', '1');
        localStorage.setItem('solu.seenChecklist', '1');
      }, theme);
      await page.goto(`http://127.0.0.1:${address.port}/`, { waitUntil: 'load' });
      await page.waitForSelector('.approval-actions', { timeout: 15_000 });
      await page.waitForSelector('.pane-automode-badge + .pane-autoapproving-chip', { timeout: 15_000 });
      await page.waitForSelector('.pane-schedule-chip', { timeout: 15_000 });
      await page.waitForSelector('.toast-icon svg', { timeout: 15_000 });
      await page.waitForSelector('.file-pane .file-lines', { timeout: 15_000 });
      const toastCopy = await page.locator('.toast-msg').innerText();
      if (toastCopy.includes('🤖')) throw new Error(`raw daemon glyph remains in toast: ${toastCopy}`);
      if (consoleErrors.length) throw new Error(`console errors: ${consoleErrors.join(' | ')}`);

      const overview = `nits-overview-${theme}.png`;
      await page.screenshot({ fullPage: true, path: path.join(out, overview) });
      shots.push({ label: `nits-overview-${theme}`, file: overview });

      await page.getByRole('button', { name: 'Maximise pane', exact: true }).click();
      await page.waitForSelector('.zoomed .file-pane');
      await page.getByRole('button', { name: 'Restore pane', exact: true }).waitFor();
      const zoomed = `file-zoomed-${theme}.png`;
      await page.screenshot({ fullPage: true, path: path.join(out, zoomed) });
      shots.push({ label: `file-zoomed-${theme}`, file: zoomed });
      await page.getByRole('button', { name: 'Restore pane', exact: true }).click();
    } finally {
      await context.close();
    }
  }
  process.stdout.write(`${JSON.stringify({ shots })}\n`);
} catch (error) {
  const message = error instanceof Error ? error.message : String(error);
  if (message.includes("Executable doesn't exist")) exitBrowserUnavailable(message);
  fs.writeSync(process.stderr.fd, `${message}\n`);
  process.exitCode = 1;
} finally {
  if (browser) await browser.close().catch(() => {});
  if (server) await new Promise((resolve) => server.close(resolve));
}
