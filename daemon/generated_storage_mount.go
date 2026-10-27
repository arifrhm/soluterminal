package main

import (
	"os"
	"path/filepath"
)

type generatedMountIdentity struct {
	Device  uint64
	MountID uint64
}

type generatedMountProbe func(string) (generatedMountIdentity, error)
type generatedMountDescendants func(string, string) (bool, error)

// Canonical paths can include children that do not exist yet. Only ENOENT
// permits walking upward; permission/capability failures are not mount proof.
func generatedProtectionMount(path string, probe generatedMountProbe) (generatedMountIdentity, error) {
	for {
		identity, err := probe(path)
		if err == nil {
			if identity.MountID == 0 {
				return generatedMountIdentity{}, errWFGeneratedUnsupported
			}
			return identity, nil
		}
		if !os.IsNotExist(err) {
			return generatedMountIdentity{}, err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return generatedMountIdentity{}, err
		}
		path = parent
	}
}

// Lexical/canonical overlap wins even across devices. Disjoint spellings on
// different mounts of one device may still alias a root or subtree: symlink
// resolution and the remover's NO_XDEV traversal cannot prove otherwise.
// The per-call probe keeps fixtures independent of process-global state.
func generatedProtectedOverlapWithProbe(path, protected string, probe generatedMountProbe, descendants generatedMountDescendants) bool {
	overlap := func(a, b string) bool {
		return filepath.Clean(a) == filepath.Clean(b) || pathUnderRoot(a, b) || pathUnderRoot(b, a)
	}
	if overlap(path, protected) {
		return true
	}
	canonicalPath, pathErr := generatedCanonicalProtectionPath(path)
	canonicalProtected, protectedErr := generatedCanonicalProtectionPath(protected)
	if pathErr != nil || protectedErr != nil {
		return true
	}
	if overlap(canonicalPath, canonicalProtected) || overlap(path, canonicalProtected) || overlap(canonicalPath, protected) {
		return true
	}
	// Root identities alone cannot exclude a bind alias mounted below either
	// root, even when those roots are on different devices. Require one known,
	// bounded descendant snapshot before either disjointness conclusion.
	if descendants == nil {
		return true
	}
	if found, err := descendants(canonicalPath, canonicalProtected); err != nil || found {
		return true
	}
	a, err := generatedProtectionMount(canonicalPath, probe)
	if err != nil {
		return true
	}
	b, err := generatedProtectionMount(canonicalProtected, probe)
	if err != nil {
		return true
	}
	if a.Device != b.Device {
		return false
	}
	return a.MountID != b.MountID
}
