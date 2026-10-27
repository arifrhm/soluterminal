package main

// Ownership-proven generated storage.
//
// Workflow commands never adopt a pre-existing cache directory. A producer
// gets a random, registry-backed root and GC will only mutate that root while
// its on-disk marker, canonical path and filesystem identity still agree.
// This deliberately makes historic caches inventory-only: size is not proof
// of ownership.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	generatedStorageSchema = 1
	generatedMarkerName    = ".soluterminal-owned.json"
	generatedHostEffect    = "not_measured_no_vhdx_compaction"
	// Resolving a worker environment is a hot path: verification ledger
	// fingerprinting and command startup can both ask for the same roots. Keep
	// Cargo's LRU signal durable without fsyncing the registry once per lookup.
	generatedLastUsedWriteInterval = time.Minute

	generatedGiB = int64(1 << 30)
)

const (
	storageGoBuild    = "go-build"
	storageGoMod      = "go-mod"
	storageNPM        = "npm"
	storageCargo      = "cargo-target"
	storagePlaywright = "playwright-browsers"
	storageTaskTemp   = "task-temp"
	storagePreview    = "static-preview"
)

type generatedDisposition string

const (
	generatedEligible generatedDisposition = "eligible"
	generatedRetained generatedDisposition = "retained"
	generatedDeferred generatedDisposition = "deferred"
	generatedExcluded generatedDisposition = "excluded"
	generatedUnknown  generatedDisposition = "unknown"
	generatedRefused  generatedDisposition = "refused"
)

type generatedStorageMarker struct {
	SchemaVersion int    `json:"schemaVersion"`
	Category      string `json:"category"`
	CanonicalPath string `json:"canonicalPath"`
	OwnershipID   string `json:"ownershipId"`
}

type generatedStorageRecord struct {
	generatedStorageMarker
	RepositoryIdentity string            `json:"repositoryIdentity,omitempty"`
	CreatedAt          time.Time         `json:"createdAt"`
	LastUsedAt         time.Time         `json:"lastUsedAt"`
	Pins               map[string]string `json:"pins,omitempty"`
}

type generatedStorageRegistry struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Roots         []generatedStorageRecord `json:"roots"`
}

type generatedFSIdentity struct {
	Device uint64
	Inode  uint64
	Valid  bool
}

// Producers may tolerate this capability absence; deletion never does.
var errWFGeneratedUnsupported = errors.New("filesystem_identity_unsupported")

func generatedCreationIdentity(path string, identify func(string) (generatedFSIdentity, error)) (generatedFSIdentity, error) {
	identity, err := identify(path)
	if errors.Is(err, errWFGeneratedUnsupported) {
		return generatedFSIdentity{}, nil
	}
	if err != nil {
		return generatedFSIdentity{}, err
	}
	if !identity.Valid {
		return generatedFSIdentity{}, errors.New("invalid filesystem identity")
	}
	return identity, nil
}

type generatedStorageCandidate struct {
	Category                      string               `json:"category"`
	Path                          string               `json:"path,omitempty"`
	OwnershipID                   string               `json:"ownershipId,omitempty"`
	RepositoryIdentity            string               `json:"repositoryIdentity,omitempty"`
	Disposition                   generatedDisposition `json:"disposition"`
	Reason                        string               `json:"reason"`
	EstimatedApparentBytes        int64                `json:"estimatedApparentBytes"`
	EstimatedAllocatedBytes       int64                `json:"estimatedAllocatedBytes"`
	MeasurementComplete           bool                 `json:"measurementComplete"`
	MeasuredRemovedApparentBytes  *int64               `json:"measuredRemovedApparentBytes,omitempty"`
	MeasuredRemovedAllocatedBytes *int64               `json:"measuredRemovedAllocatedBytes,omitempty"`

	identity  generatedFSIdentity
	removeRel []string
	lastUsed  time.Time
}

type generatedStorageCounts struct {
	Eligible int `json:"eligible"`
	Retained int `json:"retained"`
	Deferred int `json:"deferred"`
	Excluded int `json:"excluded"`
	Unknown  int `json:"unknown"`
	Refused  int `json:"refused"`
}

