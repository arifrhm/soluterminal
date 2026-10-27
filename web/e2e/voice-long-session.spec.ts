import { test, expect } from 'playwright/test';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { installStoragePreferences } from '../tools/e2e-smoke.mjs';
import { writeVoiceFixture, multipartVoiceUpload } from '../tools/voice-audio-fixture.mjs';

// These tests use real capture/encoding and the production dist. Paid upstream
// recognition/cleanup is intercepted, so this proves no recognition accuracy.
test('terminal holds through three real rollovers, delayed processing and one final submission', async ({
  browser,
  baseURL,
}, info) => {
  test.setTimeout(240000);
  const temp = await mkdtemp(join(tmpdir(), 'solu-voice-'));
  const fixture = join(temp, 'microphone.wav');
  await writeVoiceFixture(fixture, { quiet: false });
  const instance = await browser.browserType().launch({
    args: [
      '--use-fake-ui-for-media-stream',
      '--use-fake-device-for-media-stream',
      `--use-file-for-fake-audio-capture=${fixture}`,
    ],
  });
  const context = await instance.newContext({ baseURL, permissions: ['microphone'] });
  const page = await context.newPage();
  const parts: ReturnType<typeof multipartVoiceUpload>[] = [];
  const writes: string[] = [];
  await installStoragePreferences(page, 'dark');
  const pane = {
    id: 'voice_agent',
    title: 'Voice dictation',
    cwd: '/fixture',
    cmd: ['claude'],
    status: 'running',
    createdAt: '2026-10-24T00:00:00Z',
    cols: 100,
    rows: 30,
    pid: 1,
    state: 'idle',
    hasClaude: true,
  };
  const workspace = {
    name: 'Voice acceptance',
    layout: { type: 'pane', paneId: pane.id },
    paneIds: [pane.id],
    activeAt: '2026-10-24T00:00:00Z',
  };
  await page.route('**/api/workflow/tasks', (route) => route.fulfill({ json: [] }));
  await page.route('**/api/panes', (route) => route.fulfill({ json: [pane] }));
  await page.route('**/api/workspaces**', (route) =>
    route.fulfill({ json: route.request().method() === 'POST' ? workspace : [workspace] }),
  );
  await page.routeWebSocket(/\/api\/panes\/voice_agent\/io/, (socket) =>
    socket.send(Buffer.from('\r\n$ Voice acceptance fixture\r\n')),
  );
  await page.addInitScript(() => localStorage.setItem('solu.activeWorkspace', 'Voice acceptance'));
  try {
    await page.addInitScript(() => {
      localStorage.setItem('solu.voiceLanguage', 'en');
      localStorage.setItem('solu.voiceAutoSend', '1');
      localStorage.setItem('solu.voiceDeferCleanup', '0');
      localStorage.setItem('solu.voiceReadyTick', '0');
    });
    await page.route('**/api/transcribe', async (route) => {
      const request = route.request();
      const part = multipartVoiceUpload(
        request.postDataBuffer()!,
        request.headers()['content-type'],
      );
      parts.push(part);
      expect(part.fields.language).toBe('en');
      expect(part.frames).toBeLessThanOrEqual(960000);
      if (parts.length === 1) await new Promise((r) => setTimeout(r, 8000));
      await route.fulfill({
        json: {
          text: `Part ${parts.length}.`,
          rawText: `um part ${parts.length}`,
          cleanup: 'ok',
          requestedLanguage: 'en',
          detectedLanguage: 'en',
          audioDurationMs: part.durationMs,
        },
      });
    });
    await page.route('**/api/panes/*/inject', async (route) => {
      writes.push(route.request().postData() ?? '');
      await route.fulfill({ json: { ok: true } });
    });
    await page.goto('/');
    await expect(page.locator('[data-pane-id]').first()).toBeVisible();
    await page.locator('[data-pane-id] .pane-header').first().click();
    await page.keyboard.down('Alt');
    await page.keyboard.down('s');
    await expect(
      page
        .getByRole('status')
        .filter({ hasText: /^Recording/ })
        .first(),
    ).toBeVisible();
    for (const theme of ['light', 'dark']) {
      await page.evaluate((t) => (document.documentElement.dataset.theme = t), theme);
      await page.screenshot({ path: info.outputPath(`recording-${theme}.png`) });
    }
    await page.waitForTimeout(125000);
    expect(writes).toHaveLength(0);
    await expect(
      page
        .getByRole('status')
        .filter({ hasText: /^Recording/ })
        .first(),
    ).toBeVisible();
    await expect.poll(() => parts.length, { timeout: 65000 }).toBeGreaterThanOrEqual(3);
    expect(writes).toHaveLength(0);
    await page.keyboard.up('s');
    await page.keyboard.up('Alt');
    await page.screenshot({ path: info.outputPath('finishing-dark.png') });
    await expect.poll(() => writes.length, { timeout: 15000 }).toBeGreaterThanOrEqual(1);
    expect(writes.filter((w) => w.includes('Part 1.'))).toHaveLength(1);
    // Real browser decoder independently accepts every posted WAV.
    for (const part of parts) {
      const samples = await page.evaluate(async (bytes) => {
        const ctx = new AudioContext();
        try {
          return (await ctx.decodeAudioData(new Uint8Array(bytes).buffer)).length;
        } finally {
          await ctx.close();
        }
      }, Array.from(part.audio));
      expect(samples).toBeGreaterThan(0);
    }
    await info.attach('capture-evidence', {
      body: JSON.stringify({
        parts: parts.map((p) => ({
          frames: p.frames,
          durationMs: p.durationMs,
          language: p.fields.language,
        })),
        writes,
        upstream: 'mocked; no recognition accuracy measured',
      }),
      contentType: 'application/json',
    });
  } finally {
    await context.close();
    await instance.close();
    await rm(temp, { recursive: true, force: true });
  }
});

