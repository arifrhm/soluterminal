package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testRepoCommit(t *testing.T, repo, file, content, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("add", "-A")
	run("commit", "-q", "-m", msg)
	return run("rev-parse", "HEAD")
}
func fakeBindingsFor(runtimeID string) map[string]roleBinding {
	return map[string]roleBinding{
		"*":                                 {Runtime: runtimeID},
		"executor":                          {Runtime: runtimeID, Model: "builder"},
		"reviewer:regression_and_test_gaps": {Runtime: runtimeID, Model: "lens-b"},
	}
}
