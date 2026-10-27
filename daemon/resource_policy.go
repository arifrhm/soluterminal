package main

import (
	"os"

	"strconv"
	"strings"
	"time"
)

const (
	stageTimeoutDefaultSec = 1800
	stageTimeoutMaxSec     = 14400
	maxRepairLoopsDefault  = 2
	maxRepairLoopsMax      = 5
	landRepairLoopsDefault = 1
	landRepairLoopsMax     = 3
	maxInvocationsDefault  = 20
	maxInvocationsMax      = 100
	maxRefutersDefault     = 5
	maxRefutersMax         = 20
	maxArtifactRetries     = 2
	failFastHeavySec       = 300
	suiteClassSec          = 300
	heavyAgeBoost          = 20 * time.Minute
	contentionRetriesMax   = 3
	verifyLedgerTTL        = 7 * 24 * time.Hour
	verifyTrainWindow      = 3 * time.Minute
	quarantineMax          = 10
	classifyTimeoutDefault = 60 * time.Second
	classifyRetries        = 1
	evidenceTimeoutDefault = 10 * time.Minute
	evidenceRetries        = 1
	screenshotMaxBytes     = 8 << 20
	// Ship-check reports are self-contained HTML and embed screenshots as data URIs.
	shipCheckReportMaxBytes = 16 << 20
	captureTimeoutDefault   = 10 * time.Minute
	previewAcceptTimeout    = 15 * time.Minute
	// playwrightMajor gates Playwright module-candidate compatibility:
	// resolution rejects candidates whose package.json major differs, so a
	// stale global install can never shadow the pinned worktree dependency.
	playwrightMajor = 1
	// maxResumesDefault bounds unbounded in-place resumes; retry-fresh is the escape hatch.
	maxResumesDefault = 10
	// maxConcurrentTasksDefault bounds simultaneously-driven tasks — each
	// running stage is a full model worker plus (at verify) a whole test
	// suite, and this machine has real OOM history under agent overload.
	// Overridable: SOLU_WORKFLOW_MAX_CONCURRENT (clamped 1..50). Queued
	// tasks stay visible in the list as pending until a slot frees.
	maxConcurrentTasksDefault = 10
	// Hard ceiling 50: push-the-limits by policy — the user raises their own
	// cap in Settings; if the machine complains we fix that, and adaptive
	// resource-aware concurrency is the eventual smarter answer.
	maxConcurrentTasksMax = 50
	// Heavy stages are locally expensive in a way API-bound model workers are
	// not: npm ci, go test, and Playwright can each consume multi-GB RSS. Keep
	// their concurrency deliberately low after repeated host OOM incidents.
	// Overridable: SOLU_WORKFLOW_MAX_HEAVY (clamped 1..8). When unset, machines
	// with less than 24 GB total memory use one slot; larger/unknown machines
	// use the static default.
	//
	// The adaptive layer is now the pressure controller, while the 1..8 clamp
	// remains the absolute bound. Cost-aware admission still retains a static
	// ceiling in depth: a command that grows or a missing probe can make an
	// estimate permissive, but can never exceed that ceiling.
	maxHeavyOpsDefault = 2
	maxHeavyOpsMax     = 8
	heavyCPUReserve    = 4
	// A normal cost grant is comfortable only when the request consumes no
	// more than half of every governed pre-grant headroom dimension. This
	// margin is the live-fit proof used by the adaptive controller when a
	// healthy queued command needs one more within-advisory semaphore slot.
	heavyAdmissionComfortMargin = 2
	// Post-defer cost escapes are paced globally so a cohort expiring in one
	// poll tick cannot stampede the host. Measured load is the feedback loop.
	//
	// overdueGrantInterval is the historical fixed interval, retained for
	// callers and tests that pin the original global pacing exactly. Production
	// now resolves Settings.OverdueGrantSec, then
	// SOLU_WORKFLOW_OVERDUE_GRANT_SEC, then overdueGrantIntervalDefault.
	// Measurement 2026-09-20: one land command recorded costMs 1,447,597 (24
	// min) while heavyInUse was 3-4 with heavyCap 4-5 and heavyQueued 3-6, so
	// the old 60s global interval dominated the wait. 15s keeps the same
	// anti-stampede bound at a fraction of the throughput cost.
	//
	// Pre-change baseline, read from the live task store's land-dwell report over
	// three 24h windows ending 2026-09-21T16Z (17,950 recorded admissions): land
	// dwell p50 3m29s / 8m07s / 3m06s and p90 1h32m / 1h02m / 11m; land
	// resource-wait 1,421s / 5,203s / 7,485s; rebase-conflict tasks replaced by a
	// reconciliation successor 3 of 3, 3 of 4 and 5 of 5. The 86 paced
	// ("overdue-clear") grants waited p50 5-10 min and p90 18-25 min each, and
	// none was admitted at or above admitLoadLimit (max 23.8 of 24). Compare an
	// equal window after release through the land-dwell report and its pacing breakdown.
	overdueGrantInterval = 60 * time.Second
	// overdueGrantIntervalDefault is the production measured default.
	overdueGrantIntervalDefault = 15 * time.Second
	// overdueGrantSecMax bounds the tunable interval in seconds.
	overdueGrantSecMax = 300
	// overdueComfortLoadRatio treats a measured load at or below half of
	// admitLoadLimit as comfortably below it: a fitting queue head may take the
	// head-only overdue grant immediately (interval 0) while a spare heavy slot
	// exists. admitLoadLimit remains the hard gate and is never approached by
	// the fast path.
	overdueComfortLoadRatio = 0.5
	// Preview build/browser work has no per-repository learned key, so charge a
	// conservative fixed cost while it holds the shared heavy slot.
	previewHeavyCPUWeight = 2.0
	previewHeavyWeightMB  = 2048
	// Governed mode enters at 3; its ceiling is the live machine-derived
	// heavy static ceiling, never a separate fixed cap.
	adaptiveGovernedInitialCap = 3
	// New heavy work waits while MemAvailable is below this floor. When
	// SOLU_WORKFLOW_MIN_FREE_MB is unset, the floor is the greater of 2048 MB
	// and 12% of MemTotal. Zero explicitly disables the memory gate.
	memFloorDefaultMB = 2048
	// SOLU_WORKFLOW_GOCACHE_CAP_MB bounds the shared Go build cache. The
	// default is 20 GB; zero explicitly disables the cache janitor.
	goCacheCapDefaultMB = 20480
	// SOLU_WORKFLOW_MIN_FREE_DISK_MB is the free-space floor for admitting new
	// heavy work. The default is 10 GB; zero explicitly disables the disk gate.
	diskFloorDefaultMB = 10240
	// WSL has two capacity layers: the guest filesystem and the Windows volume
	// containing ext4.vhdx. The incident volume was about 2.05 TB, where 5% is
	// about 102.5 GiB. The 100 GiB absolute backstop is sized to workload
	// consumption: a browser-heavy verification run plus VHDX growth and
	// shadow-copy churn consumed the 86.5 GB that was free during the 2026-08-25
	// incident. The launcher preflight deliberately does not inherit these
	// workflow admission-defer values.
	hostDiskFloorDefaultMB  = 102400
	hostDiskFloorDefaultPct = 5
	// These mirror HostDiskPolicy::default() in desktop/src-tauri/src/daemon.rs
	// (16 GiB / 3%) and must be kept in sync manually. The launcher preflight
	// blocks WSL start, so its start-refusal floor must stay far below the
	// workflow admission-defer floor.
	launcherHostDiskDefaultMB   = 16384
	launcherHostDiskDefaultPct  = 3
	hostDiskHysteresisDefaultMB = 4096
	browserHostReserveDefaultMB = 8192
	hostDiskFloorPctMax         = 50
	// Adaptive defaults apply only while their environment overrides are unset.
	adaptiveSmallMemTotalKB int64 = 24 * 1024 * 1024
	memFloorPercent               = 12
)

