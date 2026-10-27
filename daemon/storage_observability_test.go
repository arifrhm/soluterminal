package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeStorageFile(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if size > 1 {
		if err := os.Truncate(path, size); err != nil {
			t.Fatal(err)
		}
	}
}

func storageRow(t *testing.T, inv storageInventory, category string) storageCategoryRow {
	t.Helper()
	for _, row := range inv.Rows {
		if row.Category == category {
			return row
		}
	}
	t.Fatalf("category %s missing from %+v", category, inv.Rows)
	return storageCategoryRow{}
}

func TestStorageInventoryApparentVsAllocatedAndHardlinks(t *testing.T) {
	dir := t.TempDir()
	worktrees := filepath.Join(dir, "workflow", "worktrees")
	sparse := filepath.Join(worktrees, "t1", "sparse.bin")
	writeStorageFile(t, sparse, 1<<20)
	regular := filepath.Join(worktrees, "t1", "regular.bin")
	writeStorageFile(t, regular, 4096)
	if err := os.Link(regular, filepath.Join(worktrees, "t1", "hardlink.bin")); err != nil {
		t.Skipf("hardlinks unsupported: %v", err)
	}

	inv := ScanStorageInventory(context.Background(), storageScanRequest{DataDir: dir})
	row := storageRow(t, inv, storageCatWorkflowWorktrees)
	if !row.Measured || !row.Complete {
		t.Fatalf("worktree row = %+v", row)
	}
	// The hardlink must be counted once: sparse 1 MiB + regular 4 KiB.
	if want := int64((1 << 20) + 4096); row.ApparentBytes != want {
		t.Fatalf("apparent = %d, want %d (hardlink double-counted?)", row.ApparentBytes, want)
	}
	if row.AllocatedBytes == nil {
		t.Fatalf("allocated bytes unknown on Linux: %+v", row)
	}
	if *row.AllocatedBytes >= (1<<20)+4096 {
		t.Fatalf("sparse file allocated %d, want less than apparent", *row.AllocatedBytes)
	}
}

func TestStorageInventoryNestedRootsAreNotDoubleCounted(t *testing.T) {
	dir := t.TempDir()
	writeStorageFile(t, filepath.Join(dir, "top.bin"), 1000)
	writeStorageFile(t, filepath.Join(dir, "workflow", "worktrees", "w", "nested.bin"), 2000)
	inv := ScanStorageInventory(context.Background(), storageScanRequest{DataDir: dir})
	other := storageRow(t, inv, storageCatOtherSoluData)
	worktrees := storageRow(t, inv, storageCatWorkflowWorktrees)
	if other.ApparentBytes != 1000 {
		t.Fatalf("other apparent = %d, want 1000 (nested worktrees double-counted)", other.ApparentBytes)
	}
	if worktrees.ApparentBytes != 2000 {
		t.Fatalf("worktrees apparent = %d, want 2000", worktrees.ApparentBytes)
	}
}

func TestStorageInventoryMountBoundaryIsSkipped(t *testing.T) {
	dir := t.TempDir()
	writeStorageFile(t, filepath.Join(dir, "workflow", "worktrees", "cross", "mounted.bin"), 5000)
	io := newStorageScanIO()
	io.device = func(path string) (uint64, bool) {
		if filepath.Base(path) == "cross" {
			return 2, true
		}
		return 1, true
	}
	inv := ScanStorageInventory(context.Background(), storageScanRequest{DataDir: dir, IO: io})
	row := storageRow(t, inv, storageCatWorkflowWorktrees)
	if row.ApparentBytes != 0 || row.Complete {
		t.Fatalf("mount crossing was not skipped: %+v", row)
	}
	if len(row.Reasons) == 0 || row.Reasons[0] != "mount_boundary" {
		t.Fatalf("mount boundary reason = %v", row.Reasons)
	}
}