type generatedStorageReport struct {
	DryRun                        bool                        `json:"dryRun"`
	Counts                        generatedStorageCounts      `json:"counts"`
	Entries                       []generatedStorageCandidate `json:"entries"`
	EstimatedApparentBytes        int64                       `json:"estimatedApparentBytes"`
	EstimatedAllocatedBytes       int64                       `json:"estimatedAllocatedBytes"`
	MeasurementComplete           bool                        `json:"measurementComplete"`
	MeasuredRemovedApparentBytes  *int64                      `json:"measuredRemovedApparentBytes,omitempty"`
	MeasuredRemovedAllocatedBytes *int64                      `json:"measuredRemovedAllocatedBytes,omitempty"`
	ObservedGuestFreeBytesDelta   *int64                      `json:"observedGuestFreeBytesDelta,omitempty"`
	HostPhysicalEffect            string                      `json:"hostPhysicalEffect"`
}

type generatedStorageResolution struct {
	Path        string
	Managed     bool
	External    bool
	Refused     bool
	Notice      string
	Category    string
	OwnershipID string
}

var generatedStorageState = struct {
	sync.Mutex
	workflowRoot   string
	cacheBase      string
	notices        map[string]string
	registryPath   string
	registryDigest [sha256.Size]byte
	registryParsed *generatedStorageRegistry
}{notices: map[string]string{}}

// Test seam for byte-sized fixtures. Production leaves it nil.
var generatedBudgetOverride func(category string) (high, low int64, ok bool)

var (
	generatedFairWait       = 2 * time.Minute
	generatedFairBarrierMax = 30 * time.Second
)

// configureGeneratedStorage is called once from daemon boot, before engine
// construction. Generic engines must not rebind the process-wide registry: an
// independent engine's writers could otherwise follow another engine's store
// into its teardown. Worker helpers share this one ownership source without
// exposing the daemon environment; standalone helpers use the cache fallback.
func configureGeneratedStorage(workflowRoot string) {
	generatedStorageState.Lock()
	cleanRoot := filepath.Clean(workflowRoot)
	if generatedStorageState.workflowRoot != cleanRoot {
		generatedStorageState.notices = map[string]string{}
	}
	generatedStorageState.workflowRoot = cleanRoot
	generatedStorageState.cacheBase, _ = generatedCacheBase()
	// Boot performs a read-only registry load before any producer can allocate
	// storage. Missing is a valid empty registry; corruption remains visible and
	// unknown until an operator repairs it, never overwritten opportunistically.
	if _, err := generatedReadRegistryLocked(); err != nil {
		generatedNotice("registry", err.Error())
	}
	generatedStorageState.Unlock()
}

func generatedStorageNotices() []string {
	generatedStorageState.Lock()
	defer generatedStorageState.Unlock()
	return generatedStorageNoticesLocked()
}

func generatedStorageNoticesLocked() []string {
	out := make([]string, 0, len(generatedStorageState.notices))
	for _, notice := range generatedStorageState.notices {
		out = append(out, notice)
	}
	sort.Strings(out)
	return out
}

// Inventory must honor cancellation even when a producer is synchronizing the
// registry. Producers retain their existing lock/write contract.
func generatedLockContext(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if generatedStorageState.TryLock() {
			return nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func generatedNotice(key, message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	if generatedStorageState.notices[key] != message {
		log.Printf("workflow generated storage: %s", message)
	}
	generatedStorageState.notices[key] = message
}

func generatedRegistryPathLocked() string {
	currentBase, _ := generatedCacheBase()
	if generatedStorageState.workflowRoot == "" || (generatedStorageState.cacheBase != "" && currentBase != "" && currentBase != generatedStorageState.cacheBase) {
		// Direct library/script use has no engine store to configure. Keep its
		// registry inside the managed cache namespace; production always sets
		// workflowRoot and therefore uses <data>/workflow as specified.
		if currentBase == "" {
			return ""
		}
		return filepath.Join(currentBase, "registry", "registry.json")
	}
	return filepath.Join(generatedStorageState.workflowRoot, "generated-storage", "v1", "registry.json")
}

func generatedCacheBase() (string, error) {
	root := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME"))
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", errors.New("user cache root unavailable")
		}
		root = filepath.Join(home, ".cache")
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("cache root is relative")
	}
	root = filepath.Clean(root)
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve cache root: %w", err)
	}
	if generatedUnsafeRoot(root) {
		return "", fmt.Errorf("cache root is unsafe")
	}
	return filepath.Join(root, "soluterminal", "managed", "v1"), nil
}

