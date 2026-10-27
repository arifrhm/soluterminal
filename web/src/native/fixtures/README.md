# Native render fixtures — the shared web↔Flutter golden suite

The native renderer's transcript-shaping passes (`buildTranscript` + `groupRows` +
the `bgShell`/`interrupted` annotations) are hand-written **twice**: in TS here
(`web/src/native/nativeTranscript.ts`) and in Dart
(`soluterminal-mobile` `lib/src/daemon/native_transcript.dart`). They can't share
code (React/TS vs Flutter/Dart), and the logic correctly lives client-side — the
daemon is render-blind and must stay schema-stable for older installs.
See the memory note `feedback_native_dual_render_parity` for the full rationale.

So we keep parity a different way: **specify each feature's behavior once, here, and
run the identical fixtures in both clients.** A missing Flutter port becomes a RED
Dart test, not a silent gap.

## Format — one directory per case under `fixtures/<case>/`

| file | meaning |
|---|---|
| `input.jsonl` | one wire-shape `NativeEvent` per line (exactly what the daemon emits) |
| `meta.json` | `{ agentMidTurn, pendingCount, paneId, fixtureFormatVersion }` |
| `expected.focus.json` | projection of `groupRows(buildTranscript(events, agentMidTurn).rows)` |
| `expected.transcript.json` | *(optional)* result annotations: `{ <callId>: { bgShell?, interrupted?, isError? } }` |

The **projection** is deliberately tiny and language-neutral — each render row →
`{ kind, seqs[] }`:
- an event row → `{ "kind": "event", "seqs": [<event seq>] }`
- a group (`tool-group` / `edit-group` / `step-group` / `thinking-group`) → `{ "kind": ..., "seqs": [<member seqs>] }`
- a turn separator → `{ "kind": "turn-sep", "seqs": [<the user-turn seq>] }`

That pins grouping / visibility / ordering — which is the whole contract. The
per-platform widget chrome (how a group LOOKS) is intentionally NOT part of it.

## Workflow

- **Web is the source of truth / generator.** Author `input.jsonl` + `meta.json` by
  hand, then `UPDATE_FIXTURES=1 npm test` (in `web/`) regenerates every
  `expected.*.json` from the current TS impl. Eyeball the diff, commit.
- **Sync into the Flutter repo:** `scripts/sync-native-fixtures.sh` copies this dir
  to `soluterminal-mobile/test/native_fixtures/`. Commit both.
- **The Dart suite only asserts** (`test/native_fixtures_test.dart`) — it never
  regenerates. If it goes red, the Dart renderer has drifted from the spec.

## Parity checklist for every NEW native render feature

1. Write the fixture (`input.jsonl` + `meta.json`) **before** coding either client.
2. Implement in web until `npm test` regenerates the expected you intend.
3. Run `scripts/sync-native-fixtures.sh`; commit both repos.
4. The SAME fixture is the Flutter acceptance test — the port is "done" only when
   `flutter test` passes it identically.

## Known gap (future hardening)

The sync is a manual script, so "web added a fixture but forgot to sync" isn't
auto-caught across the two repos. A pre-commit / CI step that runs the sync and
fails on a non-empty diff is the eventual guard; for now the discipline above
(edit canonical → sync → commit both) is the contract.