func TestStorageInventoryEntryTargetAndTimeoutLimits(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		writeStorageFile(t, filepath.Join(dir, "workflow", "worktrees", "w", "f"+itoa64(int64(i))), 1)
	}
	inv := ScanStorageInventory(context.Background(), storageScanRequest{
		DataDir: dir,
		Limits:  storageScanLimits{PerCategoryEntries: 2},
	})
	row := storageRow(t, inv, storageCatWorkflowWorktrees)
	if row.Complete || !storageContains(row.Reasons, "entry_limit") {
		t.Fatalf("entry limit reason = %+v", row)
	}

	limited := ScanStorageInventory(context.Background(), storageScanRequest{DataDir: dir, Limits: storageScanLimits{MaxTargets: 1}})
	if limited.Targets != 1 || limited.Complete || !storageContains(limited.Reasons, "target_limit") {
		t.Fatalf("target limit must expose incomplete inventory: %+v", limited)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	timed := ScanStorageInventory(canceledCtx, storageScanRequest{DataDir: dir})
	if timed.Complete || !storageContains(timed.Reasons, "timeout") {
		t.Fatalf("timeout result = %+v", timed)
	}
}

func TestStorageCategoryPolicyIsReadOnlyForExternalAndProtected(t *testing.T) {
	managed := map[string]bool{
		storageCatWorkflowWorktrees: true,
		storageCatRetainedHistory:   true,
		storageCatTaskTemp:          true,
		storageCatManagedGoBuild:    true,
		storageCatManagedGoMod:      true,
		storageCatManagedCargo:      true,
		storageCatManagedNpm:        true,
		storageCatManagedPlaywright: true,
	}
	for _, def := range storageCategoryDefinitions {
		if managed[def.ID] {
			if def.Cleanup == storageCleanupNone {
				t.Fatalf("managed category %s has no cleanup owner", def.ID)
			}
			continue
		}
		if def.Cleanup != storageCleanupNone {
			t.Fatalf("external category %s acquired cleanup %q", def.ID, def.Cleanup)
		}
		if def.Protection == storageProtectionManaged {
			t.Fatalf("external category %s is marked managed", def.ID)
		}
	}
	if storageCategoryDefinition(storageCatProtectedRecovery).Protection != storageProtectionProtected {
		t.Fatal("protected recovery data is not protected")
	}
}

func TestStorageInventorySeparatesOwnedAndLegacyCachesWithoutMutation(t *testing.T) {
	cacheRoot, workflowRoot := generatedTestState(t)
	t.Setenv("SOLU_WORKFLOW_GOCACHE_DIR", "")
	owned, err := generatedResolve(storageGoBuild, "", true)
	if err != nil {
		t.Fatal(err)
	}
	writeStorageFile(t, filepath.Join(owned.Path, "payload"), 1024)
	legacy := filepath.Join(cacheRoot, "soluterminal", "go-build", "user-payload")
	writeStorageFile(t, legacy, 512)
	registry := filepath.Join(workflowRoot, "generated-storage", "v1", "registry.json")
	before, err := os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(registry)
	if err != nil {
		t.Fatal(err)
	}
	inv := ScanStorageInventory(context.Background(), storageScanRequest{DataDir: filepath.Dir(workflowRoot)})
	managed := storageRow(t, inv, storageCatManagedGoBuild)
	if !managed.Measured || managed.ApparentBytes < 1024 || managed.Cleanup != storageCleanupGenerated || managed.Protection != storageProtectionManaged {
		t.Fatalf("owned cache misclassified: %+v", managed)
	}
	old := storageRow(t, inv, storageCatSharedGoBuild)
	if old.ApparentBytes != 512 || old.Protection != storageProtectionReadOnly || old.Cleanup != storageCleanupNone {
		t.Fatalf("legacy cache adopted: %+v", old)
	}
	after, err := os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(registry)
	if err != nil || !bytes.Equal(before, after) || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatalf("inventory rewrote registry: %v", err)
	}
	// Losing the marker must remove the managed label, not adopt the path.
	if err := os.WriteFile(filepath.Join(owned.Path, generatedMarkerName), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	inv = ScanStorageInventory(context.Background(), storageScanRequest{DataDir: filepath.Dir(workflowRoot)})
	if storageRow(t, inv, storageCatManagedGoBuild).Measured || inv.Complete || !storageContains(inv.Reasons, "generated_ownership_unverified") {
		t.Fatalf("unverified ownership accepted: %+v", inv)
	}
	unknown := storageRow(t, inv, storageCatUnownedSoluCache)
	if unknown.ApparentBytes < 1024 || unknown.Cleanup != storageCleanupNone {
		t.Fatalf("unverified cache disappeared or gained cleanup: %+v", unknown)
	}
	if info, err := os.Stat(legacy); err != nil || info.Size() != 512 {
		t.Fatalf("legacy payload changed: %v", err)
	}
}

