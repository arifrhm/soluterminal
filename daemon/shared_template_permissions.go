package main

import (
	"fmt"
	"os"
	"path/filepath"

	"strings"
)

// Reject symlink components before creating any directory. Roots are always
// proper descendants of the isolated worktree, never its Git administration.
func templateCanonicalRoot(worktree, rel string, create bool) (string, error) {
	base, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return "", err
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return "", err
	}
	if !templateValidDocsScope(filepath.ToSlash(rel) + "/**") {
		return "", fmt.Errorf("invalid documentation root %q", rel)
	}
	current := base
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) && create {
			if err = os.Mkdir(current, 0755); err != nil {
				return "", err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("documentation root contains symlink or non-directory: %s", current)
		}
	}
	return current, nil
}

func templateValidDocsScope(scope string) bool {
	if !strings.HasSuffix(scope, "/**") || strings.ContainsAny(scope, "\\\x00") {
		return false
	}
	root := strings.TrimSuffix(scope, "/**")
	if root == "" || root == "." || filepath.IsAbs(root) || filepath.ToSlash(filepath.Clean(root)) != root || strings.ContainsAny(root, "*?[]:") {
		return false
	}
	for _, part := range strings.Split(root, "/") {
		if part == ".." || part == ".git" || part == "" {
			return false
		}
	}
	return true
}
