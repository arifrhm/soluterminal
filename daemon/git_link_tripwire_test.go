package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type gitLinkGuard struct {
	path    string
	wasFile bool
	content []byte
}

func snapshotEnclosingGitLink() *gitLinkGuard {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	return snapshotEnclosingGitLinkFrom(cwd)
}

func snapshotEnclosingGitLinkFrom(start string) *gitLinkGuard {
	dir, err := filepath.Abs(start)
	if err != nil {
		return nil
	}
	for {
		path := filepath.Join(dir, ".git")
		info, statErr := os.Lstat(path)
		if statErr == nil {
			if info.IsDir() {
				return &gitLinkGuard{path: path}
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			return &gitLinkGuard{path: path, wasFile: true, content: content}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

func (g *gitLinkGuard) check() string {
	if g == nil || g.path == "" {
		return ""
	}
	info, statErr := os.Lstat(g.path)
	if !g.wasFile {
		if statErr == nil && info.IsDir() {
			return ""
		}
		if statErr != nil {
			return fmt.Sprintf("%s changed from a directory: %v", g.path, statErr)
		}
		return fmt.Sprintf("%s changed from a directory to %s", g.path, info.Mode().Type())
	}

	var current []byte
	readErr := statErr
	if statErr == nil && info.Mode().IsRegular() {
		current, readErr = os.ReadFile(g.path)
	}
	if readErr == nil && bytes.Equal(current, g.content) {
		return ""
	}
	offending := "<unreadable>"
	if current != nil {
		offending = strings.TrimSpace(string(current))
	}
	restoreErr := os.WriteFile(g.path, g.content, 0o644)
	if restoreErr != nil {
		return fmt.Sprintf("%s changed to %q (restore failed: %v)", g.path, offending, restoreErr)
	}
	return fmt.Sprintf("%s changed to %q; original content restored", g.path, offending)
}

func TestGitLinkTripwireDetectsClobber(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "review-worktree")
	nested := filepath.Join(worktree, "daemon")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("gitdir: " + filepath.Join(root, "repo", ".git", "worktrees", "review-worktree") + "\n")
	link := filepath.Join(worktree, ".git")
	if err := os.WriteFile(link, original, 0o644); err != nil {
		t.Fatal(err)
	}
	guard := snapshotEnclosingGitLinkFrom(nested)
	if guard == nil || guard.path != link || !guard.wasFile {
		t.Fatalf("snapshot = %+v, want file guard for %s", guard, link)
	}
	if err := os.WriteFile(link, []byte("gitdir: /tmp/fake_git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg := guard.check()
	if !strings.Contains(msg, "gitdir: /tmp/fake_git") {
		t.Fatalf("check message = %q, want offending gitdir content", msg)
	}
	got, err := os.ReadFile(link)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("restored link = %q, want %q", got, original)
	}
	if msg := guard.check(); msg != "" {
		t.Fatalf("clean recheck = %q, want empty", msg)
	}
}

func TestGitLinkTripwireSnapshotsCurrentWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "review-worktree")
	nested := filepath.Join(worktree, "daemon", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(worktree, ".git")
	original := []byte("gitdir: " + filepath.Join(root, "repo", ".git", "worktrees", "review-worktree") + "\n")
	if err := os.WriteFile(link, original, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	guard := snapshotEnclosingGitLink()
	if guard == nil || guard.path != link || !guard.wasFile || !bytes.Equal(guard.content, original) {
		t.Fatalf("snapshot after chdir = %+v, want file guard for %s", guard, link)
	}
}