func TestStorageInventoryDoesNotAllocateMissingRegistry(t *testing.T) {
	_, workflowRoot := generatedTestState(t)
	if _, reasons := storageGeneratedCacheTargets(filepath.Dir(workflowRoot), 10); len(reasons) != 0 {
		t.Fatal(reasons)
	}
	if _, err := os.Stat(workflowRoot); !os.IsNotExist(err) {
		t.Fatalf("inventory allocated registry tree: %v", err)
	}
}

func TestStorageInventoryDoesNotWalkExternalGoOverride(t *testing.T) {
	_, workflowRoot := generatedTestState(t)
	external := t.TempDir()
	writeStorageFile(t, filepath.Join(external, "private-payload"), 1024)
	t.Setenv("SOLU_WORKFLOW_GOCACHE_DIR", external)
	io := newStorageScanIO()
	io.lstat = func(path string) (os.FileInfo, error) {
		if path == external || strings.HasPrefix(path, external+string(filepath.Separator)) {
			t.Fatalf("inventory probed external override %s", path)
		}
		return os.Lstat(path)
	}
	_ = ScanStorageInventory(context.Background(), storageScanRequest{DataDir: filepath.Dir(workflowRoot), IO: io})
}

func TestStorageProducerSampleCarriesNoPaths(t *testing.T) {
	dir := t.TempDir()
	writeStorageFile(t, filepath.Join(dir, "secret-repo-name", "top.bin"), 10)
	inv := ScanStorageInventory(context.Background(), storageScanRequest{DataDir: dir})
	sample := StorageProducerSampleFromInventory(inv, "boot-a")
	if sample.Complete != inv.Complete {
		t.Fatalf("sample completeness = %v, want %v", sample.Complete, inv.Complete)
	}
	for id := range sample.Categories {
		if !isKnownStorageCategory(id) {
			t.Fatalf("producer sample carried unknown category %q", id)
		}
	}
}

func isKnownStorageCategory(id string) bool {
	for _, def := range storageCategoryDefinitions {
		if def.ID == id {
			return true
		}
	}
	_, ok := storageCapacityCategoryLabels[id]
	return ok
}

