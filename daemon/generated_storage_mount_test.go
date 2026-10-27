package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func generatedNoDescendantMounts(string, string) (bool, error) { return false, nil }

func TestGeneratedStorageMountProtectionMissingDescendantEvidence(t *testing.T) {
	if !generatedProtectedOverlapWithProbe(t.TempDir(), t.TempDir(), func(string) (generatedMountIdentity, error) {
		t.Fatal("root probe cannot replace missing descendant evidence")
		return generatedMountIdentity{}, nil
	}, nil) {
		t.Fatal("missing descendant evidence allowed cleanup")
	}
}

func TestGeneratedStorageMountProtectionEvidence(t *testing.T) {
	root := t.TempDir()
	left, right := filepath.Join(root, "left"), filepath.Join(root, "right")
	for _, path := range []string{left, right, filepath.Join(left, "child"), filepath.Join(right, "child")} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	// Canonical fixture paths also work when the platform's temp root is an alias.
	left, _ = filepath.EvalSymlinks(left)
	right, _ = filepath.EvalSymlinks(right)
	for _, test := range []struct {
		name            string
		path, protected string
		a, b            generatedMountIdentity
		aErr, bErr      error
		want            bool
		wantProbeCalls  int
	}{
		{"root_alias", left, right, generatedMountIdentity{7, 11}, generatedMountIdentity{7, 12}, nil, nil, true, 2},
		{"subtree_alias", left, filepath.Join(right, "child"), generatedMountIdentity{7, 11}, generatedMountIdentity{7, 12}, nil, nil, true, 2},
		{"parent_alias", filepath.Join(left, "child"), right, generatedMountIdentity{7, 11}, generatedMountIdentity{7, 12}, nil, nil, true, 2},
		{"same_mount_disjoint", left, right, generatedMountIdentity{7, 11}, generatedMountIdentity{7, 11}, nil, nil, false, 2},
		{"different_devices_disjoint", left, right, generatedMountIdentity{7, 11}, generatedMountIdentity{8, 12}, nil, nil, false, 2},
		{"candidate_error", left, right, generatedMountIdentity{}, generatedMountIdentity{7, 11}, os.ErrPermission, nil, true, 1},
		{"protected_error", left, right, generatedMountIdentity{7, 11}, generatedMountIdentity{}, nil, os.ErrPermission, true, 2},
		{"unsupported", left, right, generatedMountIdentity{7, 11}, generatedMountIdentity{}, nil, errWFGeneratedUnsupported, true, 2},
		{"unknown_mount", left, right, generatedMountIdentity{7, 11}, generatedMountIdentity{7, 0}, nil, nil, true, 2},
		{"lexical_child_wins", left, filepath.Join(left, "child"), generatedMountIdentity{7, 11}, generatedMountIdentity{8, 12}, nil, nil, true, 0},
		{"lexical_parent_wins", filepath.Join(left, "child"), left, generatedMountIdentity{7, 11}, generatedMountIdentity{8, 12}, nil, nil, true, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			probe := func(path string) (generatedMountIdentity, error) {
				calls++
				switch path {
				case test.path:
					return test.a, test.aErr
				case test.protected:
					return test.b, test.bErr
				default:
					t.Fatalf("unexpected prefix probe: %q", path)
					return generatedMountIdentity{}, os.ErrPermission
				}
			}
			if got := generatedProtectedOverlapWithProbe(test.path, test.protected, probe, generatedNoDescendantMounts); got != test.want {
				t.Errorf("overlap=%v want %v", got, test.want)
			}
			if calls != test.wantProbeCalls {
				t.Errorf("probe calls=%d want %d", calls, test.wantProbeCalls)
			}
		})
	}
}

func TestGeneratedStorageMountProtectionMissingPrefix(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	left, right := filepath.Join(root, "left"), filepath.Join(root, "right")
	for _, path := range []string{left, right} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name string
		b    generatedMountIdentity
		want bool
	}{
		{"possible_alias", generatedMountIdentity{7, 12}, true},
		{"same_mount", generatedMountIdentity{7, 11}, false},
		{"other_device", generatedMountIdentity{8, 12}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, b := filepath.Join(left, "missing", "cache"), filepath.Join(right, "missing", "cache")
			var calls []string
			probe := func(path string) (generatedMountIdentity, error) {
				calls = append(calls, path)
				if path == left {
					return generatedMountIdentity{7, 11}, nil
				}
				if path == right {
					return test.b, nil
				}
				return generatedMountIdentity{}, &os.PathError{Op: "statx", Path: path, Err: os.ErrNotExist}
			}
			if got := generatedProtectedOverlapWithProbe(a, b, probe, generatedNoDescendantMounts); got != test.want {
				t.Errorf("overlap=%v want %v", got, test.want)
			}
			want := []string{a, filepath.Dir(a), left, b, filepath.Dir(b), right}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("prefix probes=%v want %v", calls, want)
			}
		})
	}
}

func TestGeneratedStorageMountProtectionCanonicalOverlap(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, protected := range []string{alias, filepath.Join(alias, "missing", "cache")} {
		if !generatedProtectedOverlapWithProbe(root, protected, func(string) (generatedMountIdentity, error) {
			t.Fatal("canonical overlap must win before mount evidence")
			return generatedMountIdentity{}, nil
		}, generatedNoDescendantMounts) {
			t.Fatalf("canonical overlap missed: %q", protected)
		}
	}
}

func TestGeneratedStorageMountProtectionProductionUnknown(t *testing.T) {
	// Exercise the real overlap entry point and platform probe: an existing
	// non-directory cannot prove which mount a future cache directory uses.
	root := t.TempDir()
	protected := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(protected, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if !generatedProtectedOverlap(root, protected) {
		t.Fatal("production overlap entry point accepted unknown mount evidence")
	}
}

func TestGeneratedStorageMountProtectionDoesNotAscendOnUnknown(t *testing.T) {
	for _, probeErr := range []error{os.ErrPermission, errWFGeneratedUnsupported} {
		calls := 0
		_, err := generatedProtectionMount(filepath.Join(t.TempDir(), "missing"), func(string) (generatedMountIdentity, error) {
			calls++
			return generatedMountIdentity{}, probeErr
		})
		if !errors.Is(err, probeErr) || calls != 1 {
			t.Fatalf("unknown probe was bypassed: err=%v calls=%d", err, calls)
		}
	}
}
