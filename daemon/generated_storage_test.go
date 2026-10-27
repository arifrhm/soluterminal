package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func generatedTestState(t *testing.T) (cacheRoot, workflowRoot string) {
	t.Helper()
	cacheRoot, workflowRoot = t.TempDir(), filepath.Join(t.TempDir(), "workflow")
	t.Setenv("XDG_CACHE_HOME", cacheRoot)
	t.Setenv("GOCACHE", "")
	t.Setenv("GOMODCACHE", "")
	generatedStorageState.Lock()
	oldRoot, oldBase, oldNotices := generatedStorageState.workflowRoot, generatedStorageState.cacheBase, generatedStorageState.notices
	generatedStorageState.workflowRoot, generatedStorageState.notices = workflowRoot, map[string]string{}
	generatedStorageState.cacheBase, _ = generatedCacheBase()
	generatedStorageState.Unlock()
	oldBudget := generatedBudgetOverride
	t.Cleanup(func() {
		generatedStorageState.Lock()
		generatedStorageState.workflowRoot, generatedStorageState.cacheBase, generatedStorageState.notices = oldRoot, oldBase, oldNotices
		generatedStorageState.Unlock()
		generatedBudgetOverride = oldBudget
	})
	return cacheRoot, workflowRoot
}

func TestGeneratedStorageAllocatesWithoutAdoptingLegacyRoot(t *testing.T) {
	cacheRoot, workflowRoot := generatedTestState(t)
	legacy := filepath.Join(cacheRoot, "soluterminal", "go-build")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(legacy, "user-data")
	if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved := generatedResolveOverride(storageGoBuild, "GOCACHE")
	if !resolved.Managed || resolved.Path == "" || resolved.Path == legacy {
		t.Fatalf("resolution=%+v legacy=%s", resolved, legacy)
	}
	if _, err := os.Stat(filepath.Join(resolved.Path, generatedMarkerName)); err != nil {
		t.Fatalf("owned marker: %v", err)
	}
	registry := filepath.Join(workflowRoot, "generated-storage", "v1", "registry.json")
	if info, err := os.Stat(registry); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("registry info=%v err=%v", info, err)
	}
	if raw, err := os.ReadFile(sentinel); err != nil || string(raw) != "untouched" {
		t.Fatalf("legacy sentinel raw=%q err=%v", raw, err)
	}
	report := generatedStoragePlan(context.Background(), true)
	if !slices.ContainsFunc(report.Entries, func(entry generatedStorageCandidate) bool {
		return entry.Path == legacy && entry.Disposition == generatedUnknown
	}) {
		t.Fatalf("legacy root missing from unknown inventory: %+v", report.Entries)
	}
}

func TestGeneratedStorageDryRunIsByteAndIdentityStable(t *testing.T) {
	_, workflowRoot := generatedTestState(t)
	generatedBudgetOverride = func(category string) (int64, int64, bool) {
		if category == storageNPM {
			return 1, 0, true
		}
		return 0, 0, false
	}
	resolved, err := generatedResolve(storageNPM, "", true)
	if err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(resolved.Path, "payload")
	if err := os.WriteFile(payload, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(workflowRoot, "generated-storage", "v1", "registry.json")
	registryBefore, _ := os.ReadFile(registryPath)
	markerPath := filepath.Join(resolved.Path, generatedMarkerName)
	markerBefore, _ := os.Stat(markerPath)
	identityBefore, _ := generatedIdentity(resolved.Path)
	report := generatedStoragePlan(context.Background(), true)
	if report.Counts.Eligible != 1 || report.MeasuredRemovedApparentBytes != nil || report.ObservedGuestFreeBytesDelta != nil {
		t.Fatalf("dry report=%+v", report)
	}
	registryAfter, _ := os.ReadFile(registryPath)
	markerAfter, _ := os.Stat(markerPath)
	identityAfter, _ := generatedIdentity(resolved.Path)
	if string(registryBefore) != string(registryAfter) || markerBefore.ModTime() != markerAfter.ModTime() || identityBefore != identityAfter {
		t.Fatal("dry-run mutated registry, marker, or directory identity")
	}
	if raw, err := os.ReadFile(payload); err != nil || string(raw) != "fixture" {
		t.Fatalf("dry-run payload raw=%q err=%v", raw, err)
	}
}

func TestGeneratedStorageHotResolveDoesNotRewriteRegistry(t *testing.T) {
	_, workflowRoot := generatedTestState(t)
	repo := t.TempDir()
	firstEnv := workerEnvForRepo(repo)
	registryPath := filepath.Join(workflowRoot, "generated-storage", "v1", "registry.json")
	before, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		if again := workerEnvForRepo(repo); !slices.Equal(again, firstEnv) {
			t.Fatalf("hot worker environment changed: before=%v after=%v", firstEnv, again)
		}
	}
	after, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("hot cache resolution rewrote the durable registry")
	}
}

