package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testPreviewChannel(t *testing.T, mainJSON string) (*previewChannel, *int) {
	t.Helper()
	spawned := new(int)
	p := newPreviewChannel(t.TempDir())
	p.repo = func() string { return "/src/soluterminal" }
	p.run = func(context.Context, string, ...string) ([]byte, error) { return []byte(mainJSON), nil }
	p.spawn = func(string, string, ...string) (int, error) { *spawned++; return 4242, nil }
	p.alive = func(pid int) bool { return pid == 4242 }
	p.scripts = func(repo string) (string, func()) { return repo, func() {} }
	return p, spawned
}

func waitPreviewChecked(t *testing.T, p *previewChannel) {
	t.Helper()
	for i := 0; i < 200; i++ {
		p.mu.Lock()
		done := !p.checking && p.main.CheckedMs != 0
		p.mu.Unlock()
		if done {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("main check never finished")
}

func TestPreviewChannelUnavailableWithoutSourceCheckout(t *testing.T) {
	p := newPreviewChannel(t.TempDir())
	p.repo = func() string { return "" }
	if st := p.status("0.0.1168", "0.0.1168"); st.Available {
		t.Fatal("a machine without the source checkout must not offer Preview")
	}
}

func TestPreviewChannelCountsChanges(t *testing.T) {
	p, _ := testPreviewChannel(t, `{"mainSha":"abc","mainVersion":"0.0.1168.18","ci":"attested"}`)
	p.status("0.0.1168.15", "0.0.1168")
	waitPreviewChecked(t, p)

	st := p.status("0.0.1168.15", "0.0.1168")
	if st.Channel != "preview" || st.Ahead != 15 || st.Stable != "0.0.1168" || st.NewChanges != 3 {
		t.Fatalf("on preview: %+v", st)
	}
	st = p.status("0.0.1168", "0.0.1168")
	if st.Channel != "stable" || st.NewChanges != 18 {
		t.Fatalf("on stable: %+v", st)
	}
	// Main not yet green in CI: nothing installable is announced.
	p.main.CI = "pending"
	if st := p.status("0.0.1168.15", "0.0.1168"); st.NewChanges != 0 {
		t.Fatalf("pending CI announced new changes: %+v", st)
	}
}

func TestPreviewInstallOneAtATime(t *testing.T) {
	p, spawned := testPreviewChannel(t, `{}`)
	if err := p.install("preview"); err != nil {
		t.Fatal(err)
	}
	if err := p.install("stable"); err != errPreviewBusy {
		t.Fatalf("second switch while one runs: %v", err)
	}
	if err := p.install("nightly"); err == nil {
		t.Fatal("unknown target accepted")
	}
	if *spawned != 1 {
		t.Fatalf("spawned %d builds", *spawned)
	}
	if st := p.status("0.0.1168", "0.0.1168"); st.Job != "building-preview" {
		t.Fatalf("running job not reported: %+v", st)
	}
}

func TestPreviewEndpointsOnlyForTheOwner(t *testing.T) {
	a := &apiServer{gate: &authGate{token: "owner-token"}}
	cases := []struct {
		name   string
		mutate func(*http.Request)
		want   bool
	}{
		{"loopback", func(r *http.Request) { r.RemoteAddr = "127.0.0.1:5000"; r.Host = "127.0.0.1:8787" }, true},
		{"desktop app with owner token", func(r *http.Request) {
			r.RemoteAddr = "172.22.144.1:5000"
			r.Header.Set("Authorization", "Bearer owner-token")
		}, true},
		{"LAN without token", func(r *http.Request) { r.RemoteAddr = "192.168.1.5:5000" }, false},
		{"LAN with wrong token", func(r *http.Request) {
			r.RemoteAddr = "192.168.1.5:5000"
			r.Header.Set("Authorization", "Bearer nope")
		}, false},
		// The cloud forwards only the machine owner's requests (m.UserID ==
		// caller), and the desktop app in cloud mode reaches its daemon this way.
		{"cloud relay (owner-only)", func(r *http.Request) {
			r.RemoteAddr = "yamux:remote"
			r.Header.Set("X-Forwarded-Prefix", "/m/mach_1")
		}, true},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, "/api/update/preview/install", strings.NewReader(`{"target":"preview"}`))
		c.mutate(r)
		if got := a.ownerRequest(r); got != c.want {
			t.Errorf("%s: ownerRequest=%v, want %v", c.name, got, c.want)
		}
	}
}

func TestPreviewChannelListsUnreleasedChangesWithHowToTry(t *testing.T) {
	p, _ := testPreviewChannel(t, "")
	p.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "--changes" {
			return []byte(`{"mainBase":"0.0.1168","changes":[{"n":2,"sha":"b","pr":12,"title":"Show reviews","howToTry":"Run solu-review in a pane","installed":false},{"n":1,"sha":"a","title":"Fix X","installed":true}]}`), nil
		}
		return []byte(`{"mainSha":"b","mainVersion":"0.0.1168.2","ci":"attested"}`), nil
	}
	p.status("0.0.1168.1", "0.0.1168")
	waitPreviewChecked(t, p)
	st := p.status("0.0.1168.1", "0.0.1168")
	if len(st.Unreleased) != 2 || st.Unreleased[0].PR != 12 || st.Unreleased[0].HowToTry == "" || !st.Unreleased[1].Installed {
		t.Fatalf("unreleased list = %+v", st.Unreleased)
	}
	// A failed --changes run leaves the channel working, just without the list.
	p2, _ := testPreviewChannel(t, `{"mainSha":"b","mainVersion":"0.0.1168.2","ci":"attested"}`)
	p2.status("0.0.1168", "0.0.1168")
	waitPreviewChecked(t, p2)
	if st := p2.status("0.0.1168", "0.0.1168"); st.NewChanges != 2 || len(st.Unreleased) != 0 {
		t.Fatalf("status without a change list = %+v", st)
	}
}

