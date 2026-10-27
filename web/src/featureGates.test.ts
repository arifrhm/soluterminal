import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { describe, expect, it } from 'vitest';
import { FEATURE_GATES as CORE_GATES, GATE_HOOKS, type GateFlag } from './featureGates';
import { slotList } from './extensions';
import './loadExtensions';

// The core inventory plus what optional modules register for their own files.
const FEATURE_GATES = [...CORE_GATES, ...slotList('featureGates')];

const SRC = __dirname;

function sourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) {
      out.push(...sourceFiles(path));
    } else if (/\.(ts|tsx)$/.test(name) && !/\.test\.(ts|tsx)$/.test(name)) {
      out.push(path);
    }
  }
  return out;
}

// Files that define or document the gates rather than use them.
const GATE_DEFINITIONS = new Set(['entitlements.ts', 'featureGates.ts']);

const OPERATOR_CALL = /\b(useOperatorFeatures|operatorFeaturesAllowed)\(/;
const TEAM_CALL = /\b(useTeamFeatures|teamFeaturesAllowed)\(/;

describe('feature gate inventory', () => {
  it('lists each file once per flag', () => {
    const entries = FEATURE_GATES.map((g) => `${g.file}:${g.flag}`);
    expect(new Set(entries).size).toBe(entries.length);
  });

  it.each(FEATURE_GATES.map((g) => [g.file, g.flag] as [string, GateFlag]))(
    '%s reads the %s gate',
    (file, flag) => {
      const text = readFileSync(join(SRC, file), 'utf8');
      expect(GATE_HOOKS[flag].some((hook) => text.includes(hook))).toBe(true);
    },
  );

  it('has no operator or team gate outside the inventory', () => {
    const listed = new Set(FEATURE_GATES.map((g) => g.file));
    const unlisted = sourceFiles(SRC)
      .map((path) => relative(SRC, path).split('\\').join('/'))
      .filter((file) => !GATE_DEFINITIONS.has(file) && !listed.has(file))
      .filter((file) => {
        const text = readFileSync(join(SRC, file), 'utf8');
        return OPERATOR_CALL.test(text) || TEAM_CALL.test(text);
      });
    expect(unlisted).toEqual([]);
  });

  it('keeps operator tooling off the team flag', () => {
    // A file may gate a team-plan surface next to operator tooling only when
    // the inventory lists that team surface explicitly.
    const teamListed = new Set(FEATURE_GATES.filter((g) => g.flag === 'team').map((g) => g.file));
    for (const gate of FEATURE_GATES.filter((g) => g.flag === 'operator' && !teamListed.has(g.file))) {
      const text = readFileSync(join(SRC, gate.file), 'utf8');
      expect({ file: gate.file, team: TEAM_CALL.test(text) }).toEqual({ file: gate.file, team: false });
    }
  });
});
