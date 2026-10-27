// nativeFixtures.test.ts — the SHARED golden-fixture suite for the native renderer.
//
// The native transcript-shaping passes (buildTranscript + groupRows + the
// annotations) are hand-written TWICE: here in TS (web) and in Dart
// (soluterminal-mobile lib/src/daemon/native_transcript.dart). To stop the two
// from drifting, each feature's behavior is specified ONCE as a language-neutral
// fixture under ./fixtures/<case>/ and BOTH clients run the IDENTICAL fixtures:
//   • input.jsonl          — one wire-shape NativeEvent per line (the daemon's output)
//   • meta.json            — { agentMidTurn, pendingCount, paneId, fixtureFormatVersion }
//   • expected.focus.json  — projection of groupRows(buildTranscript(...).rows)
//   • expected.transcript.json (optional) — result annotations (bgShell/interrupted/isError)
//
// The PROJECTION is tiny + language-neutral: each render row → { kind, seqs[] }.
// That's all a golden needs to pin grouping/visibility/ordering — the per-platform
// widget chrome is NOT part of the contract (React vs Flutter render it differently).
//
// This web test is the GENERATOR (source of truth): run `UPDATE_FIXTURES=1 npm test`
// to (re)write the expected.*.json from the current TS impl, eyeball the diff, commit.
// The Dart suite (test/native_fixtures_test.dart) only ASSERTS against the synced copy.
// Sync the canonical fixtures into the Flutter repo with scripts/sync-native-fixtures.sh.
//
// A new native render feature is "done" only when its fixture is green in BOTH suites;
// a missing Flutter port shows up as a RED Dart test, not a silent gap.

import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, writeFileSync, existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import type { NativeEvent } from './nativeEvents';
import type { RenderRow } from './nativeTranscript';
import { buildTranscript, groupRows, computeNativeWorking, idleBackgroundShells } from './nativeTranscript';

const FIXTURES_DIR = join(dirname(fileURLToPath(import.meta.url)), 'fixtures');
const UPDATE = !!process.env.UPDATE_FIXTURES;

interface ProjectedRow {
  kind: string;
  seqs: number[];
}

// Tiny language-neutral projection of a RenderRow. Groups carry their members' seqs;
// a turn separator carries the user-turn seq parsed from its `sep-<seq>` key.
function projectRows(rows: RenderRow[]): ProjectedRow[] {
  return rows.map((r): ProjectedRow => {
    if (r.kind === 'event') return { kind: 'event', seqs: [r.event.seq] };
    if (r.kind === 'turn-sep') return { kind: 'turn-sep', seqs: [Number(r.key.replace(/^sep-/, ''))] };
    return { kind: r.kind, seqs: r.events.map((e) => e.seq) };
  });
}

// The parts of a paired result that aren't visible in the row STRUCTURE but are still
// client-derived contract: the background-shell status, the ghost-spinner interrupted
// marker, and the error flag. Keyed by callId, sorted for stable output.
function projectTranscript(resultByCallId: Map<string, NativeEvent>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const callId of [...resultByCallId.keys()].sort()) {
    const r = resultByCallId.get(callId)!;
    const a: Record<string, unknown> = {};
    if (r.bgShell) a.bgShell = r.bgShell;
    if (r.interrupted) a.interrupted = true;
    if (r.isError) a.isError = true;
    if (Object.keys(a).length) out[callId] = a;
  }
  return out;
}

// Sub-agent nesting: which nested tool_calls were collected under each parent Task (by
// callId), as their seqs. Pins that children are NESTED (not dropped) identically in
// both clients. Empty for fixtures with no sub-agents.
function projectChildren(childrenByParent: Map<string, NativeEvent[]>): Record<string, number[]> {
  const out: Record<string, number[]> = {};
  for (const parent of [...childrenByParent.keys()].sort()) {
    out[parent] = childrenByParent.get(parent)!.map((e) => e.seq);
  }
  return out;
}

// Derived settled-turn state: is a turn in flight, and (once settled) how many
// run_in_background shells are still outstanding. Pins computeNativeWorking +
// idleBackgroundShells identically across clients — the metabar "waiting on N
// background shell" signal.
function projectState(events: NativeEvent[], meta: Record<string, unknown>, resultByCallId: Map<string, NativeEvent>) {
  const working =
    computeNativeWorking(
      events,
      (meta.pendingCount as number) ?? 0,
      (meta.agentMidTurn as boolean | undefined) ?? undefined,
      (meta.paneId as string) ?? 'p',
    ) != null;
  return { working, idleBgShells: idleBackgroundShells(events, resultByCallId, working) };
}

function loadFixture(name: string) {
  const dir = join(FIXTURES_DIR, name);
  const events: NativeEvent[] = readFileSync(join(dir, 'input.jsonl'), 'utf8')
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)
    .map((l) => JSON.parse(l) as NativeEvent);
  const meta = JSON.parse(readFileSync(join(dir, 'meta.json'), 'utf8'));
  return { dir, events, meta };
}

// Generate (UPDATE or first run) or assert. JSON is pretty-printed so the committed
// goldens read as a reviewable diff.
function checkOrWrite(file: string, actual: unknown) {
  const json = JSON.stringify(actual, null, 2) + '\n';
  if (UPDATE || !existsSync(file)) {
    writeFileSync(file, json);
    return;
  }
  expect(JSON.parse(readFileSync(file, 'utf8'))).toEqual(actual);
}

const cases = readdirSync(FIXTURES_DIR, { withFileTypes: true })
  .filter((d) => d.isDirectory())
  .map((d) => d.name)
  .sort();

describe('native render fixtures (shared web↔Flutter golden suite)', () => {
  it('has fixtures to run', () => {
    expect(cases.length).toBeGreaterThan(0);
  });

  for (const name of cases) {
    it(name, () => {
      const { dir, events, meta } = loadFixture(name);
      const t = buildTranscript(events, meta.agentMidTurn ?? undefined);
      checkOrWrite(join(dir, 'expected.focus.json'), projectRows(groupRows(t.rows)));
      // expected.transcript.json only exists for fixtures with result annotations.
      const tproj = projectTranscript(t.resultByCallId);
      const tf = join(dir, 'expected.transcript.json');
      if (Object.keys(tproj).length > 0 || existsSync(tf)) checkOrWrite(tf, tproj);
      // expected.children.json only exists for fixtures with sub-agent nesting.
      const cproj = projectChildren(t.childrenByParent);
      const cf = join(dir, 'expected.children.json');
      if (Object.keys(cproj).length > 0 || existsSync(cf)) checkOrWrite(cf, cproj);
      // expected.state.json only exists for fixtures exercising settled-turn
      // background-shell waiting (idleBgShells > 0).
      const sproj = projectState(events, meta, t.resultByCallId);
      const sf = join(dir, 'expected.state.json');
      if (sproj.idleBgShells > 0 || existsSync(sf)) checkOrWrite(sf, sproj);
    });
  }
});