func generatedUnsafeRoot(path string) bool {
	if !filepath.IsAbs(path) {
		return true
	}
	clean := filepath.Clean(path)
	if clean == string(filepath.Separator) || clean == "." || clean == "" {
		return true
	}
	if home, err := os.UserHomeDir(); err == nil {
		if absHome, err := filepath.Abs(home); err == nil && clean == filepath.Clean(absHome) {
			return true
		}
	}
	return false
}

func generatedReadRegistryLocked() (generatedStorageRegistry, error) {
	registry := generatedStorageRegistry{SchemaVersion: generatedStorageSchema, Roots: []generatedStorageRecord{}}
	path := generatedRegistryPathLocked()
	if path == "" {
		return registry, errors.New("workflow generated-storage registry is not configured")
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return registry, nil
	}
	if err != nil {
		return registry, err
	}
	// Read the current durable bytes on EVERY lookup, including hot lookups.
	// Only JSON decoding is memoized: mtime/size are not ownership evidence and
	// can be preserved by an external edit. Returned snapshots are independent
	// so caller mutations cannot certify an unpersisted registry change.
	digest := sha256.Sum256(raw)
	if generatedStorageState.registryParsed != nil &&
		generatedStorageState.registryPath == path &&
		generatedStorageState.registryDigest == digest {
		return generatedCloneRegistry(*generatedStorageState.registryParsed), nil
	}
	if err := json.Unmarshal(raw, &registry); err != nil {
		return registry, fmt.Errorf("parse generated-storage registry: %w", err)
	}
	if registry.SchemaVersion != generatedStorageSchema {
		return registry, fmt.Errorf("unsupported generated-storage registry schema %d", registry.SchemaVersion)
	}
	if registry.Roots == nil {
		registry.Roots = []generatedStorageRecord{}
	}
	generatedStorageState.registryPath = path
	generatedStorageState.registryDigest = digest
	generatedStorageState.registryParsed = &registry
	return generatedCloneRegistry(registry), nil
}

func generatedCloneRegistry(registry generatedStorageRegistry) generatedStorageRegistry {
	registry.Roots = append([]generatedStorageRecord{}, registry.Roots...)
	for i := range registry.Roots {
		if registry.Roots[i].Pins == nil {
			continue
		}
		pins := make(map[string]string, len(registry.Roots[i].Pins))
		for key, revision := range registry.Roots[i].Pins {
			pins[key] = revision
		}
		registry.Roots[i].Pins = pins
	}
	return registry
}

func generatedWriteRegistryLocked(registry generatedStorageRegistry) error {
	path := generatedRegistryPathLocked()
	if path == "" {
		return errors.New("workflow generated-storage registry is not configured")
	}
	registry.SchemaVersion = generatedStorageSchema
	sort.Slice(registry.Roots, func(i, j int) bool { return registry.Roots[i].CanonicalPath < registry.Roots[j].CanonicalPath })
	raw, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeFileAtomicSynced(path, append(raw, '\n'), 0o600)
}

func generatedRandomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func generatedSafeName(category, repositoryIdentity, ownershipID string) string {
	name := category
	if repositoryIdentity != "" {
		name += "-" + shortStableHash(repositoryIdentity)
	}
	return name + "-" + ownershipID
}

