package main

// Storage observability gives a bounded, truthful inventory of the storage
// producers SoluTerminal can see. It deliberately separates:
//
//   - managed categories (workflow worktrees, retained task history, task temp,
//     shared Go/Cargo/npm caches) that existing owners may reclaim, from
//   - external or protected categories (user cache, repository build outputs,
//     recovery data, Windows Temp/crash dumps) that are read-only here.
//
// Every walk is bounded by target, entry, time and mount limits. Nested roots
// and hardlinks are deduplicated with one scan-wide identity set so totals
// cannot be inflated. Missing baselines, reboots, gaps and noisy movement
// produce explicit "unavailable"/"noise" states instead of invented causation.

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	storageProtectionManaged   = "managed"
	storageProtectionReadOnly  = "read-only"
	storageProtectionProtected = "protected"
)

const (
	storageCleanupNone       = "none"
	storageCleanupWorkflowGC = "workflow_gc"
	storageCleanupRetention  = "retention"
	storageCleanupGoJanitor  = "go_cache_janitor"
	storageCleanupGenerated  = "generated_storage_janitor"
)

// Stable category IDs. Producer samples and the API persist only these IDs plus
// numeric metadata; raw paths never cross the ledger boundary.
const (
	storageCatWorkflowWorktrees = "workflow_worktrees"
	storageCatRetainedHistory   = "retained_task_history"
	storageCatTaskTemp          = "task_temp"
	storageCatSharedGoBuild     = "shared_go_build"
	storageCatSharedGoMod       = "shared_go_mod"
	storageCatCargo             = "cargo_cache"
	storageCatNpm               = "npm_cache"
	storageCatManagedGoBuild    = "managed_go_build"
	storageCatManagedGoMod      = "managed_go_mod"
	storageCatManagedCargo      = "managed_cargo"
	storageCatManagedNpm        = "managed_npm"
	storageCatManagedPlaywright = "managed_playwright"
	storageCatUnownedSoluCache  = "unowned_soluterminal_cache"
	storageCatOtherSoluData     = "other_soluterminal_data"
	storageCatExternalUserCache = "external_user_cache"
	storageCatRepoBuildOutputs  = "repository_build_outputs"
	storageCatRepoRecovery      = "repository_recovery_state"
	storageCatProtectedRecovery = "protected_recovery_data"
	storageCatWindowsCrashDumps = "windows_wsl_crash_dumps"
	storageCatWindowsTemp       = "windows_temp_other"
)

// Capacity categories are derived from the capacity ledger samples and appear
// only in growth projections, never as inventory rows.
const (
	storageCatCapacityGuestFree = "capacity_guest_free"
	storageCatCapacityHostFree  = "capacity_host_free"
	storageCatCapacityVHDX      = "capacity_vhdx_allocated"
)

var storageCapacityCategoryLabels = map[string]string{
	storageCatCapacityGuestFree: "Linux guest free space",
	storageCatCapacityHostFree:  "Windows host free space",
	storageCatCapacityVHDX:      "VHDX physical allocation",
}

type storageCategoryDef struct {
	ID         string
	Label      string
	Protection string
	Cleanup    string
}

// storageCategoryDefinitions is the single ordered source of category policy.
// Legacy and override paths are not adopted by the generated-storage owner.
var storageCategoryDefinitions = []storageCategoryDef{
	{storageCatWorkflowWorktrees, "Workflow worktrees", storageProtectionManaged, storageCleanupWorkflowGC},
	{storageCatRetainedHistory, "Retained task history", storageProtectionManaged, storageCleanupRetention},
	{storageCatTaskTemp, "Task temp directories", storageProtectionManaged, storageCleanupWorkflowGC},
	{storageCatSharedGoBuild, "Legacy or external Go build cache", storageProtectionReadOnly, storageCleanupNone},
	{storageCatSharedGoMod, "Legacy Go module cache", storageProtectionReadOnly, storageCleanupNone},
	{storageCatCargo, "Legacy Cargo target cache", storageProtectionReadOnly, storageCleanupNone},
	{storageCatNpm, "Legacy npm content cache", storageProtectionReadOnly, storageCleanupNone},
	{storageCatManagedGoBuild, "Owned Go build cache", storageProtectionManaged, storageCleanupGenerated},
	{storageCatManagedGoMod, "Owned Go module cache", storageProtectionManaged, storageCleanupGenerated},
	{storageCatManagedCargo, "Owned Cargo target cache", storageProtectionManaged, storageCleanupGenerated},
	{storageCatManagedNpm, "Owned npm content cache", storageProtectionManaged, storageCleanupGenerated},
	{storageCatManagedPlaywright, "Owned Playwright browsers", storageProtectionManaged, storageCleanupGenerated},
	{storageCatUnownedSoluCache, "Other or unverified SoluTerminal cache", storageProtectionReadOnly, storageCleanupNone},
	{storageCatOtherSoluData, "Other SoluTerminal data", storageProtectionReadOnly, storageCleanupNone},
	{storageCatExternalUserCache, "External user cache", storageProtectionReadOnly, storageCleanupNone},
	{storageCatRepoBuildOutputs, "Repository build outputs", storageProtectionReadOnly, storageCleanupNone},
	{storageCatRepoRecovery, "Repository recovery/state", storageProtectionReadOnly, storageCleanupNone},
	{storageCatProtectedRecovery, "Protected recovery data", storageProtectionProtected, storageCleanupNone},
	{storageCatWindowsCrashDumps, "Windows WSL crash dumps", storageProtectionReadOnly, storageCleanupNone},
	{storageCatWindowsTemp, "Other Windows Temp", storageProtectionReadOnly, storageCleanupNone},
}

