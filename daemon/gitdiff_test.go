package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseGitDiffFiles(t *testing.T) {
	t.Parallel()
	num := "3\t1\tsrc/main.go\n5\t0\t.env\n-\t-\tlogo.png\n"
	stat := "M\tsrc/main.go\nA\t.env\nA\tlogo.png\n"

	// Local: nothing hidden, counts parsed, binary marked -1.
	files, secret, hidden := parseGitDiffFiles("/repo", num, stat, false)
	if len(files) != 3 || hidden != 0 || len(secret) != 0 {
		t.Fatalf("local: files=%d hidden=%d secret=%v", len(files), hidden, secret)
	}
	if files[0].Path != "src/main.go" || files[0].Status != "M" || files[0].Added != 3 || files[0].Removed != 1 {
		t.Fatalf("local main.go parse = %#v", files[0])
	}
	if files[2].Added != -1 || files[2].Removed != -1 {
		t.Fatalf("binary file should have -1 counts, got %#v", files[2])
	}

	// Relay: .env flagged secret + returned for exclusion.
	files, secret, hidden = parseGitDiffFiles("/repo", num, stat, true)
	if hidden != 1 || len(secret) != 1 || secret[0] != ".env" {
		t.Fatalf("relay: hidden=%d secret=%v", hidden, secret)
	}
	var envSecret bool
	for _, f := range files {
		if f.Path == ".env" {
			envSecret = f.Secret
		}
	}
	if !envSecret {
		t.Fatalf("env file not flagged secret over relay: %#v", files)
	}
}

func TestGitDiffEndpointEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("init", "-q")
	write("a.txt", "one\ntwo\n")
	write(".env", "TOKEN=originalsecret\n")
	run("add", "a.txt", ".env")
	run("commit", "-q", "-m", "init")
	// Modify both tracked files -> unstaged changes.
	write("a.txt", "one\nTWO\n")
	write(".env", "TOKEN=rotatedsecret\n")

	srv := &apiServer{relayPaneDirsForTest: []string{dir}}

	// Local: full diff, both files present, secret value visible locally.
	rec := httptest.NewRecorder()
	srv.gitDiff(rec, httptest.NewRequest("GET", "/api/git/diff?path="+url.QueryEscape(dir), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("local gitDiff code=%d body=%q", rec.Code, rec.Body.String())
	}
	var local gitDiffResp
	if err := json.Unmarshal(rec.Body.Bytes(), &local); err != nil {
		t.Fatalf("bad json: %v (%q)", err, rec.Body.String())
	}
	if len(local.Files) != 2 || local.HiddenSecret != 0 {
		t.Fatalf("local files=%#v hidden=%d", local.Files, local.HiddenSecret)
	}
	if !strings.Contains(local.Diff, "+TWO") || !strings.Contains(local.Diff, "rotatedsecret") {
		t.Fatalf("local diff missing expected content: %q", local.Diff)
	}

	// Over relay: .env flagged + excluded; its value must NOT appear in the diff.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/git/diff?path="+url.QueryEscape(dir), nil)
	req.Header.Set("X-Forwarded-Prefix", "/m/mach_1")
	srv.gitDiff(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("relay gitDiff code=%d body=%q", rec.Code, rec.Body.String())
	}
	var relay gitDiffResp
	if err := json.Unmarshal(rec.Body.Bytes(), &relay); err != nil {
		t.Fatalf("bad json: %v (%q)", err, rec.Body.String())
	}
	if relay.HiddenSecret != 1 {
		t.Fatalf("relay hiddenSecret = %d, want 1 (files=%#v)", relay.HiddenSecret, relay.Files)
	}
	if strings.Contains(relay.Diff, "rotatedsecret") || strings.Contains(relay.Diff, "originalsecret") {
		t.Fatalf("relay diff leaked a secret value: %q", relay.Diff)
	}
	if !strings.Contains(relay.Diff, "+TWO") {
		t.Fatalf("relay diff should still include non-secret file changes: %q", relay.Diff)
	}
	var envFlagged bool
	for _, f := range relay.Files {
		if f.Path == ".env" {
			envFlagged = f.Secret
		}
	}
	if !envFlagged {
		t.Fatalf(".env not flagged secret over relay: %#v", relay.Files)
	}
}

func TestGitDiffIncludesUntrackedFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	git("init", "-q")
	write("tracked.txt", "one\n")
	git("add", "tracked.txt")
	git("commit", "-q", "-m", "init")
	// A brand-new untracked file (what an agent creates) + an untracked secret
	// + a gitignored file that must NOT surface.
	write("new_file.go", "package main\n\nfunc main() {}\n")
	write(".env", "TOKEN=untracked-secret\n")
	write(".gitignore", "ignored.log\n")
	write("ignored.log", "noise\n")

	srv := &apiServer{relayPaneDirsForTest: []string{dir}}

	// Local: new_file.go appears as an added file in the list and the diff
	// body; the gitignored file does not.
	rec := httptest.NewRecorder()
	srv.gitDiff(rec, httptest.NewRequest("GET", "/api/git/diff?path="+url.QueryEscape(dir), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("local code=%d body=%q", rec.Code, rec.Body.String())
	}
	var local gitDiffResp
	if err := json.Unmarshal(rec.Body.Bytes(), &local); err != nil {
		t.Fatal(err)
	}
	paths := map[string]gitDiffFile{}
	for _, f := range local.Files {
		paths[f.Path] = f
	}
	if _, ok := paths["new_file.go"]; !ok {
		t.Fatalf("untracked new_file.go missing from files: %#v", local.Files)
	}
	if paths["new_file.go"].Status != "A" || paths["new_file.go"].Added <= 0 {
		t.Fatalf("new_file.go should be an addition with line count: %#v", paths["new_file.go"])
	}
	if _, ok := paths["ignored.log"]; ok {
		t.Fatalf("gitignored file should not appear: %#v", local.Files)
	}
	if !strings.Contains(local.Diff, "+func main() {}") {
		t.Fatalf("diff body should include new file content: %q", local.Diff)
	}

	// Over relay: the untracked .env is excluded + counted, new_file.go stays.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/git/diff?path="+url.QueryEscape(dir), nil)
	req.Header.Set("X-Forwarded-Prefix", "/m/mach_1")
	srv.gitDiff(rec, req)
	var relay gitDiffResp
	if err := json.Unmarshal(rec.Body.Bytes(), &relay); err != nil {
		t.Fatal(err)
	}
	if relay.HiddenSecret < 1 {
		t.Fatalf("untracked .env should count toward hiddenSecret over relay: %#v", relay)
	}
	if strings.Contains(relay.Diff, "untracked-secret") {
		t.Fatalf("relay diff leaked untracked secret: %q", relay.Diff)
	}
	var sawNew bool
	for _, f := range relay.Files {
		if f.Path == "new_file.go" {
			sawNew = true
		}
		if f.Path == ".env" {
			t.Fatalf("untracked .env should be excluded from relay file list")
		}
	}
	if !sawNew {
		t.Fatalf("new_file.go should still appear over relay: %#v", relay.Files)
	}
}