func shortStableHash(value string) string {
	// The full repository identity remains in the registry and this complete
	// SHA-256 label avoids truncation collisions in the human-visible path.
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func generatedResolve(category, repositoryIdentity string, create bool) (generatedStorageResolution, error) {
	generatedStorageState.Lock()
	defer generatedStorageState.Unlock()
	registry, err := generatedReadRegistryLocked()
	if err != nil {
		generatedNotice(category, err.Error())
		return generatedStorageResolution{Category: category, Refused: true, Notice: err.Error()}, err
	}
	for i := range registry.Roots {
		record := &registry.Roots[i]
		if record.Category != category || record.RepositoryIdentity != repositoryIdentity {
			continue
		}
		if err := generatedValidateRecord(*record); err != nil {
			generatedNotice(category+":"+record.OwnershipID, err.Error())
			return generatedStorageResolution{Path: record.CanonicalPath, Category: category, OwnershipID: record.OwnershipID, Refused: true, Notice: err.Error()}, err
		}
		now := time.Now().UTC()
		if create && record.Category == storageCargo && (record.LastUsedAt.IsZero() || now.Before(record.LastUsedAt) || now.Sub(record.LastUsedAt) >= generatedLastUsedWriteInterval) {
			record.LastUsedAt = now
			if err := generatedWriteRegistryLocked(registry); err != nil {
				return generatedStorageResolution{}, err
			}
		}
		return generatedStorageResolution{Path: record.CanonicalPath, Category: category, OwnershipID: record.OwnershipID, Managed: true}, nil
	}
	if !create {
		return generatedStorageResolution{Category: category}, os.ErrNotExist
	}
	base, err := generatedCacheBase()
	if err != nil {
		return generatedStorageResolution{Category: category, Refused: true, Notice: err.Error()}, err
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return generatedStorageResolution{}, err
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil || generatedUnsafeRoot(base) {
		return generatedStorageResolution{}, errors.New("managed cache base identity is unsafe")
	}
	ownershipID, err := generatedRandomID()
	if err != nil {
		return generatedStorageResolution{}, err
	}
	path := filepath.Join(base, generatedSafeName(category, repositoryIdentity, ownershipID))
	if _, err := os.Lstat(path); err == nil || !os.IsNotExist(err) {
		return generatedStorageResolution{}, fmt.Errorf("refusing to adopt existing generated-storage root %s", path)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return generatedStorageResolution{}, err
	}
	allocationIdentity, _ := generatedIdentity(path)
	canonical, err := filepath.Abs(path)
	if err != nil {
		_ = os.Remove(path)
		return generatedStorageResolution{}, err
	}
	marker := generatedStorageMarker{SchemaVersion: generatedStorageSchema, Category: category, CanonicalPath: filepath.Clean(canonical), OwnershipID: ownershipID}
	raw, _ := json.MarshalIndent(marker, "", "  ")
	if err := writeFileAtomicSynced(filepath.Join(canonical, generatedMarkerName), append(raw, '\n'), 0o600); err != nil {
		if allocationIdentity.Valid {
			_ = generatedRemoveTree(context.Background(), canonical, allocationIdentity)
		}
		return generatedStorageResolution{}, err
	}
	now := time.Now().UTC()
	registry.Roots = append(registry.Roots, generatedStorageRecord{generatedStorageMarker: marker, RepositoryIdentity: repositoryIdentity, CreatedAt: now, LastUsedAt: now})
	if err := generatedWriteRegistryLocked(registry); err != nil {
		// A marked but unregistered directory is deliberately left unknown. It
		// is safer than deleting a root whose durable registration failed.
		return generatedStorageResolution{}, err
	}
	return generatedStorageResolution{Path: canonical, Category: category, OwnershipID: ownershipID, Managed: true}, nil
}

func generatedValidateRecord(record generatedStorageRecord) error {
	if record.SchemaVersion != generatedStorageSchema || record.OwnershipID == "" || record.Category == "" {
		return errors.New("registry_record_invalid")
	}
	if generatedUnsafeRoot(record.CanonicalPath) {
		return errors.New("unsafe_root")
	}
	base, err := generatedCacheBase()
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(base, record.CanonicalPath)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("path_outside_managed_root")
	}
	info, err := os.Lstat(record.CanonicalPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("root_not_real_directory")
	}
	raw, err := generatedReadMarker(record.CanonicalPath, generatedMarkerName)
	if err != nil {
		return errors.New("ownership_marker_missing")
	}
	var marker generatedStorageMarker
	if err := json.Unmarshal(raw, &marker); err != nil {
		return errors.New("ownership_marker_corrupt")
	}
	if marker != record.generatedStorageMarker {
		return errors.New("ownership_marker_mismatch")
	}
	return nil
}

// Capture the registry-authorized value, not whatever marker happens to be on
// disk when deletion starts. Platform helpers run this comparison on the same
// pinned root before and after their traversal preflight.
func generatedRegistryMarkerValidator(expected generatedStorageMarker) func([]byte) error {
	return func(raw []byte) error {
		var current generatedStorageMarker
		if json.Unmarshal(raw, &current) != nil || current != expected {
			return errors.New("ownership_marker_mismatch")
		}
		return nil
	}
}

func generatedResolveOverride(category, envName string) generatedStorageResolution {
	raw := strings.TrimSpace(os.Getenv(envName))
	if raw != "" && raw != "off" {
		if filepath.IsAbs(raw) && !generatedUnsafeRoot(raw) {
			return generatedStorageResolution{Path: filepath.Clean(raw), Category: category, External: true, Notice: envName + " is an unmanaged user override"}
		}
		notice := envName + " override refused: path must be absolute and cannot be root/home; using an owned workflow cache"
		generatedStorageState.Lock()
		generatedNotice(envName, notice)
		generatedStorageState.Unlock()
		resolved, err := generatedResolve(category, "", true)
		if err != nil {
			return generatedStorageResolution{Category: category, Refused: true, Notice: notice + ": " + err.Error()}
		}
		return resolved
	}
	resolved, err := generatedResolve(category, "", true)
	if err != nil {
		return generatedStorageResolution{Category: category, Refused: true, Notice: err.Error()}
	}
	return resolved
}

// Resolve the existing portion of a path without creating anything. Overrides
// can name a not-yet-created child of a symlinked cache directory. A dangling
// link, permission error or otherwise ambiguous resolution is not disjointness
// proof and must conservatively prevent cleanup.
func generatedCanonicalProtectionPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		var err error
		path, err = filepath.Abs(path)
		if err != nil {
			return "", err
		}
	}
	probe := path
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			return filepath.Join(append([]string{resolved}, suffix...)...), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		// Cleaning an unresolved symlink/../ suffix can change its meaning.
		for _, part := range strings.Split(filepath.ToSlash(path), "/") {
			if part == ".." {
				return "", err
			}
		}
		if _, statErr := os.Lstat(probe); !os.IsNotExist(statErr) {
			return "", err // includes an existing but dangling symlink
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", err
		}
		suffix = append([]string{filepath.Base(probe)}, suffix...)
		probe = parent
	}
}