func TestGeneratedStorageCorruptRegistryIsUnknownAndDryRunDoesNotRewriteIt(t *testing.T) {
	_, workflowRoot := generatedTestState(t)
	path := filepath.Join(workflowRoot, "generated-storage", "v1", "registry.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	want := []byte("{corrupt")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	report := generatedStoragePlan(context.Background(), true)
	if report.Counts.Unknown != 1 || report.Entries[0].Reason != "registry_unreadable" {
		t.Fatalf("corrupt registry report=%+v", report)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(want) {
		t.Fatalf("dry plan rewrote corrupt registry got=%q err=%v", got, err)
	}
}

func TestGeneratedStorageBudgetMatrix(t *testing.T) {
	old := generatedBudgetOverride
	generatedBudgetOverride = nil
	t.Cleanup(func() { generatedBudgetOverride = old })
	for category, want := range map[string][2]int64{
		storageGoMod:      {16 * generatedGiB, 12 * generatedGiB},
		storageNPM:        {8 * generatedGiB, 4 * generatedGiB},
		storagePlaywright: {8 * generatedGiB, 4 * generatedGiB},
		storageCargo:      {32 * generatedGiB, 24 * generatedGiB},
	} {
		high, low := generatedBudget(category)
		if high != want[0] || low != want[1] {
			t.Fatalf("%s budget=(%d,%d), want=(%d,%d)", category, high, low, want[0], want[1])
		}
	}
}

func TestGeneratedStorageCargoAggregateUsesLRU(t *testing.T) {
	generatedTestState(t)
	aggregateHigh, aggregateLow := int64(1<<20), int64(512<<10)
	generatedBudgetOverride = func(category string) (int64, int64, bool) {
		switch category {
		case storageCargo:
			return 1 << 20, 512 << 10, true
		case "cargo-aggregate":
			return aggregateHigh, aggregateLow, true
		}
		return 0, 0, false
	}
	var paths []string
	for _, repo := range []string{"repo-old", "repo-middle", "repo-new"} {
		resolved, err := generatedResolve(storageCargo, repo, true)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, resolved.Path)
		if err := os.WriteFile(filepath.Join(resolved.Path, "payload"), make([]byte, 100), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	generatedStorageState.Lock()
	registry, _ := generatedReadRegistryLocked()
	for i := range registry.Roots {
		rank := map[string]int64{"repo-old": 1, "repo-middle": 2, "repo-new": 3}[registry.Roots[i].RepositoryIdentity]
		registry.Roots[i].LastUsedAt = time.Unix(rank, 0)
	}
	_ = generatedWriteRegistryLocked(registry)
	generatedStorageState.Unlock()
	// Directory sizes differ across filesystems. Put the high-water mark
	// just below this fixture's total and the low-water mark exactly where
	// removing its oldest root leaves it, so the same LRU decision is required.
	var total, oldest int64
	measured := 0
	for _, entry := range generatedStoragePlan(context.Background(), true).Entries {
		if entry.Category != storageCargo {
			continue
		}
		if !entry.MeasurementComplete || entry.EstimatedApparentBytes <= 0 {
			t.Fatalf("incomplete cargo fixture measurement: %+v", entry)
		}
		measured++
		total += entry.EstimatedApparentBytes
		if entry.Path == paths[0] {
			oldest = entry.EstimatedApparentBytes
		}
	}
	if measured != len(paths) || oldest == 0 {
		t.Fatalf("cargo fixture roots measured=%d want=%d oldest=%d", measured, len(paths), oldest)
	}
	aggregateHigh, aggregateLow = total-1, total-oldest
	report := generatedStoragePlan(context.Background(), true)
	var eligible []string
	for _, entry := range report.Entries {
		if entry.Category == storageCargo && entry.Disposition == generatedEligible {
			eligible = append(eligible, entry.Path)
		}
	}
	if len(eligible) != 1 || eligible[0] != paths[0] {
		t.Fatalf("cargo eligible=%v paths=%v report=%+v", eligible, paths, report.Entries)
	}
}

func TestGeneratedStorageInventoryCancellationDoesNotWaitForRegistryWriter(t *testing.T) {
	generatedTestState(t)
	generatedStorageState.Lock()
	defer generatedStorageState.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan generatedStorageReport, 1)
	go func() { done <- generatedStoragePlan(ctx, true) }()
	select {
	case report := <-done:
		if report.MeasurementComplete || report.Counts.Eligible != 0 {
			t.Fatalf("cancelled report=%+v", report)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled inventory waited for registry writer")
	}
}

func TestGeneratedStorageIncompleteCorruptAndChangedIdentityRefuseMutation(t *testing.T) {
	t.Run("cancelled measurement is incomplete", func(t *testing.T) {
		generatedTestState(t)
		resolved, err := generatedResolve(storageNPM, "", true)
		if err != nil {
			t.Fatal(err)
		}
		identity, err := generatedIdentity(resolved.Path)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, _, complete, reason := generatedMeasure(ctx, resolved.Path, identity); complete || reason != "measurement_incomplete" {
			t.Fatalf("complete=%v reason=%q", complete, reason)
		}
	})

	t.Run("corrupt marker is refused", func(t *testing.T) {
		generatedTestState(t)
		resolved, err := generatedResolve(storageNPM, "", true)
		if err != nil {
			t.Fatal(err)
		}
		payload := filepath.Join(resolved.Path, "payload")
		if err := os.WriteFile(payload, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(resolved.Path, generatedMarkerName), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		report := generatedStoragePlan(context.Background(), true)
		if !slices.ContainsFunc(report.Entries, func(entry generatedStorageCandidate) bool {
			return entry.Path == resolved.Path && entry.Disposition == generatedRefused && entry.Reason == "ownership_marker_corrupt"
		}) {
			t.Fatalf("corrupt marker report=%+v", report.Entries)
		}
		if raw, err := os.ReadFile(payload); err != nil || string(raw) != "keep" {
			t.Fatalf("corrupt candidate mutated raw=%q err=%v", raw, err)
		}
	})

	t.Run("replacement identity is refused", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "candidate")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		identity, err := generatedIdentity(path)
		if err != nil {
			t.Fatal(err)
		}
		old := path + "-old"
		if err := os.Rename(path, old); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		sentinel := filepath.Join(path, "sentinel")
		if err := os.WriteFile(sentinel, []byte("replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := generatedRemove(context.Background(), path, identity, nil); err == nil || !slices.Contains([]string{"changed_identity"}, generatedReason(err)) {
			t.Fatalf("replacement removal err=%v", err)
		}
		if raw, err := os.ReadFile(sentinel); err != nil || string(raw) != "replacement" {
			t.Fatalf("replacement changed raw=%q err=%v", raw, err)
		}
	})
}
