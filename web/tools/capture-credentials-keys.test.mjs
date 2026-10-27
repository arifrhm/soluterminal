import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

import {
  CREDENTIALS_KEYS_HEALTH_FIXTURE,
  defaultCredentialsKeysPaths,
  ensureCredentialsKeysEvidence,
  expectedCredentialsKeysCaptures,
  installCredentialsKeysState,
} from './capture-credentials-keys.mjs';

describe('credentials API-key built-app evidence', () => {
  it('pins no-argument paths for the pre-verify evidence runner', () => {
    expect(defaultCredentialsKeysPaths()).toEqual({
      dist: path.resolve(import.meta.dirname, '..', 'dist'),
      out: path.resolve(import.meta.dirname, '..', '..', 'docs', 'screenshots'),
    });
  });

  it('captures the Add-key and mocked narrow DeepSeek pricing states in both themes', () => {
    expect(expectedCredentialsKeysCaptures()).toEqual([
      'credentials-api-key-add-dark.png',
      'credentials-api-key-add-light.png',
      'deepseek-pricing-balance-error-narrow-dark.png',
      'deepseek-pricing-balance-error-narrow-light.png',
    ]);
  });

  it('uses required unconfigured DeepSeek and Grok keys plus a configured OpenRouter key', () => {
    const byID = Object.fromEntries(
      CREDENTIALS_KEYS_HEALTH_FIXTURE.runtimes.map((runtime) => [runtime.id, runtime]),
    );
    expect(byID['deepseek-headless'].secrets).toEqual([
      { env: 'DEEPSEEK_API_KEY', required: true, configured: false },
    ]);
    expect(byID['grok-headless'].secrets).toEqual([
      { env: 'XAI_API_KEY', required: true, configured: false },
    ]);
    expect(byID['gemini-headless'].secrets).toEqual([
      { env: 'GEMINI_API_KEY', required: false, configured: false },
    ]);
    expect(byID['openrouter-headless'].secrets).toEqual([
      { env: 'OPENROUTER_API_KEY', required: true, configured: true },
    ]);
  });

  it('opens the Credentials section without leaving it in the default-hidden set', () => {
    const values = new Map();
    const originalStorage = globalThis.localStorage;
    globalThis.localStorage = {
      setItem(key, value) { values.set(key, value); },
    };
    try {
      installCredentialsKeysState('dark');
    } finally {
      if (originalStorage === undefined) delete globalThis.localStorage;
      else globalThis.localStorage = originalStorage;
    }

    expect(values.get('solu.appTheme')).toBe('dark');
    expect(JSON.parse(values.get('solu.sidebarOpenMiddle.v2'))).toContain('credentials');
    expect(JSON.parse(values.get('solu.userPrefs.v1'))).toMatchObject({
      prefs: { sidebar: { hidden: [] } },
    });
  });

  it('preserves tracked files', async () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'solu-credentials-keys-test-'));
    const all = expectedCredentialsKeysCaptures();
    const captureCalls = [];
    const browser = { fixture: 'shared smoke browser' };
    const tracked = new Map(all.map((file, index) => [file, Buffer.from([index, 0, 255, 10])]));
    const capture = async ({ out, browser: capturedBrowser }) => {
      captureCalls.push({ out, browser: capturedBrowser });
      fs.mkdirSync(out, { recursive: true });
      for (const file of all) fs.writeFileSync(path.join(out, file), `fresh:${file}`);
      return all;
    };
    try {
      const out = path.join(root, 'complete');
      fs.mkdirSync(out);
      for (const [file, bytes] of tracked) fs.writeFileSync(path.join(out, file), bytes);

      const untouched = await ensureCredentialsKeysEvidence({ currentDist: 'dist', out, browser, capture });

      expect(captureCalls).toHaveLength(1);
      expect(captureCalls[0].browser).toBe(browser);
      expect(untouched).toEqual({ captures: all, created: false });
      for (const [file, bytes] of tracked) {
        expect(fs.readFileSync(path.join(out, file))).toEqual(bytes);
      }
    } finally {
      fs.rmSync(root, { recursive: true, force: true });
    }
  });

  it('fills missing files', async () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'solu-credentials-keys-test-'));
    const all = expectedCredentialsKeysCaptures();
    const captureCalls = [];
    const browser = { fixture: 'shared smoke browser' };
    const trackedBytes = Buffer.from([0, 1, 2, 255]);
    const capture = async ({ out, browser: capturedBrowser }) => {
      captureCalls.push({ out, browser: capturedBrowser });
      fs.mkdirSync(out, { recursive: true });
      for (const file of all) fs.writeFileSync(path.join(out, file), `fresh:${file}`);
      return all;
    };
    try {
      const out = path.join(root, 'partial');
      fs.mkdirSync(out);
      fs.writeFileSync(path.join(out, all[1]), trackedBytes);

      const filled = await ensureCredentialsKeysEvidence({ currentDist: 'dist', out, browser, capture });

      expect(captureCalls).toHaveLength(1);
      expect(captureCalls[0].browser).toBe(browser);
      expect(filled).toEqual({ captures: all, created: true });
      expect(fs.readFileSync(path.join(out, all[0]), 'utf8')).toBe(`fresh:${all[0]}`);
      expect(fs.readFileSync(path.join(out, all[1]))).toEqual(trackedBytes);
    } finally {
      fs.rmSync(root, { recursive: true, force: true });
    }
  });

  it('rejects incomplete captures', async () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'solu-credentials-keys-test-'));
    const all = expectedCredentialsKeysCaptures();
    const captureCalls = [];
    const browser = { fixture: 'shared smoke browser' };
    const capture = async ({ out, browser: capturedBrowser }) => {
      captureCalls.push({ out, browser: capturedBrowser });
      fs.mkdirSync(out, { recursive: true });
      fs.writeFileSync(path.join(out, all[1]), all[1]);
      return [all[1]];
    };
    try {
      await expect(ensureCredentialsKeysEvidence({
        currentDist: 'dist',
        out: path.join(root, 'incomplete'),
        browser,
        capture,
      })).rejects.toThrow(all[0]);
      expect(captureCalls).toHaveLength(1);
      expect(captureCalls[0].browser).toBe(browser);
    } finally {
      fs.rmSync(root, { recursive: true, force: true });
    }
  });
});