func generatedProtectedOverlap(path, protected string) bool {
	return generatedProtectedOverlapWithProbe(path, protected, generatedProbeMount, generatedDescendantMounts)
}

func generatedOverrideExcluded(path string) bool {
	// This is the explicit workflow override; ambient GOCACHE/GOMODCACHE are
	// deliberately not worker-routing inputs (see sharedGoCacheDir).
	raw := strings.TrimSpace(os.Getenv("SOLU_WORKFLOW_GOCACHE_DIR"))
	return raw != "" && raw != "off" && filepath.IsAbs(raw) && generatedProtectedOverlap(path, raw)
}

func generatedBudget(category string) (high, low int64) {
	if generatedBudgetOverride != nil {
		if high, low, ok := generatedBudgetOverride(category); ok {
			return high, low
		}
	}
	switch category {
	case storageGoBuild:
		high = int64(goCacheCapMB()) << 20
		// Preserve the landed Go-cache policy: crossing the high-water mark
		// clears the disposable build cache completely. Other categories use
		// explicit non-zero low-water marks.
		low = 0
	case storageGoMod:
		high, low = 16*generatedGiB, 12*generatedGiB
	case storageNPM:
		high, low = 8*generatedGiB, 4*generatedGiB
	case storagePlaywright:
		high, low = 8*generatedGiB, 4*generatedGiB
	case storageCargo:
		high, low = 32*generatedGiB, 24*generatedGiB
	}
	return high, low
}