func storageCategoryDefinition(id string) storageCategoryDef {
	for _, def := range storageCategoryDefinitions {
		if def.ID == id {
			return def
		}
	}
	if label, ok := storageCapacityCategoryLabels[id]; ok {
		return storageCategoryDef{ID: id, Label: label, Protection: storageProtectionReadOnly, Cleanup: storageCleanupNone}
	}
	return storageCategoryDef{ID: id, Label: id, Protection: storageProtectionReadOnly, Cleanup: storageCleanupNone}
}

// StorageCapacitySamples converts persisted capacity events into the same
// producer-sample shape so growth projections share one bounded timeline.
// Operation and producer rows are ignored by capacity calculations.
func StorageCapacitySamples(events []hostStorageEvent) []storageProducerSample {
	out := make([]storageProducerSample, 0)
	for _, ev := range events {
		if ev.Kind != "sample" || ev.At.IsZero() {
			continue
		}
		categories := map[string]int64{}
		if ev.GuestFreeMB > 0 {
			categories[storageCatCapacityGuestFree] = ev.GuestFreeMB << 20
		}
		if ev.HostFreeMB > 0 {
			categories[storageCatCapacityHostFree] = ev.HostFreeMB << 20
		}
		if ev.VHDXPhysicalMB > 0 {
			categories[storageCatCapacityVHDX] = ev.VHDXPhysicalMB << 20
		}
		if len(categories) == 0 {
			continue
		}
		out = append(out, storageProducerSample{At: ev.At, BootID: ev.BootID, Complete: true, Categories: categories})
	}
	return out
}

// storageCategoryRow is one producer row. Bytes is the apparent-byte
// compatibility alias; allocated bytes are only populated when the platform
// supports block accounting and every file in the category was measured.
type storageCategoryRow struct {
	Category       string    `json:"category"`
	Label          string    `json:"label"`
	Bytes          int64     `json:"bytes"`
	ApparentBytes  int64     `json:"apparentBytes"`
	AllocatedBytes *int64    `json:"allocatedBytes,omitempty"`
	Measured       bool      `json:"measured"`
	Complete       bool      `json:"complete"`
	Protection     string    `json:"protection"`
	Cleanup        string    `json:"cleanup"`
	ScannedAt      time.Time `json:"scannedAt"`
	TargetCount    int       `json:"targetCount"`
	Reasons        []string  `json:"reasons,omitempty"`
}

type storageInventory struct {
	ScannedAt time.Time            `json:"scannedAt"`
	Complete  bool                 `json:"complete"`
	Rows      []storageCategoryRow `json:"rows"`
	Targets   int                  `json:"targets"`
	Reasons   []string             `json:"reasons,omitempty"`
}

type storageProducerSample struct {
	At         time.Time        `json:"at"`
	BootID     string           `json:"bootId,omitempty"`
	Complete   bool             `json:"complete"`
	Categories map[string]int64 `json:"categories"`
	Allocated  map[string]int64 `json:"allocated,omitempty"`
}

type storageCategoryDelta struct {
	Category      string `json:"category"`
	Label         string `json:"label"`
	BaselineBytes int64  `json:"baselineBytes"`
	LatestBytes   int64  `json:"latestBytes"`
	DeltaBytes    int64  `json:"deltaBytes"`
	Noise         bool   `json:"noise"`
}

// Growth continuity states. "ok" is the only state that carries per-category
// deltas; every other state is explicit rather than a zero delta.
const (
	storageContinuityOK         = "ok"
	storageContinuityNoBaseline = "no_baseline"
	storageContinuityStale      = "stale"
	storageContinuityGap        = "gap"
	storageContinuityReboot     = "reboot"
	storageContinuityIncomplete = "incomplete"
)

type storageGrowthWindow struct {
	Window     string                 `json:"window"`
	Available  bool                   `json:"available"`
	Continuity string                 `json:"continuity"`
	BaselineAt *time.Time             `json:"baselineAt,omitempty"`
	LatestAt   *time.Time             `json:"latestAt,omitempty"`
	Deltas     []storageCategoryDelta `json:"deltas,omitempty"`
	Reason     string                 `json:"reason,omitempty"`
}

