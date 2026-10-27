package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAriaGitStatusSummarizesRepo(t *testing.T) {
	root := initGitRepo(t)
	writeFile(t, filepath.Join(root, "notes.txt"), "one\n")
	gitCmd(t, root, "add", "notes.txt")
	gitCmd(t, root, "commit", "-m", "initial")
	writeFile(t, filepath.Join(root, "notes.txt"), "one\ntwo\n")

	got := (&Aria{}).toolGitStatus(root, true, 3)
	var out struct {
		Root           string   `json:"root"`
		Status         []string `json:"status"`
		Commits        []string `json:"commits"`
		DiffStat       []string `json:"diff_stat"`
		StagedDiffStat []string `json:"staged_diff_stat"`
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("toolGitStatus returned invalid json %q: %v", got, err)
	}
	// Git reports forward slashes even when Windows TempDir uses backslashes.
	if filepath.Clean(out.Root) != filepath.Clean(root) {
		t.Fatalf("root = %q, want %q", out.Root, root)
	}
	if len(out.Status) == 0 || !strings.Contains(strings.Join(out.Status, "\n"), "notes.txt") {
		t.Fatalf("status = %#v, want notes.txt", out.Status)
	}
	if len(out.Commits) == 0 || !strings.Contains(out.Commits[0], "initial") {
		t.Fatalf("commits = %#v, want initial commit", out.Commits)
	}
	if len(out.DiffStat) == 0 || !strings.Contains(strings.Join(out.DiffStat, "\n"), "notes.txt") {
		t.Fatalf("diff_stat = %#v, want notes.txt", out.DiffStat)
	}
}

func TestAriaGitStatusRejectsNonRepo(t *testing.T) {
	t.Parallel()
	got := (&Aria{}).toolGitStatus(t.TempDir(), true, 5)
	if !strings.Contains(got, "not a git repo") {
		t.Fatalf("toolGitStatus = %q, want not a git repo error", got)
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitCmd(t, root, "init")
	gitCmd(t, root, "config", "user.email", "aria-test@example.test")
	gitCmd(t, root, "config", "user.name", "ARIA Test")
	return root
}

func gitCmd(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