func generatedStoragePlan(ctx context.Context, dryRun bool) generatedStorageReport {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	report := generatedStorageReport{DryRun: dryRun, Entries: []generatedStorageCandidate{}, MeasurementComplete: true, HostPhysicalEffect: generatedHostEffect}
	var registry generatedStorageRegistry
	err := generatedLockContext(ctx)
	if err == nil {
		registry, err = generatedReadRegistryLocked()
		generatedStorageState.Unlock()
	}
	if err != nil {
		report.Entries = append(report.Entries, generatedStorageCandidate{Category: "registry", Disposition: generatedUnknown, Reason: "registry_unreadable", MeasurementComplete: false})
		report.MeasurementComplete = false
		generatedSummarizeReport(&report)
		return report
	}
	for _, record := range registry.Roots {
		candidate := generatedStorageCandidate{Category: record.Category, Path: record.CanonicalPath, OwnershipID: record.OwnershipID, RepositoryIdentity: record.RepositoryIdentity, Disposition: generatedRetained, Reason: "below_high_water", MeasurementComplete: true, lastUsed: record.LastUsedAt}
		if ctx.Err() != nil {
			candidate.Disposition, candidate.Reason, candidate.MeasurementComplete = generatedDeferred, "measurement_incomplete", false
			report.Entries = append(report.Entries, candidate)
			continue
		}
		if generatedOverrideExcluded(record.CanonicalPath) {
			candidate.Disposition, candidate.Reason, candidate.MeasurementComplete = generatedExcluded, "user_override_unmanaged", false
			report.Entries = append(report.Entries, candidate)
			continue
		}
		if err := generatedValidateRecord(record); err != nil {
			candidate.Disposition, candidate.Reason, candidate.MeasurementComplete = generatedRefused, generatedReason(err), false
			report.Entries = append(report.Entries, candidate)
			continue
		}
		identity, err := generatedIdentity(record.CanonicalPath)
		if err != nil || !identity.Valid {
			candidate.Disposition, candidate.Reason, candidate.MeasurementComplete = generatedRefused, "filesystem_identity_unsupported", false
			report.Entries = append(report.Entries, candidate)
			continue
		}
		candidate.identity = identity
		rootCtx, rootCancel := context.WithTimeout(ctx, 3*time.Second)
		apparent, allocated, complete, reason := generatedMeasure(rootCtx, record.CanonicalPath, identity)
		candidate.EstimatedApparentBytes, candidate.EstimatedAllocatedBytes, candidate.MeasurementComplete = apparent, allocated, complete
		if !complete {
			candidate.Disposition, candidate.Reason = generatedRefused, reason
		} else if high, _ := generatedBudget(record.Category); high > 0 && apparent > high {
			hasPayload, payloadErr := generatedRootHasPayload(record.CanonicalPath)
			if payloadErr != nil {
				candidate.Disposition, candidate.Reason, candidate.MeasurementComplete = generatedRefused, "inspection_error", false
			} else if hasPayload {
				candidate.Disposition, candidate.Reason = generatedEligible, "above_high_water"
			}
		}
		if candidate.Disposition == generatedEligible && record.Category == storagePlaywright {
			generatedPlanPlaywright(rootCtx, &candidate, record)
		}
		rootCancel()
		report.Entries = append(report.Entries, candidate)
	}
	generatedApplyCargoAggregate(&report)
	generatedAddUnknownInventory(ctx, &report)
	generatedSummarizeReport(&report)
	return report
}

func generatedRootHasPayload(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Name() != generatedMarkerName {
			return true, nil
		}
	}
	return false, nil
}

func generatedPlanPlaywright(ctx context.Context, candidate *generatedStorageCandidate, record generatedStorageRecord) {
	if candidate == nil {
		return
	}
	pinned := map[string]bool{}
	for name, revision := range record.Pins {
		if revision != "" {
			pinned[strings.ReplaceAll(name, "-", "_")+"-"+revision] = true
		}
	}
	entries, err := os.ReadDir(candidate.Path)
	if err != nil {
		candidate.Disposition, candidate.Reason = generatedRefused, "inspection_error"
		return
	}
	type child struct {
		name string
		size int64
		used time.Time
	}
	var removable []child
	for _, entry := range entries {
		if !entry.IsDir() || pinned[entry.Name()] {
			continue
		}
		path := filepath.Join(candidate.Path, entry.Name())
		identity, identityErr := generatedIdentity(path)
		if identityErr != nil || !identity.Valid || identity.Device != candidate.identity.Device {
			candidate.Disposition, candidate.Reason = generatedRefused, "mount_crossing"
			candidate.removeRel = nil
			return
		}
		size, _, complete, _ := generatedMeasure(ctx, path, identity)
		if !complete {
			candidate.Disposition, candidate.Reason = generatedRefused, "measurement_incomplete"
			candidate.removeRel = nil
			return
		}
		info, _ := entry.Info()
		used := time.Time{}
		if info != nil {
			used = info.ModTime()
		}
		removable = append(removable, child{name: entry.Name(), size: size, used: used})
	}
	sort.Slice(removable, func(i, j int) bool {
		if removable[i].used.Equal(removable[j].used) {
			return removable[i].name < removable[j].name
		}
		return removable[i].used.Before(removable[j].used)
	})
	_, low := generatedBudget(storagePlaywright)
	remaining := candidate.EstimatedApparentBytes
	for _, item := range removable {
		if remaining <= low {
			break
		}
		candidate.removeRel = append(candidate.removeRel, item.name)
		remaining -= item.size
	}
	if len(candidate.removeRel) == 0 {
		candidate.Disposition, candidate.Reason = generatedDeferred, "required_browser_versions_pinned"
	} else {
		candidate.Reason = "unpinned_browser_versions_lru"
	}
}

