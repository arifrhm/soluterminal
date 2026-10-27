import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

import {
  contextPackEvidenceFixture,
  contextPackEvidenceScenarios,
  ensureContextPackEvidence,
  expectedContextPackCaptures,
} from './capture-context-pack.mjs';

describe('context-pack built-app evidence', () => {
  it('covers every required surface in both themes', () => {
    expect(contextPackEvidenceScenarios.map(({ name }) => name)).toEqual([
      'context-pack-workspace-default',
      'context-pack-new-pane',
      'context-pack-pane-dialog',
      'context-pack-pane-badges',
    ]);
    expect(expectedContextPackCaptures()).toEqual(
      contextPackEvidenceScenarios.flatMap(({ name }) => [
        `${name}-dark.png`,
        `${name}-light.png`,
      ]),
    );
  });

  it('uses real workspace and pane states for the controls and warning badges', () => {
    expect(contextPackEvidenceFixture.workspaces[0]).toMatchObject({
      name: 'Strategy',
      contextPack: '/home/user/wsl-projects/strategy',
    });
    expect(contextPackEvidenceFixture.panes.map(({ contextPackStatus }) => contextPackStatus)).toEqual([
      'unsupported',
      'missing',
    ]);
  });

  it('always verifies a fresh capture and only fills missing tracked evidence', async () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'solu-context-pack-test-'));
    const all = expectedContextPackCaptures();
    const captureCalls = [];
    const capture = async ({ out }) => {
      captureCalls.push(out);
      fs.mkdirSync(out, { recursive: true });
      for (const file of all) fs.writeFileSync(path.join(out, file), `fresh:${file}`);
      return all;
    };
    try {
      const complete = path.join(root, 'complete');
      fs.mkdirSync(complete);
      for (const file of all) fs.writeFileSync(path.join(complete, file), `tracked:${file}`);
      const untouched = await ensureContextPackEvidence({ currentDist: 'dist', out: complete, capture });
      expect(captureCalls).toHaveLength(1);
      expect(untouched).toEqual({ captures: all, created: false });
      expect(fs.readFileSync(path.join(complete, all[0]), 'utf8')).toBe(`tracked:${all[0]}`);

      const partial = path.join(root, 'partial');
      fs.mkdirSync(partial);
      for (const file of all.slice(1)) fs.writeFileSync(path.join(partial, file), `tracked:${file}`);
      const filled = await ensureContextPackEvidence({ currentDist: 'dist', out: partial, capture });
      expect(captureCalls).toHaveLength(2);
      expect(filled).toEqual({ captures: all, created: true });
      expect(fs.readFileSync(path.join(partial, all[0]), 'utf8')).toBe(`fresh:${all[0]}`);
      expect(fs.readFileSync(path.join(partial, all[1]), 'utf8')).toBe(`tracked:${all[1]}`);

      const incomplete = path.join(root, 'incomplete');
      await expect(ensureContextPackEvidence({
        currentDist: 'dist',
        out: incomplete,
        capture: async ({ out }) => {
          fs.mkdirSync(out, { recursive: true });
          for (const file of all.slice(1)) fs.writeFileSync(path.join(out, file), file);
          return all.slice(1);
        },
      })).rejects.toThrow(all[0]);
    } finally {
      fs.rmSync(root, { recursive: true, force: true });
    }
  });
});
