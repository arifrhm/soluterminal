// Proof: the REAL <Pane> keeps a streaming terminal pinned to the tail, and a
// click into the transcript does not latch the reader away — while a real
// wheel-up still does. Run with a Vite dev server on :5199 serving web/.
import { chromium } from 'playwright';
import fs from 'node:fs';

// Match the version baked into THIS bundle (vite define __SOLU_WEB_VERSION__),
// so the stale-bundle self-heal does not fire and reload the page under us.
const VER = JSON.parse(fs.readFileSync(new URL('../../desktop/src-tauri/tauri.conf.json', import.meta.url), 'utf8')).version;

const W = 120;
const rule = '─'.repeat(W);

function createTui(send) {
  let drawn = true;
  let up = 0;
  const rawDraw = () => {
    const erase = drawn ? `\r${up > 0 ? `\x1b[${up}A` : ''}\x1b[J` : '';
    drawn = false; up = 0;
    const shown = ['▸ '];
    const out = [erase, '\r', rule + '\n', shown.join('\n') + '\n', rule + '\n', '⠋ thinking · 0.0s · type to queue · Ctrl+C cancels'];
    const targetRow = 1;
    const total = 0 + 2 + shown.length + 1;
    const u = (total - 1) - targetRow;
    out.push(`\r${u > 0 ? `\x1b[${u}A` : ''}`);
    send(Buffer.from(out.join(''), 'utf8'));
    drawn = true; up = targetRow;
  };
  const rawErase = () => {
    if (!drawn) return;
    send(Buffer.from(`\r${up > 0 ? `\x1b[${up}A` : ''}\x1b[J`, 'utf8'));
    drawn = false; up = 0;
  };
  const emit = (text) => {
    const had = drawn;
    if (had) rawErase();
    send(Buffer.from(text, 'utf8'));
    if (had) rawDraw();
  };
  rawDraw();
  return { rawDraw, emit };
}

const PANE = {
  id: 'pane_test', cwd: '/home/user', cmd: ['node', 'deepseek-terminal.mjs'],
  title: 'deepseek', status: 'running', createdAt: new Date().toISOString(),
  cols: W, rows: 32, pid: 4242, lastActivityMs: Date.now(), state: 'idle',
  hasClaude: false, hasCodex: false, hasMuse: false, hasPi: false, hasDeepseek: true,
  backend: 'wsl', lastPrompts: [],
};

const browser = await chromium.launch();
const context = await browser.newContext({ viewport: { width: 1280, height: 800 } });

await context.route('**/api/**', (route) => {
  const p = new URL(route.request().url()).pathname;
  const json = (body) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
  if (p === '/api/panes') return json([PANE]);
  if (p === '/api/workspaces') return json([]);
  if (p === '/api/health') return json({ status: 'ok', version: VER, webVersion: VER, webSourceSha: 'dev', backendID: 'wsl' });
  if (p === '/api/update/status') return json({ current: VER, latest: VER, available: false, applying: false });
  if (p === '/api/ui/events' || p === '/api/events') return route.fulfill({ status: 200, contentType: 'text/event-stream', body: '' });
  return json({});
});

let paragraph = 0;
await context.routeWebSocket((url) => url.pathname.includes('/api/panes/pane_test/io'), (ws) => {
  ws.onMessage(() => {});
  const tui = createTui((b) => ws.send(b));
  ws.send(JSON.stringify({ type: 'ownership', owned: true }));
  const para = setInterval(() => {
    paragraph += 1;
    tui.emit(`\nParagraph ${paragraph}. ${'lorem ipsum dolor sit amet consectetur adipiscing elit sed do. '.repeat(4)}\n`);
  }, 90);
  const spin = setInterval(() => tui.rawDraw(), 120);
  ws.onClose(() => { clearInterval(para); clearInterval(spin); });
});

const page = await context.newPage();
page.on('pageerror', (e) => console.log('PAGEERROR', e.message));
page.on('load', () => console.log('EVENT load', page.url()));
page.on('framenavigated', (f) => { if (f === page.mainFrame()) console.log('EVENT nav', f.url()); });
await page.goto('http://127.0.0.1:5199/tools/scrolltest.html', { waitUntil: 'load' });
await page.waitForSelector('.xterm', { timeout: 15000 });
await page.waitForFunction(() => {
  const m = window.__soluTerms;
  return m && m.get && m.get('pane_test');
}, null, { timeout: 15000 });

// Continuous sampler of the real xterm viewport + the chip.
await page.evaluate(() => {
  const term = window.__soluTerms.get('pane_test');
  const bs = term._core._bufferService;
  window.__maxAway = 0;
  window.__last = null;
  window.__sampler = setInterval(() => {
    const b = term.buffer.active;
    const away = b.baseY - b.viewportY;
    if (away > window.__maxAway) window.__maxAway = away;
    window.__last = { away, viewportY: b.viewportY, baseY: b.baseY, ius: bs.isUserScrolling, chip: !!document.querySelector('.scroll-latest-chip') };
  }, 80);
});
await new Promise((r) => setTimeout(r, 500));
console.log('after setup:', await page.evaluate(() => ({ maxAway: window.__maxAway, hasTerm: !!window.__soluTerms?.get('pane_test') })));

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Phase A: streaming only (6s). A click into the transcript lands mid-stream.
await sleep(2500);
await page.evaluate(() => {
  const screen = document.querySelector('.xterm-screen');
  screen.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, composed: true }));
  document.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, composed: true }));
});
await sleep(3500);
const phaseA = await page.evaluate(() => ({ maxAway: window.__maxAway, last: window.__last }));

