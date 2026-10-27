# Go race CI gate audit — 2026-08-16

## CI red window

The last green `build` run was for `a3f0bba0` (`Prevent stale review evidence
reuse after resume`) at 2026-08-10T08:23:24Z. The first red run was at
2026-08-10T14:06:16Z for `6af7814e` (`repair deferred workflow resume
recovery`). The gate remained red through run 31936616757 on 2026-08-16
(`70f2acd4`), a six-calendar-day window covering at least ten pushes to main.

Run 31936616757 and the earlier failures identify approximately 46 workflow
race reports. The executor environment could not download the logs again:
`XDG_CACHE_HOME=/tmp gh run view 31936616757 --log-failed` could not connect to
`api.github.com`. The classification below therefore combines the run evidence
captured in the task artifact with local race-detector reproduction.

There is an important deployment-gate discrepancy. At this commit,
`.github/workflows/build.yml` defines `deploy-render.needs` as `web`,
`go-tests`, `daemon-windows-check`, `docker-build`, and `release-dry-run`; it
does **not** include `go-race`. Therefore a red `go-race` job makes the build
workflow red but does not directly prevent `deploy-render` from running. The
reported plain `go-tests` failures are direct deploy blockers. If `go-race` is
intended to guard Render, adding it to `deploy-render.needs` is a separate CI
change.

## Race classification and fixes

| Family | Conflicting accesses | Classification | Resolution |
| --- | --- | --- | --- |
| `wfEngine.tasks` / registered handles | Test fixtures wrote `e.tasks` without `e.mu` while the engine-lifetime `blockWatcher` called `List`; digest tests also mutated `h.t` while `snapshotOf` read it | Test-fixture race | Added `wfRegisterTaskHandle`, converted every unprotected registration found by the audit, and put post-registration digest/push mutations under `h.mu`. Existing registrations already enclosed by `e.mu` remain valid. |
| `ariaClient` | Todo tests swapped the package client while `normalizeTodoAsync`'s fire-and-forget usage reporter loaded it | Test seam/global swap race, not an ARIA state-machine race | Changed the swappable client to `atomic.Pointer[http.Client]`; every production caller uses `Load`, and tests use `Load`/`Store` for swap and cleanup. |
| Dependency helpers | CI named dependency intent/cycle tests, but their write stack was the shared engine fixture; `wfIntentBlockersKnown`, `wfDepCyclePath`, and `wfDetectDepCycle` are pure | Test-fixture race | Locked fixture registration only; no dependency-engine change. |

No report had non-test engine code on both sides. The engine read paths already
use the intended locks: `List` holds `e.mu`, and `snapshotOf` holds `h.mu`.
Digest scheduling, narrator state, autopilot policy, dependency cascade, and
release-cut code did not require production synchronization changes.

## Verification and resource measurements

The CI-named race regression set passed five consecutive runs:

```text
go test -race -count=5 -run 'TestDigestDisabledSkipsQualifyingTasks|TestWorkflowReverifyBlocksTestWeakening|TestWfAutopilotRepositoryOptOutOnlyNarrows|TestWfAutopilotNarrowestClassWins|TestWfDepCyclePathShapes|TestWfIntentBlockersKnown|TestWfAutopilotLevelGatesEveryClass|TestNormalizeTodoAsync' ./...
ok soluterminal 21.747s
```

An expanded set covering the API, recovery, triage, evidence, MCP, and queue
fixtures passed twice under `-race` in 13.372s. A race-enabled compile-only run
and `go vet ./...` also passed.

The exact full-suite plain and race commands were invoked through
`scripts/check-daemon-test-rss.sh` with `GOMAXPROCS=4`, `GOGC=50`,
`GOMEMLIMIT=2500MiB`, a 10 GiB RSS budget, and `-p 2`. This isolated executor
denies socket creation, so both invocations stopped in unrelated listener tests
before providing valid full-suite measurements:

| Invocation | Observed wall time | Observed peak RSS | Why the value is not a full-suite baseline |
| --- | ---: | ---: | --- |
| plain `go test -p 2 ./...` | 2.09s | 401 MB | `TestRebindSurvivesOldListenerClose` and `httptest.NewServer` failed with `operation not permitted` |
| `go test -race -p 2 ./...` | 2.87s | 375 MB | the same sandbox socket prohibition stopped the run |

For a comparable successful measurement, the eight-test CI regression set was
run through the same wrapper after warming the build cache: plain was 5.80s / 488
MB peak RSS and race was 6.14s / 399 MB peak RSS. Compiler overlap and 200ms RSS
sampling make these small-set memory figures noisy; they must not be
extrapolated to the full suite. A network-capable host or CI runner must record
the final full-suite wall time and peak RSS before changing the verify budget.

## Verify-gate recommendation

