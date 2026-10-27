//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func overrideFixturePath(t *testing.T, root, variant string) string {
	t.Helper()
	switch variant {
	case "exact":
		return root
	case "child":
		return filepath.Join(root, "missing", "cache")
	case "parent":
		return filepath.Dir(root)
	case "alias", "alias-child", "alias-parent":
		target := root
		if variant == "alias-parent" {
			target = filepath.Dir(root)
		}
		alias := filepath.Join(t.TempDir(), "alias")
		if err := os.Symlink(target, alias); err != nil {
			t.Fatal(err)
		}
		if variant == "alias-child" {
			return filepath.Join(alias, "missing", "cache")
		}
		return alias
	}
	t.Fatalf("unknown variant %s", variant)
	return ""
}

func TestGeneratedStorageProtectionCanonicalAndAmbiguousPaths(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "protected")
	other := filepath.Join(parent, "other")
	for _, path := range []string{root, other} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(other, alias); err != nil {
		t.Fatal(err)
	}
	// Preserve .. until after symlink resolution, rather than cleaning it
	// into the alias's lexical parent.
	if !generatedProtectedOverlap(root, alias+"/../protected") {
		t.Fatal("symlink/.. overlap missed")
	}
	if generatedProtectedOverlap(root, root+"-sibling") {
		t.Fatal("lexical prefix treated as ancestry")
	}
	if generatedProtectedOverlap(root, filepath.Join(alias, "missing", "cache")) {
		t.Fatal("disjoint missing child refused")
	}
	dangling := filepath.Join(parent, "dangling")
	if err := os.Symlink(filepath.Join(parent, "absent"), dangling); err != nil {
		t.Fatal(err)
	}
	if !generatedProtectedOverlap(root, dangling) {
		t.Fatal("ambiguous override treated as disjoint")
	}
}

func TestGeneratedStorageOverridePreventsCargoAggregateReclassification(t *testing.T) {
	generatedTestState(t)
	generatedBudgetOverride = func(string) (int64, int64, bool) { return 1, 0, true }
	resolved, err := generatedResolve(storageCargo, "repo", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resolved.Path, "sentinel"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOLU_WORKFLOW_GOCACHE_DIR", filepath.Dir(resolved.Path))
	report := generatedStoragePlan(context.Background(), true)
	for _, entry := range report.Entries {
		if entry.Path == resolved.Path && entry.Disposition != generatedExcluded {
			t.Fatalf("aggregate reopened exclusion: %+v", entry)
		}
	}
}

func TestGeneratedStorageRegistryMarkerChangeBeforeMutation(t *testing.T) {
	for _, selective := range []bool{false, true} {
		t.Run(fmt.Sprintf("selective=%v", selective), func(t *testing.T) {
			generatedTestState(t)
			t.Setenv("SOLU_WORKFLOW_GOCACHE_DIR", "")
			resolved, err := generatedResolve(storageNPM, "", true)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"victim", "pinned"} {
				if err := os.WriteFile(filepath.Join(resolved.Path, name), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			identity, err := generatedIdentity(resolved.Path)
			if err != nil {
				t.Fatal(err)
			}
			generatedStorageState.Lock()
			registry, err := generatedReadRegistryLocked()
			record, valid := generatedRegistryOwnedRecord(registry, storageNPM, resolved.Path, identity)
			generatedStorageState.Unlock()
			if err != nil || !valid {
				t.Fatalf("capture registry proof: valid=%v err=%v", valid, err)
			}
			if err := os.Chmod(resolved.Path, 0500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(resolved.Path, 0700) })
			fs := generatedMarkedFS(generatedMarkerName, generatedRegistryMarkerValidator(record.generatedStorageMarker))
			fs.afterPreflight = func() {
				changed := record.generatedStorageMarker
				changed.OwnershipID += "-replacement"
				raw, err := json.Marshal(changed)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(resolved.Path, generatedMarkerName), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var rels []string
			if selective {
				rels = []string{"victim"}
			}
			if err := fs.remove(context.Background(), resolved.Path, identity, rels, generatedMarkerName, false); generatedReason(err) != "ownership_marker_mismatch" {
				t.Fatalf("registry owner change accepted: %v", err)
			}
			for _, name := range []string{"victim", "pinned"} {
				if raw, err := os.ReadFile(filepath.Join(resolved.Path, name)); err != nil || string(raw) != "keep" {
					t.Fatalf("payload %s changed: %q %v", name, raw, err)
				}
			}
			if info, err := os.Stat(resolved.Path); err != nil || info.Mode().Perm() != 0500 {
				t.Fatalf("failed proof changed root mode: %v %v", info, err)
			}
		})
	}
}