// Precedence layer that produced a resolved cap. Reported in stats and shown
// in the Tasks Settings UI so a stale override is visible at a glance instead
// of silently governing admission.
const (
	capSourceSettings   = "settings"
	capSourceEnv        = "env"
	capSourceAdaptive   = "adaptive"
	capSourceController = "controller"
	capSourceFloor      = "settings-floor"
)

// resolvedCap is one resource knob's effective value plus its provenance.
type resolvedCap struct {
	Value  int
	Source string
}

// resolveConcurrentCap resolves the engine-wide parallel-task cap.
func resolveConcurrentCap(override *int) resolvedCap {
	if override != nil {
		return resolvedCap{Value: min(maxConcurrentTasksMax, max(1, *override)), Source: capSourceSettings}
	}
	if raw := strings.TrimSpace(os.Getenv("SOLU_WORKFLOW_MAX_CONCURRENT")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 1 {
			return resolvedCap{Value: min(maxConcurrentTasksMax, n), Source: capSourceEnv}
		}
	}
	return resolvedCap{Value: maxConcurrentTasksDefault, Source: capSourceAdaptive}
}

// resolveHeavyCap resolves the engine-wide locally-heavy-operation cap.
func resolveHeavyCap(override *int) resolvedCap {
	if override != nil {
		return resolvedCap{Value: min(maxHeavyOpsMax, max(1, *override)), Source: capSourceSettings}
	}
	if raw := strings.TrimSpace(os.Getenv("SOLU_WORKFLOW_MAX_HEAVY")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 1 {
			return resolvedCap{Value: min(maxHeavyOpsMax, n), Source: capSourceEnv}
		}
	}
	memTotalKB, ok := memTotalProbe()
	return resolvedCap{Value: adaptiveHeavyCap(memTotalKB, ok), Source: capSourceAdaptive}
}

