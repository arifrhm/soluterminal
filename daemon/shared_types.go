package main

import (
	"os"

	"strconv"
	"strings"
)

// Stage identifiers. The set is open (stored as strings, plans validated at
// submit) so later stages (judge, …) arrive without a state migration.
const (
	stgIntake   = "intake"
	stgClassify = "classify"
	// review_evidence, like classify, is an invocation/skill/telemetry
	// identifier only. It is never inserted into a task's StagePlan.
	stgEvidence = "review_evidence"
	// refute is another internal pseudo-stage. One read-only invocation examines
	// one admitted gating finding; it never appears in StagePlan.
	stgRefute = "refute"
	// triage is another internal pseudo-stage. One read-only invocation recommends
	// a resolution for one stuck task; it never appears in StagePlan.
	stgTriage = "triage"
	stgPlan   = "plan"
	stgScout  = "scout"
	stgSpec   = "spec"
	// challenge appears in a StagePlan only for complex/critical tiers when
	// the challenger feature flag is on.
	stgChallenge     = "challenge"
	stgImplement     = "implement"
	stgVerify        = "verify"
	stgBrowserAccept = "browser_accept"
	stgReview        = "review"
	stgRereview      = "re_review"
	stgJudge         = "judge"
	stgRepair        = "repair"
	stgReverify      = "reverify"
	stgReland        = "reland"
	stgResolve       = "resolve_conflicts"
	stgApproval      = "human_approval"
	stgLand          = "land"
	stgComplete      = "complete"
)

const (
	roleClassifier         = "classifier"
	roleChallenger         = "challenger"
	roleAcceptanceAuthor   = "acceptance_author"
	roleBrowserDriver      = "browser_driver"
	roleAcceptanceVerifier = "acceptance_verifier"
	routePending           = "pending"
	routeOverride          = "override"
	routeClassifier        = "classifier"
	routeRisk              = "risk_floor"
	routeHeuristic         = "heuristic"
	routeSizeGuard         = "size_guardrail"
)

const roleJudge = "judge"

// Routing tiers. AUTO uses a model classifier first, deterministic heuristics
// on failure, and a corroborated critical risk floor.
const (
	tierAuto     = "auto"
	tierSmall    = "small"
	tierStandard = "standard"
	tierComplex  = "complex"
	tierCritical = "critical"
)

// Invocation outcomes.
const (
	outcomeOK            = "ok"
	outcomeError         = "error"
	outcomeTimeout       = "timeout"
	outcomeInterrupted   = "interrupted"
	outcomeInvalidOutput = "invalid_output"
)

// Timeout flavors. Both keep FailureKind failTimeout so existing filters,
// resume gating and old events keep meaning; TimeoutKind is additive detail.
// idle = no observable worker output for the idle window (a hang); hard = the
// absolute cap elapsed while the worker was still producing.
const (
	timeoutIdle = "idle_timeout"
	timeoutHard = "hard_timeout"
)

// Bounded invocation failure details. Quota/auth and timeout details reuse
// their existing persisted values so telemetry never develops competing
// names for the same classification.
const (
	detailBinaryMissing    = "binary_missing"
	detailNodeMissing      = "node_missing"
	detailModelCapacity    = "model_capacity"
	detailModelUnsupported = "model_unsupported"
	// detailBinaryMissingPreflight appears only on task.fallback_engaged.detail:
	// the head runtime was observed missing (LookPath/stat, never a spawn) before
	// dispatch and an installed later entry took over WITHOUT any spawn, so there
	// is no invocation to carry it. It is never an invocation FailureDetail, and
	// infraDetailIsUnhealable deliberately excludes it — nothing ever feeds it
	// there from errDetail.
	detailBinaryMissingPreflight = "binary_missing_preflight"
	detailContract               = "contract"
	// detailSilentExit: the worker exited nonzero having written ZERO bytes
	// to stdout+stderr. That is an environment/spawn fault — an env-starved
	// child, a crashed shim, a missing runtime dependency — never worker
	// logic, because the worker never got far enough to have any. The
	// invocation carries the verbatim exit status and the spawn argv.
	detailSilentExit = "silent_exit"
	// detailMaxTurns: the worker hit its --max-turns ceiling and produced no
	// artifact at all. Triage is the only stage that passes a ceiling, so this
	// detail is triage-specific in practice. It is its own classification
	// because the run was billed in full for nothing, which is a spend decision
	// (raise the ceiling, tighten the prompt, or stop re-running) rather than
	// the generic "unknown worker failure" it used to read as.
	detailMaxTurns      = "max_turns"
	detailContentSafety = "content_safety_flag"
	detailUnknown       = "unknown"
)

