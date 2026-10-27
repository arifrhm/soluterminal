package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Regression tests for the second adversarial review of the turn / working
// tree diff hardening. Each one reproduced a leak or bypass before its fix.

func hardeningRepo(t *testing.T) (string, func(args ...string)) {
	t.Helper()
	repo := gitInit(t)
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return repo, git
}

func relayGitDiff(t *testing.T, dir string, staged bool) gitDiffResp {
	t.Helper()
	q := "/api/git/diff?path=" + url.QueryEscape(dir)
	if staged {
		q += "&staged=1"
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", q, nil)
	req.Header.Set("X-Forwarded-Prefix", "/m/mach_1")
	(&apiServer{dataDir: t.TempDir(), relayPaneDirsForTest: []string{dir}}).gitDiff(rec, req)
	if rec.Code != 200 {
		t.Fatalf("git diff code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp gitDiffResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// F1: the file list was read through a 20 000-character runner, so files past
// the cut were never flagged — yet the patch text (512 KB cap) still carried
// their content over the relay.
func TestGitDiffLongFileListNeverLeaksSecretsOverRelay(t *testing.T) {
	repo, git := hardeningRepo(t)
	long := strings.Repeat("x", 60)
	for i := 0; i < 400; i++ {
		if err := os.WriteFile(filepath.Join(repo, fmt.Sprintf("%s-%03d.txt", long, i)), []byte("v1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "z.env"), []byte("A=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-qm", "many")
	for i := 0; i < 400; i++ {
		os.WriteFile(filepath.Join(repo, fmt.Sprintf("%s-%03d.txt", long, i)), []byte("v2\n"), 0o644)
	}
	const secret = "ZENV-SECRET-91f2"
	os.WriteFile(filepath.Join(repo, "z.env"), []byte("A="+secret+"\n"), 0o644)

	resp := relayGitDiff(t, repo, false)
	if strings.Contains(resp.Diff, secret) {
		t.Fatal("secret from a file past the old 20k list cut leaked over the relay")
	}
	if len(resp.Files) != 401 || resp.HiddenSecret < 1 {
		t.Fatalf("want all 401 files listed and z.env hidden, got files=%d hidden=%d", len(resp.Files), resp.HiddenSecret)
	}
}

// F2: an untracked symlink was read through (os.Stat + os.ReadFile), showing
// the target's content — e.g. ~/.ssh keys — in the diff.
func TestGitDiffUntrackedSymlinkShowsLinkNotTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	repo, _ := hardeningRepo(t)
	outside := filepath.Join(t.TempDir(), "id_dummy")
	const secret = "OUTSIDE-KEY-77ab"
	os.WriteFile(outside, []byte(secret+"\n"), 0o600)
	if err := os.Symlink(outside, filepath.Join(repo, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	for _, staged := range []bool{false, true} {
		if resp := relayGitDiff(t, repo, staged); strings.Contains(resp.Diff, secret) {
			t.Fatalf("symlink target content leaked (staged=%v): %q", staged, resp.Diff)
		}
	}
}

// F2 (FIFO): an untracked named pipe made the endpoint block forever.
func TestGitDiffUntrackedFifoDoesNotHang(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fifo")
	}
	repo, _ := hardeningRepo(t)
	if err := exec.Command("mkfifo", filepath.Join(repo, "pipe")).Run(); err != nil {
		t.Skip("mkfifo unavailable")
	}
	done := make(chan struct{})
	go func() { relayGitDiff(t, repo, false); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("working-tree diff hung on an untracked FIFO")
	}
}

// F3: `promisor=yes` is a promisor remote to git but passed the "true" text
// check; and repo-level protocol.<name>.allow=always overrode
// `-c protocol.allow=never`.
func TestTurnGitRefusesAllPromisorSpellingsAndTransports(t *testing.T) {
	repo, git := hardeningRepo(t)
	git("config", "remote.origin.promisor", "yes")
	if err := turnRepoRefusal(context.Background(), repo); err == nil {
		t.Fatal("promisor=yes not refused")
	}
	git("config", "--unset", "remote.origin.promisor")

	src, srcGit := hardeningRepo(t)
	os.WriteFile(filepath.Join(src, "f"), []byte("x\n"), 0o644)
	srcGit("add", "f")
	srcGit("commit", "-qm", "x")
	git("config", "protocol.file.allow", "always")
	git("config", "protocol.allow", "always")
	if _, err := turnGit(context.Background(), repo, nil, nil, "fetch", "file://"+src); err == nil {
		t.Fatal("a transport ran through turnGit despite GIT_ALLOW_PROTOCOL")
	}
}

// F4: /api/git/diff ran plain `git diff`, honouring the repository's
// diff.external, textconv drivers, clean filters and fsmonitor hook — all
// executed as the daemon, outside the agent sandbox.
func TestGitDiffNeverRunsRepoConfiguredPrograms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	repo, git := hardeningRepo(t)
	sentinel := filepath.Join(t.TempDir(), "ran")
	prog := filepath.Join(t.TempDir(), "prog.sh")
	os.WriteFile(prog, []byte("#!/bin/sh\necho ran >> '"+sentinel+"'\ncat\n"), 0o755)
	os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.txt diff=evil filter=evil\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644)
	git("add", "-A")
	git("commit", "-qm", "x")
	for _, kv := range [][2]string{
		{"diff.external", prog},
		{"diff.evil.textconv", prog},
		{"diff.evil.command", prog},
		{"filter.evil.clean", prog},
		{"core.fsmonitor", prog},
	} {
		git("config", kv[0], kv[1])
	}
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("two\n"), 0o644)
	// Make a.txt stat-dirty but staged too, so both views have work to do.
	for _, staged := range []bool{false, true} {
		resp := relayGitDiff(t, repo, staged)
		if !staged && !strings.Contains(resp.Diff, "+two") {
			t.Fatalf("unstaged diff should show the change: %q", resp.Diff)
		}
	}
	if b, err := os.ReadFile(sentinel); err == nil {
		t.Fatalf("a repository-configured program ran: %q", b)
	}
}

// F5: cappedBuffer embedded bytes.Buffer, so io.Copy used the promoted
// ReadFrom and never hit the capped Write.
func TestTurnGitOutputCapIsEnforced(t *testing.T) {
	repo, _ := hardeningRepo(t)
	big := filepath.Join(repo, "big.bin")
	os.WriteFile(big, []byte(strings.Repeat("y", 5<<20)), 0o644)
	sha, err := turnGit(context.Background(), repo, nil, nil, "hash-object", "-w", "--no-filters", big)
	if err != nil {
		t.Fatal(err)
	}
	out, overflow, err := turnGitLimited(context.Background(), repo, nil, nil, 1000, "cat-file", "-p", strings.TrimSpace(sha))
	if err != nil {
		t.Fatal(err)
	}
	if !overflow || len(out) > 1000 {
		t.Fatalf("cap not enforced: overflow=%v held=%d bytes", overflow, len(out))
	}
}

// F7: core.worktree could point the snapshot at a different repository.
func TestGitTurnSnapshotRefusesRedirectedWorktree(t *testing.T) {
	repo, git := hardeningRepo(t)
	other, _ := hardeningRepo(t)
	git("config", "core.worktree", other)
	if _, err := (gitTurnSnapshotter{}).snapshot(context.Background(), repo); err == nil {
		t.Fatal("snapshot followed core.worktree into another repository")
	}
}

// F8: oddly named (but valid) files must still snapshot; only structurally
// unsafe paths are dropped, and never fail the snapshot.
func TestGitTurnSnapshotOddNamesAndUnsafePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tab in file name")
	}
	repo, git := hardeningRepo(t)
	odd := "tab\there \"q\".txt"
	os.WriteFile(filepath.Join(repo, odd), []byte("v1\n"), 0o644)
	git("add", "-A")
	git("commit", "-qm", "odd")
	os.WriteFile(filepath.Join(repo, odd), []byte("v2\n"), 0o644)
	s, err := gitTurnSnapshotter{}.snapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := exec.Command("git", "-C", repo, "cat-file", "-p", s.tree+":"+odd).Output()
	if err != nil || string(blob) != "v2\n" {
		t.Fatalf("odd-named file not captured: %q %v", blob, err)
	}
	for _, bad := range []string{"../x", "a/../b", "/abs", "a//b", ".git/config", "a/./b"} {
		if turnSafeRel(bad) {
			t.Errorf("turnSafeRel(%q) = true", bad)
		}
	}
}

// Final review (H1): diff.submodule=diff makes git run a nested `git diff`
// inside the submodule, which honours the SUBMODULE's own config
// (diff.external) without our --no-ext-diff — executing it as the daemon.
func TestGitDiffNeverRunsSubmoduleConfiguredPrograms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	sub, subGit := hardeningRepo(t)
	os.WriteFile(filepath.Join(sub, "f"), []byte("1\n"), 0o644)
	subGit("add", "f")
	subGit("commit", "-qm", "c1")
	c1, _ := exec.Command("git", "-C", sub, "rev-parse", "HEAD").Output()
	os.WriteFile(filepath.Join(sub, "f"), []byte("2\n"), 0o644)
	subGit("commit", "-qam", "c2")
	c2, _ := exec.Command("git", "-C", sub, "rev-parse", "HEAD").Output()

	repo, git := hardeningRepo(t)
	if out, err := exec.Command("cp", "-r", sub, filepath.Join(repo, "sub")).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	git("update-index", "--add", "--cacheinfo", "160000,"+strings.TrimSpace(string(c1))+",sub")
	git("commit", "-qm", "add submodule")
	git("update-index", "--cacheinfo", "160000,"+strings.TrimSpace(string(c2))+",sub")
	git("config", "diff.submodule", "diff")

	sentinel := filepath.Join(t.TempDir(), "ran")
	prog := filepath.Join(t.TempDir(), "evil.sh")
	os.WriteFile(prog, []byte("#!/bin/sh\necho ran >> '"+sentinel+"'\n"), 0o755)
	if out, err := exec.Command("git", "-C", filepath.Join(repo, "sub"), "config", "diff.external", prog).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	relayGitDiff(t, repo, true) // staged: HEAD vs index moves the submodule commit
	if b, err := os.ReadFile(sentinel); err == nil {
		t.Fatalf("a submodule-configured program ran: %q", b)
	}
}

// Final review (L2): a tracked path containing a newline was split apart by
// the index parser and showed as deleted.
func TestGitTurnSnapshotKeepsNewlineNamedFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("newline in file name")
	}
	repo, git := hardeningRepo(t)
	name := "a\nb.txt"
	os.WriteFile(filepath.Join(repo, name), []byte("v1\n"), 0o644)
	git("add", "-A")
	git("commit", "-qm", "nl")
	s, err := gitTurnSnapshotter{}.snapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command("git", "-C", repo, "ls-tree", "-z", "--name-only", s.tree).Output()
	if !strings.Contains(string(out), name+"\x00") {
		t.Fatalf("newline-named file missing from snapshot: %q", out)
	}
}