func generatedReason(err error) string {
	if err == nil {
		return "inspection_error"
	}
	text := strings.ToLower(err.Error())
	for _, stable := range []string{"unsafe_root", "path_outside_managed_root", "root_not_real_directory", "ownership_marker_missing", "ownership_marker_corrupt", "ownership_marker_mismatch", "registry_record_invalid", "symlink_detected", "mount_crossing", "changed_identity", "filesystem_identity_unsupported"} {
		if strings.Contains(text, stable) {
			return stable
		}
	}
	return "inspection_error"
}

func generatedApplyCargoAggregate(report *generatedStorageReport) {
	var cargo []*generatedStorageCandidate
	var total int64
	for i := range report.Entries {
		candidate := &report.Entries[i]
		if candidate.Category == storageCargo && candidate.MeasurementComplete && (candidate.Disposition == generatedRetained || candidate.Disposition == generatedEligible) {
			cargo = append(cargo, candidate)
			total += candidate.EstimatedApparentBytes
		}
	}
	aggregateHigh, aggregateLow := int64(48*generatedGiB), int64(36*generatedGiB)
	if generatedBudgetOverride != nil {
		if high, low, ok := generatedBudgetOverride("cargo-aggregate"); ok {
			aggregateHigh, aggregateLow = high, low
		}
	}
	if total <= aggregateHigh {
		return
	}
	sort.Slice(cargo, func(i, j int) bool {
		if cargo[i].lastUsed.Equal(cargo[j].lastUsed) {
			return cargo[i].Path < cargo[j].Path
		}
		return cargo[i].lastUsed.Before(cargo[j].lastUsed)
	})
	for _, candidate := range cargo {
		if total <= aggregateLow {
			break
		}
		candidate.Disposition, candidate.Reason = generatedEligible, "cargo_aggregate_above_high_water_lru"
		total -= candidate.EstimatedApparentBytes
	}
}

func generatedAddUnknownInventory(ctx context.Context, report *generatedStorageReport) {
	root := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME"))
	if root == "" {
		if home, err := os.UserHomeDir(); err == nil {
			root = filepath.Join(home, ".cache")
		}
	}
	if root == "" || !filepath.IsAbs(root) {
		return
	}
	legacy := []struct{ category, path string }{
		{storageGoBuild, filepath.Join(root, "soluterminal", "go-build")},
		{storageGoMod, filepath.Join(root, "soluterminal", "go-mod")},
		{storageNPM, filepath.Join(root, "soluterminal", "npm-cache")},
		{storageCargo, filepath.Join(root, "soluterminal", "cargo-target")},
	}
	for _, item := range legacy {
		if generatedOverrideExcluded(item.path) {
			report.Entries = append(report.Entries, generatedStorageCandidate{Category: item.category, Path: item.path, Disposition: generatedExcluded, Reason: "user_override_unmanaged"})
			continue
		}
		if info, err := os.Lstat(item.path); err == nil {
			candidate := generatedStorageCandidate{Category: item.category, Path: item.path, Disposition: generatedUnknown, Reason: "legacy_unregistered_report_only", MeasurementComplete: false}
			if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				if identity, identityErr := generatedIdentity(item.path); identityErr == nil && identity.Valid {
					scanCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
					candidate.EstimatedApparentBytes, candidate.EstimatedAllocatedBytes, candidate.MeasurementComplete, _ = generatedMeasure(scanCtx, item.path, identity)
					cancel()
				}
			}
			report.Entries = append(report.Entries, candidate)
		}
	}
	for _, override := range []struct{ env, category string }{{"SOLU_WORKFLOW_GOCACHE_DIR", storageGoBuild}} {
		if raw := strings.TrimSpace(os.Getenv(override.env)); raw != "" && raw != "off" && filepath.IsAbs(raw) {
			alreadyReported := false
			for _, entry := range report.Entries {
				if entry.Path == filepath.Clean(raw) && entry.Disposition == generatedExcluded {
					alreadyReported = true
					break
				}
			}
			if !alreadyReported {
				report.Entries = append(report.Entries, generatedStorageCandidate{Category: override.category, Path: filepath.Clean(raw), Disposition: generatedExcluded, Reason: "user_override_unmanaged", MeasurementComplete: false})
			}
		}
	}
	if err := generatedLockContext(ctx); err != nil {
		report.Entries = append(report.Entries, generatedStorageCandidate{Category: "configuration", Disposition: generatedDeferred, Reason: "measurement_incomplete"})
		return
	}
	notices := generatedStorageNoticesLocked()
	generatedStorageState.Unlock()
	for _, notice := range notices {
		report.Entries = append(report.Entries, generatedStorageCandidate{Category: "configuration", Disposition: generatedRefused, Reason: notice, MeasurementComplete: false})
	}
}

