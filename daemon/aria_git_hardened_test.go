package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// The MCP git_status tool runs on repositories agents control, as the daemon
// and outside any sandbox. Plain `git status` / `git diff --stat` would run
// the repo's clean filter, fsmonitor hook, external diff or textconv.
func TestGitStatusToolNeverRunsRepoConfiguredPrograms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	root := initGitRepo(t)
	sentinel := filepath.Join(t.TempDir(), "ran")
	prog := filepath.Join(t.TempDir(), "evil.sh")
	writeFile(t, prog, "#!/bin/sh\necho ran >> '"+sentinel+"'\ncat\n")
	os.Chmod(prog, 0o755)
	writeFile(t, filepath.Join(root, ".gitattributes"), "*.txt filter=evil diff=evil\n")
	writeFile(t, filepath.Join(root, "a.txt"), "one\n")
	gitCmd(t, root, "add", "-A")
	gitCmd(t, root, "commit", "-m", "init")
	for _, kv := range [][2]string{{"filter.evil.clean", prog}, {"core.fsmonitor", prog}, {"diff.external", prog}, {"diff.evil.textconv", prog},
		{"log.showSignature", "true"}, {"gpg.program", prog}, {"gpg.format", "openpgp"}} {
		gitCmd(t, root, "config", kv[0], kv[1])
	}
	// A "signed" HEAD commit: any gpgsig header makes `git log` with
	// log.showSignature run gpg.program to verify it.
	tree := strings.TrimSpace(gitCmdOut(t, root, "rev-parse", "HEAD^{tree}"))
	head := strings.TrimSpace(gitCmdOut(t, root, "rev-parse", "HEAD"))
	body := "tree " + tree + "\nparent " + head + "\nauthor A <a@b> 1 +0000\ncommitter A <a@b> 1 +0000\ngpgsig -----BEGIN PGP SIGNATURE-----\n \n -----END PGP SIGNATURE-----\n\nsigned\n"
	signedFile := filepath.Join(t.TempDir(), "commit")
	writeFile(t, signedFile, body)
	signed := strings.TrimSpace(gitCmdOut(t, root, "hash-object", "-t", "commit", "-w", signedFile))
	gitCmd(t, root, "update-ref", "HEAD", signed)
	writeFile(t, filepath.Join(root, "a.txt"), "two\n")

	got := (&Aria{}).toolGitStatus(root, true, 3)
	if b, err := os.ReadFile(sentinel); err == nil {
		t.Fatalf("a repository-configured program ran: %q", b)
	}
	if !strings.Contains(got, " M a.txt") {
		t.Fatalf("modified file not reported: %s", got)
	}
}

// The hardened status must read like `git status --short --branch`.
func TestGitStatusToolMatchesGitStatusFormat(t *testing.T) {
	root := initGitRepo(t)
	writeFile(t, filepath.Join(root, "keep.txt"), "k\n")
	writeFile(t, filepath.Join(root, "mod.txt"), "1\n")
	writeFile(t, filepath.Join(root, "gone.txt"), "x\n")
	writeFile(t, filepath.Join(root, "both.txt"), "1\n")
	gitCmd(t, root, "add", "-A")
	gitCmd(t, root, "commit", "-m", "init")
	writeFile(t, filepath.Join(root, "mod.txt"), "2\n")  // unstaged M
	os.Remove(filepath.Join(root, "gone.txt"))           // unstaged D
	writeFile(t, filepath.Join(root, "both.txt"), "2\n") // staged M ...
	gitCmd(t, root, "add", "both.txt")
	writeFile(t, filepath.Join(root, "both.txt"), "3\n") // ... and unstaged M
	writeFile(t, filepath.Join(root, "new.txt"), "n\n")  // staged A
	gitCmd(t, root, "add", "new.txt")
	writeFile(t, filepath.Join(root, "untracked.txt"), "u\n") // ??

	var out struct {
		Status []string `json:"status"`
	}
	if err := json.Unmarshal([]byte((&Aria{}).toolGitStatus(root, false, 1)), &out); err != nil {
		t.Fatal(err)
	}
	real, err := exec.Command("git", "-C", root, "status", "--short", "--branch").Output()
	if err != nil {
		t.Fatal(err)
	}
	want := splitNonEmptyLines(string(real))
	norm := func(l []string) []string {
		c := append([]string(nil), l[1:]...) // body lines; branch header checked separately
		sort.Strings(c)
		return c
	}
	if strings.Join(norm(out.Status), "\n") != strings.Join(norm(want), "\n") {
		t.Fatalf("status lines differ\n got: %q\nwant: %q", norm(out.Status), norm(want))
	}
	if out.Status[0] != want[0] {
		t.Fatalf("branch header %q, want %q", out.Status[0], want[0])
	}
}

func gitCmdOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// A modified tracked file the snapshot cannot read must not look clean, and
// untracked directories collapse to one line like git status.
func TestGitStatusToolFlagsUnknownAndCollapsesUntrackedDirs(t *testing.T) {
	root := initGitRepo(t)
	writeFile(t, filepath.Join(root, "big.bin"), "x\n")
	gitCmd(t, root, "add", "-A")
	gitCmd(t, root, "commit", "-m", "init")
	big := make([]byte, turnTrackedMaxBytes+1)
	writeFile(t, filepath.Join(root, "big.bin"), string(big))
	os.MkdirAll(filepath.Join(root, "build", "deep"), 0o755)
	for _, n := range []string{"build/a", "build/deep/b", "build/deep/c"} {
		writeFile(t, filepath.Join(root, n), "u\n")
	}
	var out struct {
		Status   []string `json:"status"`
		Unknown  string   `json:"unstaged_unknown"`
		DiffStat []string `json:"diff_stat"`
	}
	if err := json.Unmarshal([]byte((&Aria{}).toolGitStatus(root, true, 1)), &out); err != nil {
		t.Fatal(err)
	}
	if out.Unknown == "" {
		t.Fatalf("unreadable modified file reported as clean: %+v", out)
	}
	untracked := 0
	for _, l := range out.Status {
		if strings.HasPrefix(l, "?? ") {
			untracked++
			if l != "?? build/" {
				t.Fatalf("untracked line %q, want ?? build/", l)
			}
		}
	}
	if untracked != 1 {
		t.Fatalf("status = %q, want one collapsed ?? build/", out.Status)
	}
	for _, l := range out.DiffStat {
		if strings.Contains(l, "build/") {
			t.Fatalf("diff_stat counts untracked files: %q", out.DiffStat)
		}
	}
}