// These run-pool caps differ from the workflow semaphores above: ScheduleStore
// re-resolves them on every admission through runCapsUnlocked. A Settings PUT
// therefore governs the very next fire without a separate push or restart.
func resolveAgentRunsCap(override *int) resolvedCap {
	if override != nil {
		return resolvedCap{Value: min(agentRunsCeiling, max(1, *override)), Source: capSourceSettings}
	}
	if raw := strings.TrimSpace(os.Getenv(agentRunsEnv)); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 1 {
			return resolvedCap{Value: min(agentRunsCeiling, n), Source: capSourceEnv}
		}
	}
	return resolvedCap{Value: agentRunsDefault, Source: capSourceAdaptive}
}

func resolveReservedRunsCap(override *int) resolvedCap {
	if override != nil {
		return resolvedCap{Value: min(agentRunsReservedCeiling, max(0, *override)), Source: capSourceSettings}
	}
	if raw := strings.TrimSpace(os.Getenv(agentRunsReservedEnv)); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			return resolvedCap{Value: min(agentRunsReservedCeiling, n), Source: capSourceEnv}
		}
	}
	return resolvedCap{Value: agentRunsReservedDefault, Source: capSourceAdaptive}
}

// resolveMemFloorMB resolves the MemAvailable floor for heavy admission.
func resolveMemFloorMB(override *int) resolvedCap {
	if override != nil {
		return resolvedCap{Value: max(0, *override), Source: capSourceSettings}
	}
	if raw := strings.TrimSpace(os.Getenv("SOLU_WORKFLOW_MIN_FREE_MB")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			return resolvedCap{Value: n, Source: capSourceEnv}
		}
	}
	memTotalKB, ok := memTotalProbe()
	return resolvedCap{Value: adaptiveMemFloorMB(memTotalKB, ok), Source: capSourceAdaptive}
}

// resolveDiskFloorMBOverride resolves the free-disk floor for heavy admission.
func resolveDiskFloorMBOverride(override *int) resolvedCap {
	if override != nil {
		return resolvedCap{Value: max(0, *override), Source: capSourceSettings}
	}
	if raw := strings.TrimSpace(os.Getenv("SOLU_WORKFLOW_MIN_FREE_DISK_MB")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			return resolvedCap{Value: n, Source: capSourceEnv}
		}
	}
	return resolvedCap{Value: diskFloorDefaultMB, Source: capSourceAdaptive}
}

func resolveHostDiskFloorMBOverride(override *int) resolvedCap {
	return resolveNonNegativeCap(override, "SOLU_WORKFLOW_MIN_FREE_HOST_DISK_MB", hostDiskFloorDefaultMB, 0)
}

func resolveHostDiskFloorPctOverride(override *int) resolvedCap {
	return resolveNonNegativeCap(override, "SOLU_WORKFLOW_MIN_FREE_HOST_DISK_PCT", hostDiskFloorDefaultPct, hostDiskFloorPctMax)
}

func resolveHostDiskHysteresisMBOverride(override *int) resolvedCap {
	return resolveNonNegativeCap(override, "SOLU_WORKFLOW_HOST_DISK_HYSTERESIS_MB", hostDiskHysteresisDefaultMB, 0)
}