test('retains failed audio for download and retry without submitting automatically', async ({
  browser,
  baseURL,
}, info) => {
  const temp = await mkdtemp(join(tmpdir(), 'solu-voice-recovery-'));
  const fixture = join(temp, 'microphone.wav');
  await writeVoiceFixture(fixture, { seconds: 10, quiet: false });
  const instance = await browser.browserType().launch({
    args: [
      '--use-fake-ui-for-media-stream',
      '--use-fake-device-for-media-stream',
      `--use-file-for-fake-audio-capture=${fixture}`,
    ],
  });
  const context = await instance.newContext({
    baseURL,
    permissions: ['microphone'],
    viewport: { width: 390, height: 844 },
  });
  const page = await context.newPage();
  try {
    await installStoragePreferences(page, 'dark');
    const pane = {
      id: 'voice_recovery',
      title: 'Voice recovery',
      cwd: '/fixture',
      cmd: ['claude'],
      status: 'running',
      createdAt: '2026-10-24T00:00:00Z',
      cols: 80,
      rows: 24,
      pid: 1,
      state: 'idle',
      hasClaude: true,
    };
    const workspace = {
      name: 'Voice recovery',
      paneIds: [pane.id],
      layout: { type: 'pane', paneId: pane.id },
      activeAt: '2026-10-24T00:00:00Z',
    };
    await page.route('**/api/workflow/tasks', (route) => route.fulfill({ json: [] }));
    await page.route('**/api/panes', (route) => route.fulfill({ json: [pane] }));
    await page.route('**/api/workspaces**', (route) =>
      route.fulfill({ json: route.request().method() === 'POST' ? workspace : [workspace] }),
    );
    await page.routeWebSocket(/\/api\/panes\/voice_recovery\/io/, (socket) =>
      socket.send(Buffer.from('\r\nVoice recovery fixture\r\n')),
    );
    await page.addInitScript(() => {
      localStorage.setItem('solu.activeWorkspace', 'Voice recovery');
      localStorage.setItem('solu.voiceLanguage', 'en');
      localStorage.setItem('solu.voiceAutoSend', '1');
      localStorage.setItem('solu.voiceDeferCleanup', '0');
      localStorage.setItem('solu.voiceReadyTick', '0');
    });
    const parts: ReturnType<typeof multipartVoiceUpload>[] = [];
    const writes: string[] = [];
    await page.route('**/api/transcribe', async (route) => {
      const request = route.request();
      parts.push(
        multipartVoiceUpload(request.postDataBuffer()!, request.headers()['content-type']),
      );
      if (parts.length === 1) {
        await new Promise(resolve => setTimeout(resolve, 1000));
        await route.fulfill({ status: 503, body: 'Synthetic connection failure' });
      }
      else
        await route.fulfill({
          json: {
            text: 'Recovered words.',
            cleanup: 'ok',
            requestedLanguage: 'en',
            detectedLanguage: 'en',
          },
        });
    });
    await page.route('**/api/panes/*/inject', async (route) => {
      writes.push(route.request().postData() ?? '');
      await route.fulfill({ json: { bytes: 10 } });
    });
    await page.goto('/');
    await page.locator('[data-pane-id] .pane-header').first().click();
    await page.keyboard.down('Alt');
    await page.keyboard.down('s');
    await expect(
      page
        .getByRole('status')
        .filter({ hasText: /^Recording/ })
        .first(),
    ).toBeVisible();
    await page.waitForTimeout(1000);
    await page.keyboard.up('s');
    await page.keyboard.up('Alt');
    // Alt+C belongs to the terminal once recording has stopped. It must
    // neither discard finishing audio nor erase a retained recovery session.
    await expect(page.getByRole('status').filter({ hasText: /^Finishing dictation/ })).toBeVisible();
    await page.keyboard.press('Alt+c');
    const retry = page.getByRole('button', { name: 'Retry remaining', exact: true });
    await expect(retry).toBeEnabled();
    await page.keyboard.press('Alt+c');
    await expect(retry).toBeEnabled();
    expect(writes).toHaveLength(0);
    await expect(page.getByRole('button', { name: 'Use available text' })).toBeDisabled();
    await page.screenshot({ path: info.outputPath('recovery-narrow-dark.png') });
    const downloaded = page.waitForEvent('download');
    await page.getByRole('button', { name: 'Download retained audio' }).click();
    await expect.poll(async () => (await downloaded).suggestedFilename()).toBe('voice-part-1.wav');
    await retry.click();
    await expect.poll(() => writes.length).toBe(1);
    await page.waitForTimeout(600);
    expect(writes).toHaveLength(1);
    expect(parts).toHaveLength(2);
    expect(parts.map((part) => part.fields.language)).toEqual(['en', 'en']);
    expect(parts[0].audio.equals(parts[1].audio)).toBe(true);
  } finally {
    await context.close();
    await instance.close();
    await rm(temp, { recursive: true, force: true });
  }
});