func TestPreviewChannelNeedsTheOwnerOptIn(t *testing.T) {
	// A source checkout alone (anyone can clone the public repo) offers nothing.
	t.Setenv(previewOwnerEnv, "")
	if repo := ownerPreviewSourceRepo(); repo != "" {
		t.Fatalf("Preview offered without the owner opt-in: %q", repo)
	}
	p := newPreviewChannel(t.TempDir())
	if st := p.status("0.0.1168", "0.0.1168"); st.Available {
		t.Fatal("a machine that has not opted in must not offer Preview")
	}
	if err := p.install("preview"); err == nil {
		t.Fatal("installing a preview must fail without the owner opt-in")
	}
}

func TestPreviewRunsTheLatestMainScriptsNotTheOwnersCheckout(t *testing.T) {
	root := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(dir, text string) {
		t.Helper()
		_ = os.MkdirAll(filepath.Join(dir, "scripts"), 0o755)
		if err := os.WriteFile(filepath.Join(dir, "scripts", "follow-main.sh"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(dir string) string {
		b, _ := os.ReadFile(filepath.Join(dir, "scripts", "follow-main.sh"))
		return string(b)
	}
	push := func(text string) {
		write(filepath.Join(root, "seed"), text)
		run(filepath.Join(root, "seed"), "add", "-A")
		run(filepath.Join(root, "seed"), "commit", "-qm", text)
		run(filepath.Join(root, "seed"), "push", "-q", "origin", "HEAD:main")
	}
	origin := filepath.Join(root, "origin.git")
	run(root, "init", "-q", "--bare", "-b", "main", origin)
	run(root, "clone", "-q", origin, filepath.Join(root, "seed"))
	push("v2")
	owner := filepath.Join(root, "owner")
	run(root, "clone", "-q", origin, owner)
	run(owner, "checkout", "-q", "-b", "old-feature")
	write(owner, "v1 (an old branch with an old script)")

	p := newPreviewChannel(filepath.Join(root, "data"))
	running := 0
	p.alive = func(pid int) bool { return pid == running }
	v2, releaseV2 := p.scriptRoot(owner)
	if v2 == owner || read(v2) != "v2" {
		t.Fatalf("scripts ran from %s (%q), want a clean origin/main checkout", v2, read(v2))
	}
	// A switch starts from the v2 checkout and keeps running.
	running = 77
	data, _ := json.Marshal(previewJob{Target: "preview", PID: 77, Root: v2})
	if err := os.WriteFile(p.jobPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	releaseV2() // the switch's job record keeps v2 from here
	push("v3")
	v3, releaseV3 := p.scriptRoot(owner) // a slow status check keeps reading v3
	push("v4")
	v4, releaseV4 := p.scriptRoot(owner)
	releaseV4()
	if read(v3) != "v3" || read(v4) != "v4" {
		t.Fatalf("checkouts must follow main: %q %q", read(v3), read(v4))
	}
	if read(v2) != "v2" {
		t.Fatal("the checkout a running switch uses must stay untouched")
	}
	push("v5")
	_, r5 := p.scriptRoot(owner)
	r5()
	push("v6")
	_, r6 := p.scriptRoot(owner)
	r6()
	if read(v3) != "v3" {
		t.Fatal("a checkout a status check is still reading must not be removed")
	}
	releaseV3()
	push("v7")
	_, r7 := p.scriptRoot(owner)
	r7()
	if _, err := os.Stat(v3); err == nil {
		t.Fatalf("once released and old, a checkout is pruned: %s still there", v3)
	}
	if read(v2) != "v2" {
		t.Fatal("the running switch's checkout must stay")
	}
	if read(owner) != "v1 (an old branch with an old script)" {
		t.Fatal("the owner's own checkout must be left alone")
	}
}

func TestPreviewOffersNothingWhenMainIsThePublishedRelease(t *testing.T) {
	p, _ := testPreviewChannel(t, `{"mainSha":"abc","mainVersion":"0.0.1222","ci":"attested","release":true}`)
	p.status("0.0.1221", "0.0.1222")
	waitPreviewChecked(t, p)
	if st := p.status("0.0.1221", "0.0.1222"); st.NewChanges != 0 {
		t.Fatalf("the published release itself was offered as a preview: %+v", st)
	}
}