type storageCleanupSkip struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// storageCleanupOperation is the normalized operation DTO shared by the ledger,
// API and CLI. Reclaimed fields stay nil for a dry run or an unverified
// operation; only a verified executed Windows op may claim host reclamation.
type storageCleanupOperation struct {
	At                   time.Time            `json:"at"`
	Kind                 string               `json:"kind"`
	Source               string               `json:"source,omitempty"`
	Layer                string               `json:"layer"`
	Executed             bool                 `json:"executed"`
	DryRun               bool                 `json:"dryRun"`
	Complete             bool                 `json:"complete"`
	Verified             bool                 `json:"verified"`
	ApparentBytes        *int64               `json:"apparentBytes,omitempty"`
	AllocatedBytes       *int64               `json:"allocatedBytes,omitempty"`
	ReclaimedBytes       *int64               `json:"reclaimedBytes,omitempty"`
	EstimatedBytes       *int64               `json:"estimatedBytes,omitempty"`
	GuestFreeBeforeBytes *int64               `json:"guestFreeBeforeBytes,omitempty"`
	GuestFreeAfterBytes  *int64               `json:"guestFreeAfterBytes,omitempty"`
	GuestFreeDeltaBytes  *int64               `json:"guestFreeDeltaBytes,omitempty"`
	HostFreeBeforeBytes  *int64               `json:"hostFreeBeforeBytes,omitempty"`
	HostFreeAfterBytes   *int64               `json:"hostFreeAfterBytes,omitempty"`
	HostFreeDeltaBytes   *int64               `json:"hostFreeDeltaBytes,omitempty"`
	VHDXBeforeBytes      *int64               `json:"vhdxBeforeBytes,omitempty"`
	VHDXAfterBytes       *int64               `json:"vhdxAfterBytes,omitempty"`
	Skipped              []storageCleanupSkip `json:"skipped,omitempty"`
	Detail               string               `json:"detail,omitempty"`
}

type storageCleanupSummary struct {
	ScannedAt                *time.Time                `json:"scannedAt,omitempty"`
	Complete                 bool                      `json:"complete"`
	ReclaimableBytes         *int64                    `json:"reclaimableBytes,omitempty"`
	ReclaimableTmpBytes      *int64                    `json:"reclaimableTmpBytes,omitempty"`
	ReclaimableWorktreeBytes *int64                    `json:"reclaimableWorktreeBytes,omitempty"`
	ReclaimedBytes           *int64                    `json:"reclaimedBytes,omitempty"`
	Operations               []storageCleanupOperation `json:"operations"`
	Skips                    []storageCleanupSkip      `json:"skips,omitempty"`
	State                    string                    `json:"state,omitempty"`
	Message                  string                    `json:"message,omitempty"`
}

const (
	storageStateGuestOnlyPendingHostReclaim = "guest_only_pending_host_reclaim"
	storageGuestOnlyMessage                 = "Linux space freed; Windows image still allocated; maintenance needed"
)

// storageScanLimits mirrors the sampler budgets. The zero value means "use the
// documented defaults".
type storageScanLimits struct {
	PerEntryTimeout    time.Duration
	TotalTimeout       time.Duration
	PerCategoryEntries int
	TotalEntries       int
	MaxTargets         int
	RepoCandidateLimit int
}

func (l storageScanLimits) withDefaults() storageScanLimits {
	if l.PerEntryTimeout <= 0 {
		l.PerEntryTimeout = systemBreakdownEntryTO
	}
	if l.TotalTimeout <= 0 {
		l.TotalTimeout = systemBreakdownTotalTO
	}
	if l.PerCategoryEntries <= 0 {
		l.PerCategoryEntries = 200000
	}
	if l.TotalEntries <= 0 {
		l.TotalEntries = 500000
	}
	if l.MaxTargets <= 0 {
		l.MaxTargets = 256
	}
	if l.RepoCandidateLimit <= 0 {
		l.RepoCandidateLimit = 64
	}
	return l
}

type storageScanTarget struct {
	Category string
	Path     string
	Exclude  []string
}

type storageScanIO struct {
	lstat    func(string) (os.FileInfo, error)
	readDir  func(string) ([]os.DirEntry, error)
	identity func(string) (dev, ino uint64, blocks int64, ok bool)
	device   func(string) (uint64, bool)
	now      func() time.Time
}

func newStorageScanIO() storageScanIO {
	return storageScanIO{
		lstat:    os.Lstat,
		readDir:  os.ReadDir,
		identity: storageStatIdentity,
		device:   storageRootDevice,
		now:      time.Now,
	}
}