// envRequirement is one materialized environment precondition, checked
// inside the task's worktree before an executor or verify stage runs. Exactly
// one of Path (worktree-relative existence) or Exec (bare name on the daemon's
// PATH) is set; Name labels the failure. Detection only — an unmet requirement
// fails the task, and the engine never provisions anything.
type envRequirement struct {
	Name     string            `json:"name,omitempty"`
	Path     string            `json:"path,omitempty"`
	Exec     string            `json:"exec,omitempty"`
	ToolPath string            `json:"toolPath,omitempty"`
	Probe    *capabilityRecipe `json:"probe,omitempty"`
}

// A declaration is frozen at task creation. These recipes never contain a
// shell, arbitrary arguments, environment entries or provider credentials.
type capabilityRecipe struct {
	Kind                 string `json:"kind"`
	VersionArg           string `json:"versionArg,omitempty"`
	RequiresScratchWrite bool   `json:"requiresScratchWrite,omitempty"`
	InspectorExec        string `json:"inspectorExec,omitempty"`
	InspectorToolPath    string `json:"inspectorToolPath,omitempty"`
}

func adaptiveHeavyCap(memTotalKB int64, ok bool) int {
	if ok && memTotalKB < adaptiveSmallMemTotalKB {
		return 1
	}
	return maxHeavyOpsDefault
}

func adaptiveMemFloorMB(memTotalKB int64, ok bool) int {
	if !ok {
		return memFloorDefaultMB
	}
	derived := int((memTotalKB / 1024) * memFloorPercent / 100)
	if derived < memFloorDefaultMB {
		return memFloorDefaultMB
	}
	return derived
}

// goCacheCapMB resolves the shared Go build-cache cap.
func goCacheCapMB() int {
	raw := strings.TrimSpace(os.Getenv("SOLU_WORKFLOW_GOCACHE_CAP_MB"))
	if raw == "" {
		return goCacheCapDefaultMB
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return goCacheCapDefaultMB
	}
	return n
}

// Task status values.
const (
	statusPending           = "pending"
	statusRunning           = "running"
	statusAwaitingApproval  = "awaiting_approval"
	statusCompleted         = "completed"
	statusFailed            = "failed"
	statusCancelled         = "cancelled"
	statusQuotaPaused       = "paused_for_quota"
	statusEnvironmentPaused = "paused_for_environment"
	statusBudgetPaused      = "paused_for_budget"
	statusFallbackPaused    = "paused_for_fallback"
)

// Landing outcomes distinguish deliberate completed policies from a merge.
// Command failures fail the land stage and do not carry an outcome.
// "reconciled" means a needs_merge branch was later found (or made) an
// ancestor of the repository HEAD — it counts as landed everywhere merged does.
const (
	landMerged     = "merged"
	landNeedsMerge = "needs_merge"
	landNothing    = "nothing_to_land"
	landReconciled = "reconciled"
)

func (p *capabilityRecipe) UnmarshalJSON(data []byte) error {
	type plain capabilityRecipe
	var decoded plain
	if err := strictCapabilityJSON(data, &decoded); err != nil {
		return err
	}
	*p = capabilityRecipe(decoded)
	return nil
}

func (r *envRequirement) UnmarshalJSON(data []byte) error {
	type plain envRequirement
	var decoded plain
	if err := strictCapabilityJSON(data, &decoded); err != nil {
		return err
	}
	*r = envRequirement(decoded)
	return nil
}

func measuredHostDiskFloorMB(stats hostGrowthStats, sparse bool) int64 {
	base := adaptiveHostDiskSafetyMB
	if sparse {
		base = adaptiveHostDiskSparseSafetyMB
	}
	return min(int64(hostDiskFloorDefaultMB), max(adaptiveHostDiskFloorMinMB, base+stats.WindowMBP99))
}

const (
	adaptiveHostDiskFloorMinMB     int64 = 32768
	adaptiveHostDiskSafetyMB       int64 = 24576
	adaptiveHostDiskSparseSafetyMB int64 = 16384
)