// Final review (L1): core.worktree pointing at an ANCESTOR repository (e.g. a
// dotfiles ~/.git) passed the "toplevel contains dir" check.
func TestGitTurnSnapshotRefusesAncestorRepoRedirect(t *testing.T) {
	outer, _ := hardeningRepo(t)
	inner := filepath.Join(outer, "proj")
	if out, err := exec.Command("git", "init", "-q", inner).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if out, err := exec.Command("git", "-C", inner, "config", "core.worktree", outer).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if _, err := (gitTurnSnapshotter{}).snapshot(context.Background(), inner); err == nil {
		t.Fatal("snapshot followed core.worktree up into an ancestor repository")
	}
}

// Final review (M1): the "hidden from this remote diff" count must not
// appear on local views (secret-looking untracked files are reported as
// skipped instead).
func TestGitDiffLocalViewHasNoRelaySecretCount(t *testing.T) {
	repo, git := hardeningRepo(t)
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("1\n"), 0o644)
	git("add", "a.txt")
	git("commit", "-qm", "a")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("2\n"), 0o644)
	os.WriteFile(filepath.Join(repo, ".env"), []byte("K=v\n"), 0o600)
	rec := httptest.NewRecorder()
	(&apiServer{dataDir: t.TempDir()}).gitDiff(rec, httptest.NewRequest("GET", "/api/git/diff?path="+url.QueryEscape(repo), nil))
	var resp gitDiffResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.HiddenSecret != 0 || resp.SkippedUntracked < 1 {
		t.Fatalf("local view: want hidden=0 and skipped>=1, got hidden=%d skipped=%d", resp.HiddenSecret, resp.SkippedUntracked)
	}
	if relay := relayGitDiff(t, repo, false); relay.HiddenSecret < 1 {
		t.Fatalf("relay view should count the hidden secret, got %d", relay.HiddenSecret)
	}
}