// Phase R: mid-stream pane resizes (the fitPreservingScroll path). This is the
// writer the real trace showed moving the viewport.
await page.evaluate(() => { window.__maxAway = 0; });
for (let k = 0; k < 3; k += 1) {
  await page.setViewportSize({ width: 1280, height: 420 });
  await sleep(500);
  await page.setViewportSize({ width: 1280, height: 800 });
  await sleep(500);
}
await sleep(1500);
const phaseR = await page.evaluate(() => ({ maxAway: window.__maxAway, last: window.__last }));

// Phase B: a genuine wheel scroll-up must still latch the chip.
await page.evaluate(() => { window.__maxAwayBefore = window.__maxAway; window.__maxAway = 0; });
await page.evaluate(() => {
  const vp = document.querySelector('.xterm-viewport');
  vp.dispatchEvent(new WheelEvent('wheel', { deltaY: -240, bubbles: true }));
  for (let i = 0; i < 5; i += 1) { vp.scrollTop -= 60; vp.dispatchEvent(new Event('scroll')); }
});
await sleep(1500);
const phaseB = await page.evaluate(() => ({ maxAway: window.__maxAway, last: window.__last }));

const result = {
  paragraph,
  phaseA_streaming_plus_click: { maxAway: phaseA.maxAway, chip: phaseA.last?.chip, ius: phaseA.last?.ius, viewportY: phaseA.last?.viewportY, baseY: phaseA.last?.baseY },
  phaseR_midstream_resize: { maxAway: phaseR.maxAway, chip: phaseR.last?.chip, ius: phaseR.last?.ius, viewportY: phaseR.last?.viewportY, baseY: phaseR.last?.baseY },
  phaseB_real_wheel: { maxAway: phaseB.maxAway, chip: phaseB.last?.chip, ius: phaseB.last?.ius, viewportY: phaseB.last?.viewportY, baseY: phaseB.last?.baseY },
};
console.log(JSON.stringify(result, null, 2));

const pass = phaseA.maxAway <= 1 && phaseA.last?.chip === false && phaseR.maxAway <= 1 && phaseR.last?.chip === false && phaseB.maxAway > 0;
console.log(`RESULT: ${pass ? 'PASS' : 'FAIL'} (A<=1, R<=1, B>0)`);
await browser.close();
process.exit(pass ? 0 : 1);