func generatedSummarizeReport(report *generatedStorageReport) {
	report.Counts = generatedStorageCounts{}
	report.EstimatedApparentBytes, report.EstimatedAllocatedBytes = 0, 0
	report.MeasurementComplete = true
	for _, entry := range report.Entries {
		switch entry.Disposition {
		case generatedEligible:
			report.Counts.Eligible++
			report.EstimatedApparentBytes += entry.EstimatedApparentBytes
			report.EstimatedAllocatedBytes += entry.EstimatedAllocatedBytes
		case generatedRetained:
			report.Counts.Retained++
		case generatedDeferred:
			report.Counts.Deferred++
			report.EstimatedApparentBytes += entry.EstimatedApparentBytes
			report.EstimatedAllocatedBytes += entry.EstimatedAllocatedBytes
		case generatedExcluded:
			report.Counts.Excluded++
		case generatedUnknown:
			report.Counts.Unknown++
		case generatedRefused:
			report.Counts.Refused++
		}
		if entry.Disposition != generatedEligible && strings.HasPrefix(entry.Reason, "trimmed_") {
			report.EstimatedApparentBytes += entry.EstimatedApparentBytes
			report.EstimatedAllocatedBytes += entry.EstimatedAllocatedBytes
		}
		if !entry.MeasurementComplete {
			report.MeasurementComplete = false
		}
	}
}

func generatedRegistryOwnedRecord(registry generatedStorageRegistry, category, path string, expected generatedFSIdentity) (generatedStorageRecord, bool) {
	for _, record := range registry.Roots {
		if record.Category == category && record.CanonicalPath == path {
			if err := generatedValidateRecord(record); err != nil {
				return generatedStorageRecord{}, false
			}
			identity, err := generatedIdentity(record.CanonicalPath)
			return record, err == nil && identity == expected
		}
	}
	return generatedStorageRecord{}, false
}

func generatedPathOwned(category, path string) bool {
	if generatedOverrideExcluded(path) {
		return false
	}
	generatedStorageState.Lock()
	defer generatedStorageState.Unlock()
	registry, err := generatedReadRegistryLocked()
	if err != nil {
		return false
	}
	clean := filepath.Clean(path)
	for _, record := range registry.Roots {
		if record.Category == category && record.CanonicalPath == clean && generatedValidateRecord(record) == nil {
			return true
		}
	}
	return false
}

func generatedProbePath(entries []generatedStorageCandidate) string {
	for _, entry := range entries {
		if entry.Disposition == generatedEligible && entry.Path != "" {
			return entry.Path
		}
	}
	return "."
}

func generatedStoragePinPlaywright(revisions map[string]string) error {
	if len(revisions) == 0 {
		return nil
	}
	generatedStorageState.Lock()
	defer generatedStorageState.Unlock()
	registry, err := generatedReadRegistryLocked()
	if err != nil {
		return err
	}
	changed := false
	found := false
	for i := range registry.Roots {
		if registry.Roots[i].Category != storagePlaywright {
			continue
		}
		found = true
		if registry.Roots[i].Pins == nil {
			registry.Roots[i].Pins = map[string]string{}
		}
		for name, revision := range revisions {
			if registry.Roots[i].Pins[name] != revision {
				registry.Roots[i].Pins[name] = revision
				changed = true
			}
		}
	}
	if !found {
		return errors.New("managed Playwright browser root is not registered")
	}
	if changed {
		return generatedWriteRegistryLocked(registry)
	}
	return nil
}