func storageContains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func TestStorageGrowthWindowFixtures(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	sample := func(at time.Time, boot string, complete bool, cats map[string]int64) storageProducerSample {
		return storageProducerSample{At: at, BootID: boot, Complete: complete, Categories: cats}
	}
	samples := []storageProducerSample{
		sample(now.Add(-24*time.Hour), "boot-a", true, map[string]int64{storageCatWorkflowWorktrees: 1 << 30}),
		sample(now.Add(-12*time.Hour), "boot-a", true, map[string]int64{storageCatWorkflowWorktrees: 1<<30 + 10<<20}),
		sample(now.Add(-time.Hour), "boot-a", true, map[string]int64{storageCatWorkflowWorktrees: 1<<30 + 200<<20}),
	}
	day := StorageGrowthWindow(samples, now, 24*time.Hour)
	if !day.Available || day.Continuity != storageContinuityOK {
		t.Fatalf("day window = %+v", day)
	}
	if len(day.Deltas) != 1 || day.Deltas[0].DeltaBytes != 200<<20 || day.Deltas[0].Noise {
		t.Fatalf("day deltas = %+v", day.Deltas)
	}

	// A small movement is measurement noise; a reboot is explicit continuity.
	noisy := []storageProducerSample{
		sample(now.Add(-24*time.Hour), "boot-a", true, map[string]int64{storageCatWorkflowWorktrees: 1 << 30}),
		sample(now.Add(-12*time.Hour), "boot-a", true, map[string]int64{storageCatWorkflowWorktrees: 1<<30 + 20<<20}),
		sample(now.Add(-time.Hour), "boot-a", true, map[string]int64{storageCatWorkflowWorktrees: 1<<30 + 50<<20}),
	}
	if got := StorageGrowthWindow(noisy, now, 24*time.Hour); len(got.Deltas) != 1 || !got.Deltas[0].Noise {
		t.Fatalf("noise rule not applied: %+v", got.Deltas)
	}
	reboot := []storageProducerSample{
		sample(now.Add(-24*time.Hour), "boot-a", true, map[string]int64{storageCatWorkflowWorktrees: 1 << 30}),
		sample(now.Add(-time.Hour), "boot-b", true, map[string]int64{storageCatWorkflowWorktrees: 2 << 30}),
	}
	if got := StorageGrowthWindow(reboot, now, 24*time.Hour); got.Continuity != storageContinuityReboot || got.Available {
		t.Fatalf("reboot window = %+v", got)
	}

	// Stale: newest sample older than 12h.
	stale := []storageProducerSample{sample(now.Add(-30*time.Hour), "boot-a", true, map[string]int64{storageCatWorkflowWorktrees: 1 << 30})}
	if got := StorageGrowthWindow(stale, now, 24*time.Hour); got.Continuity != storageContinuityStale {
		t.Fatalf("stale window = %+v", got)
	}

	// Gap over 12h between the baseline and the newest sample.
	gap := []storageProducerSample{
		sample(now.Add(-24*time.Hour), "boot-a", true, map[string]int64{storageCatWorkflowWorktrees: 1 << 30}),
		sample(now.Add(-1*time.Hour), "boot-a", true, map[string]int64{storageCatWorkflowWorktrees: 2 << 30}),
	}
	if got := StorageGrowthWindow(gap, now, 24*time.Hour); got.Continuity != storageContinuityGap {
		t.Fatalf("gap window = %+v", got)
	}
	// Capacity readings cannot fill a missing producer history. The producer
	// delta stays unavailable when its own samples are more than 12h apart.
	maskedGap := []storageProducerSample{
		sample(now.Add(-24*time.Hour), "boot-a", true, map[string]int64{storageCatWorkflowWorktrees: 1 << 30}),
		sample(now.Add(-12*time.Hour), "boot-a", true, map[string]int64{storageCatCapacityHostFree: 100 << 30}),
		sample(now.Add(-time.Hour), "boot-a", true, map[string]int64{storageCatWorkflowWorktrees: 2 << 30}),
	}
	if got := StorageGrowthWindow(maskedGap, now, 24*time.Hour); got.Available || got.Continuity != storageContinuityGap || len(got.Deltas) != 0 {
		t.Fatalf("capacity samples masked a producer gap: %+v", got)
	}

	// Incomplete endpoints yield no delta.
	incomplete := []storageProducerSample{
		sample(now.Add(-24*time.Hour), "boot-a", false, map[string]int64{storageCatWorkflowWorktrees: 1 << 30}),
		sample(now.Add(-time.Hour), "boot-a", true, map[string]int64{storageCatWorkflowWorktrees: 2 << 30}),
	}
	if got := StorageGrowthWindow(incomplete, now, 24*time.Hour); got.Available {
		t.Fatalf("incomplete window produced deltas: %+v", got)
	}

	// The 7-day window uses a wider baseline tolerance and requires no >12h gap.
	weekSamples := []storageProducerSample{sample(now.Add(-168*time.Hour+11*time.Hour), "boot-a", true, map[string]int64{storageCatCapacityVHDX: 900 << 30})}
	for hours := 145; hours >= 2; hours -= 11 {
		weekSamples = append(weekSamples, sample(now.Add(-time.Duration(hours)*time.Hour), "boot-a", true, map[string]int64{storageCatCapacityVHDX: 949 << 30}))
	}
	if got := StorageGrowthWindow(weekSamples, now, 168*time.Hour); !got.Available || got.Window != "168h" {
		t.Fatalf("week window = %+v", got)
	}
}