Prefer a full daemon `go test -race -p 2 ./...` verify step. This incident
crossed digest, autopilot, dependency, API, recovery, and todo fixtures, so a
short name-based subset would recreate the blind spot. Keep the existing 10
GiB process-tree RSS wrapper and 3600s timeout, but benchmark with a race-specific
`GOMEMLIMIT` (start at 5 GiB rather than the plain suite's 2500 MiB) because race
instrumentation commonly needs materially more memory. Land the verify change
only after a network-capable measurement demonstrates that it fits both limits.

If the measured full race suite does not fit, extend the existing targeted
`verify[4]` race sweep to the workflow/digest/autopilot/recovery tests plus
`TestNormalizeTodoAsync`, and keep the full GitHub Actions `go-race` job as the
backstop. That is a cost fallback, not the preferred gate. This task intentionally
does not edit `.solu/workflow.json`.

## Addendum 2026-08-17 (second race batch)

A second new-race batch landed within two days of the first (run 32001573853,
commit ddb82342). This race lived in production code: `wfReleaseTrainRunCmd`
fed its stdout/stderr tee into an unsynchronized `wfTailBuf`, which no
name-based sweep would have executed. That strengthens this audit's preference
for a full `go test -race -p 2 ./...` verify step. As an interim fallback, this
task extends `verify[4]` with `TestReleaseTrainRunCmd|TestReleaseTrainPublish`
and adds a deterministic race-canary test. The full-race verify step remains
gated on the network-capable RSS/wall-time benchmark already specified above,
starting with a race-specific 5 GiB `GOMEMLIMIT`.

## Addendum 2026-08-18

The recommended full `go test -race -count=1 -p 2 ./...` verify step landed in
`.solu/workflow.json` as `go test full suite (race)`, with
`GOMEMLIMIT=2500MiB`, `SOLU_TEST_RSS_BUDGET_MB=9216`, and `timeoutSec=1200`.
The original 285–298 second sizing measurement came from the faster reference
worker/commit. A cold-cache run under the shipped caps took 390.2 seconds and
reported 862 MB peak RSS while exposing the recovery-sweep fixture race fixed
with the gate. The earlier 380.8-second / 827 MB run used
`GOMEMLIMIT=5120MiB` and `SOLU_TEST_RSS_BUDGET_MB=15360`, before the row was
calibrated, and is therefore historical rather than the shipped-row baseline.
The tuned budget retains about 23% headroom above the
`(p+1)*GOMEMLIMIT` minimum, while the row timeout now fires before the
1800-second small/standard verify stage cap. The interim `verify[4]`
supersession sweep remains as the load-dependent stress backstop.

## Other CI failures

The daemon `go-tests` matrix also reported plain failures outside the race
detector:

- `TestWorkflowReconcileNeedsMergeAndDependentWake` passed locally with
  `-count=5` (6.868s).
- `TestWorkflowAutoApplySettingsChangeMidIncidentReleasesFailurePush` could not
  be evaluated in this executor because its push recorder uses
  `httptest.NewServer`, which the sandbox rejects before the assertion. CI had
  reported its `triage_kill_switch` family failure, so it remains a possible
  deploy blocker until rerun on a normal host.

No unrelated CI job or plain-test behavior was changed here.

## Addendum 2026-08-28

The race gate's timeout layers were recalibrated from measured wall-clock and
peak-RSS data. A live quiet-host run from an executor worktree was attempted
but denied by the executor sandbox's command-approval policy before it could
start (`bash scripts/daemon-test-gate.sh -count=1 -run TestNoSuchTest`
returned "This command requires approval" even with the sandbox override
flag set, on a host already running two other worktrees' race suites at load
average 9.15/8.46/8.60). Per the documented fallback, the calibration instead
used the retained 2026-08-28 canonical-gate fleet completions as
contention-evidence inputs: the plain row at 593-673 seconds (865-2007 MB
peak RSS) and the race row at 627-776 seconds (1386-1558 MB peak RSS), 1-minute
load 15-30 across that window.

That data attributes the historical ~750-790-second race-suite kills, despite
a nominal 1800-second row outer, to the *shared* verify-stage
`wfCommandBudget` sitting at the tier default of 1800 seconds -- a config that
predated any explicit `timeouts` section -- rather than to the row's own
outer timeout or the Go `-timeout=25m` alarm: the plain suite and earlier
verify rows had already consumed most of that shared budget by the time the
race row started. The verify-stage hard cap is now pinned at 12000 seconds
across all four tiers in `.solu/workflow.json`, sized to cover the full
daemon+config verify row set rather than just the two full-package rows. The
inner Go alarm (25 minutes) and the race row's outer (1800 seconds) are
unchanged; both retain more than 50% margin over the measured wall-clock
inputs above. The authoritative calibration record, including the full
protocol and sizing arithmetic, lives in
`docs/ai-workflow/daemon-test-memory.md` under "2026-08-28 quiet-host
calibration".