func resolveBrowserHostReserveMBOverride(override *int) resolvedCap {
	return resolveNonNegativeCap(override, "SOLU_WORKFLOW_BROWSER_HOST_RESERVE_MB", browserHostReserveDefaultMB, 0)
}

func resolveNonNegativeCap(override *int, envName string, fallback, upper int) resolvedCap {
	clamp := func(n int) int {
		n = max(0, n)
		if upper > 0 {
			n = min(upper, n)
		}
		return n
	}
	if override != nil {
		return resolvedCap{Value: clamp(*override), Source: capSourceSettings}
	}
	if raw := strings.TrimSpace(os.Getenv(envName)); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			return resolvedCap{Value: clamp(n), Source: capSourceEnv}
		}
	}
	return resolvedCap{Value: fallback, Source: capSourceAdaptive}
}

func hostDiskEffectiveFloorMB(totalMB int64, absMB, pct int) int64 {
	if absMB <= 0 && pct <= 0 {
		return 0
	}
	return max(int64(max(0, absMB)), totalMB*int64(max(0, pct))/100)
}

// hostDiskGateDisabled reports whether host workflow gating is explicitly
// switched off: both the absolute and percentage floors resolve to 0 from a
// non-adaptive source (settings or env). Adaptive defaults are never 0, so this
// is equivalent to checking the resolved values, but the source check documents
// intent and keeps a future adaptive-0 default from silently disabling gating.
func hostDiskGateDisabled(abs, pct resolvedCap) bool {
	return abs.Value <= 0 && pct.Value <= 0 && abs.Source != capSourceAdaptive && pct.Source != capSourceAdaptive
}

// resolvedFlag is a boolean knob's effective value plus its provenance —
// the resolvedCap contract for policies that are on/off rather than sized.
type resolvedFlag struct {
	Value  bool
	Source string
}

// resolveCostAware resolves whether heavy admission reserves learned
// per-command memory estimates against live headroom (on) or falls back to
// pure slot counting (off). Same precedence contract as the
// resolve…Override cap helpers: Settings override > env > default.
//
// The default is ON. Off is the pre-v3b behavior, retained as a kill switch
// because a mis-learned weight can only ever make admission MORE conservative
// (a too-large estimate delays work; the resourceDeferMax escape still
// fail-opens), but an operator debugging a stall deserves a one-flag way back
// to the policy they know.
//
// An unparsable value is treated as unset and falls through to the default,
// per the same rule the cap resolvers document: a typo in ~/.solu/env must
// never silently select a different policy than no override at all.
func resolveCostAware(override *bool) resolvedFlag {
	if override != nil {
		return resolvedFlag{Value: *override, Source: capSourceSettings}
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SOLU_WORKFLOW_COST_AWARE"))) {
	case "0", "false", "off", "no":
		return resolvedFlag{Value: false, Source: capSourceEnv}
	case "1", "true", "on", "yes":
		return resolvedFlag{Value: true, Source: capSourceEnv}
	}
	return resolvedFlag{Value: true, Source: capSourceAdaptive}
}

// resolveAdaptiveHeavy is the controller kill switch. Settings wins over
// environment; unset defaults on so rollout can progress without cap tuning.
func resolveAdaptiveHeavy(override *bool) resolvedFlag {
	if override != nil {
		return resolvedFlag{Value: *override, Source: capSourceSettings}
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SOLU_WORKFLOW_ADAPTIVE_HEAVY"))) {
	case "0", "false", "off", "no":
		return resolvedFlag{Value: false, Source: capSourceEnv}
	case "1", "true", "on", "yes":
		return resolvedFlag{Value: true, Source: capSourceEnv}
	}
	return resolvedFlag{Value: true, Source: capSourceAdaptive}
}

// resolveOptInFlag resolves a default-off opt-in. Invalid environment values
// must never opt in.
func resolveOptInFlag(override *bool, envName string) resolvedFlag {
	if override != nil {
		return resolvedFlag{Value: *override, Source: capSourceSettings}
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envName))) {
	case "1", "true", "on", "yes":
		return resolvedFlag{Value: true, Source: capSourceEnv}
	case "0", "false", "off", "no":
		return resolvedFlag{Value: false, Source: capSourceEnv}
	}
	return resolvedFlag{Value: false, Source: capSourceAdaptive}
}