func TestStorageCapacitySamplesIgnoreOperationsAndProducers(t *testing.T) {
	now := time.Now().UTC()
	events := []hostStorageEvent{
		{At: now, Kind: "sample", HostFreeMB: 100, VHDXPhysicalMB: 200, GuestFreeMB: 300},
		{At: now, Kind: "cleanup_operation", CleanupOperation: &storageCleanupOperation{At: now, Kind: "worktree_gc"}},
		{At: now, Kind: "producer_sample", Producer: &storageProducerSample{At: now, Complete: true, Categories: map[string]int64{storageCatWorkflowWorktrees: 1}}},
	}
	samples := StorageCapacitySamples(events)
	if len(samples) != 1 {
		t.Fatalf("capacity samples = %+v", samples)
	}
	if samples[0].Categories[storageCatCapacityVHDX] != 200<<20 {
		t.Fatalf("vhdx capacity = %+v", samples[0].Categories)
	}
}

func TestStorageGuestOnlyPendingHostReclaimState(t *testing.T) {
	now := time.Now().UTC()
	guest := storageCleanupOperation{At: now.Add(-time.Hour), Kind: "worktree_gc", Layer: "guest", Executed: true, Complete: true}
	if StorageGuestOnlyPendingHostReclaim([]storageCleanupOperation{guest}) {
		t.Fatal("a cleanup with no measured removal cannot imply pending host reclaim")
	}
	removed := int64(100)
	guest.ApparentBytes = &removed
	if !StorageGuestOnlyPendingHostReclaim([]storageCleanupOperation{guest}) {
		t.Fatal("guest cleanup without host reclamation must be pending")
	}
	verifiedGuest := guest
	verifiedGuest.Verified = true
	if !StorageGuestOnlyPendingHostReclaim([]storageCleanupOperation{verifiedGuest}) {
		t.Fatal("verified guest cleanup still needs measured host reclamation")
	}
	unverifiedHost := storageCleanupOperation{At: now, Kind: "windows_maintenance", Layer: "host", Executed: true}
	if !StorageGuestOnlyPendingHostReclaim([]storageCleanupOperation{verifiedGuest, unverifiedHost}) {
		t.Fatal("unverified host work must not clear the state")
	}
	host := storageCleanupOperation{At: now, Kind: "windows_maintenance", Layer: "host", Executed: true, Verified: true}
	if !StorageGuestOnlyPendingHostReclaim([]storageCleanupOperation{verifiedGuest, host}) {
		t.Fatal("host maintenance without measured VHDX reclamation must not clear the state")
	}
	reclaimed := int64(100)
	host.ReclaimedBytes = &reclaimed
	if StorageGuestOnlyPendingHostReclaim([]storageCleanupOperation{guest, host}) {
		t.Fatal("verified later host reclamation must clear the state")
	}
	if StorageCleanupMessage(storageStateGuestOnlyPendingHostReclaim) != "Linux space freed; Windows image still allocated; maintenance needed" {
		t.Fatal("guest-only message changed")
	}
}