type storageScanRequest struct {
	DataDir   string
	RepoPaths []string
	Limits    storageScanLimits
	IO        storageScanIO
	// HostRows overrides/merges categories measured outside the guest walk
	// (Windows crash dumps and Temp bytes).
	HostRows []storageCategoryRow
}

// storageRepoBuildCandidates are the only repository subdirectories the
// inventory will discover. Discovery never walks a whole repository.
var storageRepoBuildCandidates = []string{
	"target", "node_modules", "dist", "build", "out", ".next", "coverage",
	".cache", ".state", ".backups", "backups", ".claude/worktrees",
}

func isStorageRepoBuildCandidate(name string) bool {
	for _, candidate := range storageRepoBuildCandidates {
		if name == candidate {
			return true
		}
	}
	return false
}

func storageSortRepoPaths(paths []string, limit int) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		clean := filepath.Clean(strings.TrimSpace(path))
		if clean == "" || clean == "." {
			continue
		}
		if seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
	}
	sort.Strings(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// buildStorageTargets produces the bounded, non-overlapping target list. Only
// existing paths are added so targetCount reflects real measured roots.
func buildStorageTargets(req storageScanRequest, limits storageScanLimits, lstat func(string) (os.FileInfo, error)) ([]storageScanTarget, []string) {
	truncated := false
	add := func(targets []storageScanTarget, category, path string, exclude ...string) []storageScanTarget {
		if len(targets) >= limits.MaxTargets {
			truncated = true
			return targets
		}
		if strings.TrimSpace(path) == "" {
			return targets
		}
		info, err := lstat(path)
		if err != nil || info == nil || !info.IsDir() {
			return targets
		}
		return append(targets, storageScanTarget{Category: category, Path: filepath.Clean(path), Exclude: exclude})
	}
	var targets []storageScanTarget
	if req.DataDir != "" {
		targets = add(targets, storageCatWorkflowWorktrees, filepath.Join(req.DataDir, "workflow", "worktrees"))
		targets = add(targets, storageCatRetainedHistory, filepath.Join(req.DataDir, "workflow", "tasks"))
		targets = add(targets, storageCatTaskTemp, filepath.Join(req.DataDir, "workflow", "tmp"))
		targets = add(targets, storageCatOtherSoluData, req.DataDir,
			filepath.Join(req.DataDir, "workflow", "worktrees"),
			filepath.Join(req.DataDir, "workflow", "tasks"),
			filepath.Join(req.DataDir, "workflow", "tmp"))
	}
	cacheRoot := xdgCacheRootPath()
	if dir, ok := sharedGoCacheDirPath(); ok {
		targets = add(targets, storageCatSharedGoBuild, dir)
	}
	if dir, ok := sharedGoModCacheDirPath(); ok {
		targets = add(targets, storageCatSharedGoMod, dir)
	}
	if dir, ok := sharedCargoTargetRootPath(); ok {
		targets = add(targets, storageCatCargo, dir)
	}
	if dir, ok := sharedNpmCacheDirPath(); ok {
		targets = add(targets, storageCatNpm, dir)
	}
	owned, reasons := storageGeneratedCacheTargets(req.DataDir, limits.MaxTargets)
	for _, target := range owned {
		targets = add(targets, target.Category, target.Path)
	}
	if cacheRoot != "" {
		targets = add(targets, storageCatProtectedRecovery, filepath.Join(cacheRoot, "soluterminal", "recovery"))
		// Nested known roots are excluded by the scan-wide root set. Unknown
		// and corrupt-marker roots remain visible without claiming ownership.
		targets = add(targets, storageCatUnownedSoluCache, filepath.Join(cacheRoot, "soluterminal"), filepath.Join(cacheRoot, "soluterminal", "recovery"))
		targets = add(targets, storageCatExternalUserCache, cacheRoot, filepath.Join(cacheRoot, "soluterminal"))
	}
	repos := storageSortRepoPaths(req.RepoPaths, 0)
	if len(repos) > limits.RepoCandidateLimit {
		truncated = true
		repos = repos[:limits.RepoCandidateLimit]
	}
	for _, repo := range repos {
		for _, candidate := range storageRepoBuildCandidates {
			if len(targets) >= limits.MaxTargets {
				truncated = true
				break
			}
			targets = add(targets, storageCatRepoBuildOutputs, filepath.Join(repo, filepath.FromSlash(candidate)))
		}
		targets = add(targets, storageCatRepoRecovery, filepath.Join(repo, ".solu"))
	}
	if truncated {
		reasons = append(reasons, "target_limit")
	}
	return targets, reasons
}

// Inventory must not call producer allocation helpers: even a missing registry
// is a valid read-only observation. Read the requested data directory, never a
// different engine's process-global registry. Bound metadata and record work.
func storageGeneratedCacheTargets(dataDir string, limit int) ([]storageScanTarget, []string) {
	if dataDir == "" {
		return nil, nil
	}
	f, err := os.Open(filepath.Join(dataDir, "workflow", "generated-storage", "v1", "registry.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, []string{"generated_registry_unreadable"}
	}
	defer f.Close()
	const maxRegistryBytes = 4 << 20
	raw, err := io.ReadAll(io.LimitReader(f, maxRegistryBytes+1))
	var registry generatedStorageRegistry
	if err != nil || len(raw) > maxRegistryBytes || json.Unmarshal(raw, &registry) != nil || registry.SchemaVersion != generatedStorageSchema {
		return nil, []string{"generated_registry_unreadable"}
	}
	categories := map[string]string{
		storageGoBuild: storageCatManagedGoBuild, storageGoMod: storageCatManagedGoMod,
		storageCargo: storageCatManagedCargo, storageNPM: storageCatManagedNpm,
		storagePlaywright: storageCatManagedPlaywright,
	}
	var targets []storageScanTarget
	var reasons []string
	if len(registry.Roots) > limit {
		registry.Roots = registry.Roots[:limit]
		reasons = append(reasons, "target_limit")
	}
	unverified := false
	for _, record := range registry.Roots {
		category, known := categories[record.Category]
		if !known || generatedValidateRecord(record) != nil {
			unverified = true
			continue
		}
		targets = append(targets, storageScanTarget{Category: category, Path: record.CanonicalPath})
	}
	if unverified {
		reasons = append(reasons, "generated_ownership_unverified")
	}
	return targets, reasons
}

type storageCategoryAccum struct {
	apparent   int64
	allocated  int64
	allocKnown bool
	targets    int
	entries    int
	complete   bool
	reasons    map[string]bool
}

func newStorageCategoryAccum() *storageCategoryAccum {
	return &storageCategoryAccum{allocKnown: true, complete: true, reasons: map[string]bool{}}
}

func (a *storageCategoryAccum) reason(reason string) {
	if a == nil || reason == "" {
		return
	}
	a.reasons[reason] = true
	a.complete = false
}

// ScanStorageInventory walks only the enumerated bounded roots. It follows no
// symlinks, refuses mount crossings, deduplicates hardlinks with one scan-wide
// inode set, excludes nested roots, and returns explicit lower-bound reasons.
func ScanStorageInventory(ctx context.Context, req storageScanRequest) storageInventory {
	limits := req.Limits.withDefaults()
	io := req.IO
	if io.lstat == nil {
		io.lstat = os.Lstat
	}
	if io.readDir == nil {
		io.readDir = os.ReadDir
	}
	if io.identity == nil {
		io.identity = storageStatIdentity
	}
	if io.device == nil {
		io.device = storageRootDevice
	}
	if io.now == nil {
		io.now = time.Now
	}
	scanCtx, cancel := context.WithTimeout(ctx, limits.TotalTimeout)
	defer cancel()

	targets, targetReasons := buildStorageTargets(req, limits, io.lstat)
	inventory := storageInventory{
		ScannedAt: io.now().UTC(),
		Complete:  true,
		Targets:   len(targets),
		Rows:      make([]storageCategoryRow, 0, len(storageCategoryDefinitions)),
	}
	reasons := map[string]bool{}
	for _, reason := range targetReasons {
		reasons[reason] = true
	}
	rootSet := make(map[string]bool, len(targets))
	for _, target := range targets {
		rootSet[target.Path] = true
	}
	accums := map[string]*storageCategoryAccum{}
	// totals entries counted across every category.
	totalEntries := 0
	// one identity set for the whole scan: hardlink dedupe and, on Linux,
	// mount device tracking.
	seenInodes := map[[2]uint64]bool{}

	for _, target := range targets {
		accum := accums[target.Category]
		if accum == nil {
			accum = newStorageCategoryAccum()
			accums[target.Category] = accum
		}
		accum.targets++
		if scanCtx.Err() != nil {
			inventory.Complete = false
			reasons["timeout"] = true
			accum.reason("timeout")
			break
		}
		excluded := normalizedPathSet(target.Exclude)
		rootDevice, deviceKnown := io.device(target.Path)
		entryCtx, cancelEntry := context.WithTimeout(scanCtx, limits.PerEntryTimeout)
		status := walkStorageTarget(entryCtx, target.Path, target.Path, rootDevice, deviceKnown, excluded, rootSet, io, limits, &totalEntries, seenInodes, accum)
		cancelEntry()
		if status == storageWalkTimeout {
			reasons["timeout"] = true
		}
		if status == storageWalkEntryLimit {
			reasons["entry_limit"] = true
		}
	}

	for _, def := range storageCategoryDefinitions {
		accum := accums[def.ID]
		row := storageCategoryRow{
			Category: def.ID, Label: def.Label, Protection: def.Protection, Cleanup: def.Cleanup,
			Complete: true, ScannedAt: inventory.ScannedAt,
		}
		if accum != nil {
			row.Bytes, row.ApparentBytes = accum.apparent, accum.apparent
			row.Measured = true
			row.Complete = accum.complete
			row.TargetCount = accum.targets
			if storageAllocatedSupported() && accum.allocKnown {
				allocated := accum.allocated
				row.AllocatedBytes = &allocated
			}
			row.Reasons = sortedReasonSet(accum.reasons)
			if !row.Complete {
				inventory.Complete = false
			}
		}
		inventory.Rows = append(inventory.Rows, row)
	}
	// Merge caller-supplied host rows (Windows crash dumps and Temp) by
	// replacing the placeholder row for the same category.
	for _, hostRow := range req.HostRows {
		def := storageCategoryDefinition(hostRow.Category)
		if hostRow.Label == "" {
			hostRow.Label = def.Label
		}
		if hostRow.Protection == "" {
			hostRow.Protection = def.Protection
		}
		if hostRow.Cleanup == "" {
			hostRow.Cleanup = def.Cleanup
		}
		if hostRow.ScannedAt.IsZero() {
			hostRow.ScannedAt = inventory.ScannedAt
		}
		replaced := false
		for i := range inventory.Rows {
			if inventory.Rows[i].Category == hostRow.Category {
				inventory.Rows[i] = hostRow
				replaced = true
				break
			}
		}
		if !replaced {
			inventory.Rows = append(inventory.Rows, hostRow)
		}
	}
	inventory.Reasons = sortedReasonSet(reasons)
	if len(inventory.Reasons) > 0 {
		inventory.Complete = false
	}
	return inventory
}

type storageWalkStatus int

const (
	storageWalkOK storageWalkStatus = iota
	storageWalkTimeout
	storageWalkEntryLimit
)

func normalizedPathSet(paths []string) map[string]bool {
	if len(paths) == 0 {
		return nil
	}
	out := make(map[string]bool, len(paths))
	for _, path := range paths {
		if clean := filepath.Clean(strings.TrimSpace(path)); clean != "" && clean != "." {
			out[clean] = true
		}
	}
	return out
}

func pathUnderAny(path string, set map[string]bool) bool {
	if len(set) == 0 {
		return false
	}
	if set[path] {
		return true
	}
	for root := range set {
		if strings.HasPrefix(path, root+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// walkStorageTarget iteratively walks one target. It never follows symlinks and
// reports explicit lower-bound reasons into accum.
func walkStorageTarget(
	ctx context.Context,
	root, dir string,
	rootDevice uint64,
	deviceKnown bool,
	excluded, rootSet map[string]bool,
	io storageScanIO,
	limits storageScanLimits,
	totalEntries *int,
	seenInodes map[[2]uint64]bool,
	accum *storageCategoryAccum,
) storageWalkStatus {
	stack := []string{dir}
	for len(stack) > 0 {
		if ctx.Err() != nil {
			accum.reason("timeout")
			return storageWalkTimeout
		}
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := io.readDir(current)
		if err != nil {
			accum.reason("read_error")
			continue
		}
		for _, entry := range entries {
			if ctx.Err() != nil {
				accum.reason("timeout")
				return storageWalkTimeout
			}
			path := filepath.Join(current, entry.Name())
			if pathUnderAny(path, excluded) {
				// Nested-root exclusion: counted under its own category.
				continue
			}
			info, err := io.lstat(path)
			if err != nil {
				accum.reason("read_error")
				continue
			}
			mode := info.Mode()
			if mode&os.ModeSymlink != 0 {
				// Never follow symlinks; they can escape the bounded root.
				continue
			}
			if mode.IsDir() {
				if rootSet[path] && path != root {
					// Overlapping target: it will be measured once, on its own.
					continue
				}
				if deviceKnown {
					if dev, ok := io.device(path); ok && dev != rootDevice {
						accum.reason("mount_boundary")
						continue
					}
				}
				stack = append(stack, path)
				continue
			}
			if !mode.IsRegular() {
				continue
			}
			accum.entries++
			*totalEntries++
			if *totalEntries > limits.TotalEntries || accum.entries > limits.PerCategoryEntries {
				accum.reason("entry_limit")
				return storageWalkEntryLimit
			}
			if dev, ino, blocks, ok := io.identity(path); ok {
				key := [2]uint64{dev, ino}
				if seenInodes[key] {
					// Hardlink already counted elsewhere in this scan.
					continue
				}
				seenInodes[key] = true
				accum.allocated += blocks * 512
			} else {
				accum.allocKnown = false
			}
			accum.apparent += info.Size()
		}
	}
	return storageWalkOK
}

func sortedReasonSet(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for reason := range set {
		out = append(out, reason)
	}
	sort.Strings(out)
	return out
}

// StorageWindowsHostRows converts the Windows offender measurement into the two
// read-only Windows categories. Crash-dump bytes come from the measured
// DumpBytes field alone; the remainder of Temp is a separate category.
func StorageWindowsHostRows(p diskPressureState) []storageCategoryRow {
	offender := p.HostOffender
	if offender == nil {
		return nil
	}
	scanned := p.HostOffenderScannedAt
	if scanned.IsZero() {
		scanned = time.Now().UTC()
	}
	dump := offender.DumpBytes
	other := offender.Bytes - dump
	if other < 0 {
		other = 0
	}
	rows := make([]storageCategoryRow, 0, 2)
	if dump > 0 {
		rows = append(rows, storageCategoryRow{Category: storageCatWindowsCrashDumps, ApparentBytes: dump, Bytes: dump, Measured: true, Complete: offender.Complete, ScannedAt: scanned})
	}
	if offender.Exists {
		rows = append(rows, storageCategoryRow{Category: storageCatWindowsTemp, ApparentBytes: other, Bytes: other, Measured: true, Complete: offender.Complete, ScannedAt: scanned})
	}
	return rows
}

// StorageProducerSampleFromInventory converts a completed scan into the only
// shape the ledger persists: stable IDs plus byte counts.
func StorageProducerSampleFromInventory(inv storageInventory, bootID string) storageProducerSample {
	sample := storageProducerSample{
		At: inv.ScannedAt, BootID: bootID, Complete: inv.Complete,
		Categories: make(map[string]int64, len(inv.Rows)),
	}
	if sample.At.IsZero() {
		sample.At = time.Now().UTC()
	}
	for _, row := range inv.Rows {
		if row.Category == "" || !row.Measured {
			continue
		}
		sample.Categories[row.Category] = row.ApparentBytes
		if row.AllocatedBytes != nil {
			if sample.Allocated == nil {
				sample.Allocated = make(map[string]int64)
			}
			sample.Allocated[row.Category] = *row.AllocatedBytes
		}
	}
	return sample
}

var (
	storageGrowthBaselineTolerance24h  = 3 * time.Hour
	storageGrowthBaselineTolerance168h = 12 * time.Hour
	storageGrowthMaxSampleAge          = 12 * time.Hour
	storageGrowthMaxGap                = 12 * time.Hour
	storageGrowthMinDeltaBytes         = int64(64 << 20)
)

// StorageGrowthWindow projects category deltas between the newest complete
// sample and a baseline near now-window. It never returns a zero delta for a
// missing/incomplete/rebooting history: the continuity field says why.
func StorageGrowthWindow(samples []storageProducerSample, now time.Time, window time.Duration) storageGrowthWindow {
	label := "24h"
	tolerance := storageGrowthBaselineTolerance24h
	if window >= 7*24*time.Hour {
		label = "168h"
		tolerance = storageGrowthBaselineTolerance168h
	}
	result := storageGrowthWindow{Window: label}
	complete := make([]storageProducerSample, 0, len(samples))
	for _, sample := range samples {
		if sample.At.IsZero() || !sample.Complete {
			continue
		}
		complete = append(complete, sample)
	}
	if len(complete) == 0 {
		result.Continuity = storageContinuityNoBaseline
		result.Reason = "no complete producer samples"
		return result
	}
	sort.Slice(complete, func(i, j int) bool { return complete[i].At.Before(complete[j].At) })
	newest := complete[len(complete)-1]
	latest := newest.At
	result.LatestAt = &latest
	if now.Sub(newest.At) > storageGrowthMaxSampleAge {
		result.Continuity = storageContinuityStale
		result.Reason = "newest producer sample is older than 12h"
		return result
	}
	target := now.Add(-window)
	baseline := -1
	for i, sample := range complete {
		if absDuration(sample.At.Sub(target)) <= tolerance {
			if baseline < 0 || absDuration(sample.At.Sub(target)) < absDuration(complete[baseline].At.Sub(target)) {
				baseline = i
			}
		}
	}
	if baseline < 0 {
		result.Continuity = storageContinuityNoBaseline
		result.Reason = "no complete baseline within tolerance"
		return result
	}
	if baseline == len(complete)-1 && baseline == 0 {
		result.Continuity = storageContinuityNoBaseline
		result.Reason = "baseline and latest are the same sample"
		return result
	}
	if complete[baseline].BootID != "" && newest.BootID != "" && complete[baseline].BootID != newest.BootID {
		result.Continuity = storageContinuityReboot
		result.Reason = "boot changed between baseline and latest"
		return result
	}
	baseAt := complete[baseline].At
	result.BaselineAt = &baseAt
	for i := baseline; i < len(complete)-1; i++ {
		if complete[i+1].At.Sub(complete[i].At) > storageGrowthMaxGap {
			result.Continuity = storageContinuityGap
			result.Reason = "a producer sample gap exceeds 12h"
			return result
		}
	}
	ids := map[string]bool{}
	for _, sample := range complete {
		for id := range sample.Categories {
			ids[id] = true
		}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	categoryGap := false
	categoryStale := false
	for _, id := range ordered {
		baseIdx, latestIdx := -1, -1
		baseDist := time.Duration(1) << 62
		for i, sample := range complete {
			if _, ok := sample.Categories[id]; !ok {
				continue
			}
			latestIdx = i
			if dist := absDuration(sample.At.Sub(target)); dist <= tolerance && dist < baseDist {
				baseDist = dist
				baseIdx = i
			}
		}
		if baseIdx < 0 || latestIdx < 0 || baseIdx == latestIdx {
			continue
		}
		if now.Sub(complete[latestIdx].At) > storageGrowthMaxSampleAge {
			categoryStale = true
			continue
		}
		previous := baseIdx
		gap := false
		for i := baseIdx + 1; i <= latestIdx; i++ {
			if _, present := complete[i].Categories[id]; !present {
				continue
			}
			if complete[i].At.Sub(complete[previous].At) > storageGrowthMaxGap {
				gap = true
				break
			}
			previous = i
		}
		if gap {
			categoryGap = true
			continue
		}
		baseBytes := complete[baseIdx].Categories[id]
		latestBytes := complete[latestIdx].Categories[id]
		delta := latestBytes - baseBytes
		threshold := storageGrowthMinDeltaBytes
		if pct := baseBytes / 100; pct > threshold {
			threshold = pct
		}
		result.Deltas = append(result.Deltas, storageCategoryDelta{
			Category: id, Label: storageCategoryDefinition(id).Label,
			BaselineBytes: baseBytes, LatestBytes: latestBytes, DeltaBytes: delta,
			Noise: abs64(delta) < threshold,
		})
	}
	if len(result.Deltas) == 0 {
		switch {
		case categoryGap:
			result.Continuity = storageContinuityGap
			result.Reason = "no category has continuous samples across the window"
		case categoryStale:
			result.Continuity = storageContinuityStale
			result.Reason = "no category has a fresh latest sample"
		default:
			result.Continuity = storageContinuityNoBaseline
			result.Reason = "no category has a baseline and latest sample"
		}
		return result
	}
	result.Available = true
	result.Continuity = storageContinuityOK
	return result
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// StorageGuestOnlyPendingHostReclaim is true when a guest cleanup with a
// measured positive removal is newer than any verified host reclamation.
func StorageGuestOnlyPendingHostReclaim(ops []storageCleanupOperation) bool {
	var lastGuest, lastHost time.Time
	for _, op := range ops {
		if !op.Executed || op.DryRun {
			continue
		}
		if op.Layer == "guest" && (positiveStorageBytes(op.ApparentBytes) || positiveStorageBytes(op.AllocatedBytes) || positiveStorageBytes(op.GuestFreeDeltaBytes)) {
			// A positive removal can be known without a before/after free-space
			// measurement. Only measured host reclamation clears this state.
			if op.At.After(lastGuest) {
				lastGuest = op.At
			}
		}
		if op.Layer == "host" && op.Verified && op.ReclaimedBytes != nil && *op.ReclaimedBytes > 0 {
			if op.At.After(lastHost) {
				lastHost = op.At
			}
		}
	}
	return !lastGuest.IsZero() && lastGuest.After(lastHost)
}

func positiveStorageBytes(value *int64) bool {
	return value != nil && *value > 0
}

// StorageCleanupMessage returns the exact user-facing explanation for the
// derived guest-only state.
func StorageCleanupMessage(state string) string {
	if state == storageStateGuestOnlyPendingHostReclaim {
		return storageGuestOnlyMessage
	}
	return ""
}

// MergeStorageSkips aggregates skipped/exclusion reasons so task identities and
// raw paths never enter the persisted or rendered summaries.
func MergeStorageSkips(ops []storageCleanupOperation) []storageCleanupSkip {
	counts := map[string]int{}
	for _, op := range ops {
		for _, skip := range op.Skipped {
			if strings.TrimSpace(skip.Reason) == "" {
				continue
			}
			counts[skip.Reason] += max(1, skip.Count)
		}
	}
	out := make([]storageCleanupSkip, 0, len(counts))
	for reason, count := range counts {
		out = append(out, storageCleanupSkip{Reason: reason, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Reason < out[j].Reason
	})
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}

func storageSkipsFromCounts(counts map[string]int) []storageCleanupSkip {
	out := make([]storageCleanupSkip, 0, len(counts))
	for reason, count := range counts {
		if strings.TrimSpace(reason) == "" {
			continue
		}
		out = append(out, storageCleanupSkip{Reason: reason, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Reason < out[j].Reason
	})
	if len(out) > hostStorageOperationReasonCap {
		out = out[:hostStorageOperationReasonCap]
	}
	return out
}
