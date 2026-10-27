package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// relayFixture lays out a fake home:
//
//	home/h.txt                 home itself is never a derived root
//	home/soludata/notes.txt    the daemon data dir
//	home/proj/.git             git top-level of the pane cwd below
//	home/proj/sub/b.txt        pane cwd (relayPaneDirsForTest)
//	home/proj/a.txt
//	home/proj/link.txt -> home/other/o.txt   symlink escape (file)
//	home/proj/dirlink  -> home/other         symlink escape (dir)
//	home/other/o.txt           outside every root
type relayFixture struct {
	home, data, proj, other string
	srv                     *apiServer
}

func newRelayFixture(t *testing.T) *relayFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &relayFixture{home: filepath.Join(base, "home")}
	f.data = filepath.Join(f.home, "soludata")
	f.proj = filepath.Join(f.home, "proj")
	f.other = filepath.Join(f.home, "other")
	t.Setenv("HOME", f.home)
	t.Setenv("USERPROFILE", f.home)
	t.Setenv(relayFileRootsEnv, "")
	t.Setenv(relayFileConfinementEnv, "")
	for _, d := range []string{f.data, filepath.Join(f.proj, ".git"), filepath.Join(f.proj, "sub"), f.other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(f.home, "h.txt"):         "home\n",
		filepath.Join(f.data, "notes.txt"):     "data\n",
		filepath.Join(f.proj, "a.txt"):         "a\n",
		filepath.Join(f.proj, "sub", "b.txt"):  "b\n",
		filepath.Join(f.other, "o.txt"):        "outside\n",
		filepath.Join(f.other, ".env"):         "TOKEN=x\n",
		filepath.Join(f.proj, "sub", "c.txt"):  "c\n",
		filepath.Join(f.proj, "sub", "d.json"): "{}\n",
	}
	for p, body := range files {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(f.other, "o.txt"), filepath.Join(f.proj, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(f.other, filepath.Join(f.proj, "dirlink")); err != nil {
		t.Fatal(err)
	}
	// home, the data dir and "/" are offered as pane cwds too: all must be
	// dropped. proj/sub contributes its git top-level, proj.
	f.srv = &apiServer{
		dataDir:              f.data,
		relayPaneDirsForTest: []string{filepath.Join(f.proj, "sub"), f.home, f.data, string(filepath.Separator)},
	}
	return f
}

func relayRequest(method, target string, body string, relay bool) *http.Request {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	if relay {
		req.Header.Set("X-Forwarded-Prefix", "/m/mach_1")
	}
	return req
}

func (f *relayFixture) read(path string, relay bool) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.srv.readFile(rec, relayRequest("GET", "/api/file?path="+url.QueryEscape(path), "", relay))
	return rec
}

func TestRelayFileReadConfinedToRoots(t *testing.T) {
	f := newRelayFixture(t)
	sep := string(filepath.Separator)
	cases := []struct {
		name  string
		path  string
		relay bool
		want  int
	}{
		{"inside git top-level root", filepath.Join(f.proj, "a.txt"), true, 200},
		{"inside pane cwd", filepath.Join(f.proj, "sub", "b.txt"), true, 200},
		{"outside roots", filepath.Join(f.other, "o.txt"), true, 403},
		{"dotdot escape", f.proj + sep + "sub" + sep + ".." + sep + ".." + sep + "other" + sep + "o.txt", true, 403},
		{"symlinked file escape", filepath.Join(f.proj, "link.txt"), true, 403},
		{"symlinked dir escape", filepath.Join(f.proj, "dirlink", "o.txt"), true, 403},
		{"home dir file", filepath.Join(f.home, "h.txt"), true, 403},
		{"data dir file", filepath.Join(f.data, "notes.txt"), true, 403},
		{"missing file outside", filepath.Join(f.other, "missing.txt"), true, 403},
		{"local outside unaffected", filepath.Join(f.other, "o.txt"), false, 200},
		{"local home unaffected", filepath.Join(f.home, "h.txt"), false, 200},
		{"local symlink unaffected", filepath.Join(f.proj, "link.txt"), false, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.read(tc.path, tc.relay)
			if rec.Code != tc.want {
				t.Fatalf("code = %d, want %d (body %q)", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == 403 && !strings.Contains(rec.Body.String(), relayOutsideRootsRefusal) {
				t.Fatalf("403 body = %q, want the relay-roots refusal", rec.Body.String())
			}
		})
	}
}

func TestRelayFileConfinementEscapeHatch(t *testing.T) {
	f := newRelayFixture(t)
	t.Setenv(relayFileConfinementEnv, "off")
	if rec := f.read(filepath.Join(f.other, "o.txt"), true); rec.Code != 200 {
		t.Fatalf("confinement off: code = %d, want 200 (%q)", rec.Code, rec.Body.String())
	}
	// The deny-list still applies with confinement off.
	if rec := f.read(filepath.Join(f.other, ".env"), true); rec.Code != 403 {
		t.Fatalf("confinement off .env: code = %d, want 403", rec.Code)
	}
}

func TestRelayFileRootsEnv(t *testing.T) {
	f := newRelayFixture(t)
	t.Setenv(relayFileRootsEnv, f.home+string(os.PathListSeparator)+string(filepath.Separator)+string(os.PathListSeparator)+"relative/dir")
	if rec := f.read(filepath.Join(f.home, "h.txt"), true); rec.Code != 200 {
		t.Fatalf("explicit home root: code = %d, want 200 (%q)", rec.Code, rec.Body.String())
	}
	// The data dir stays unreachable even under an explicit home root.
	if rec := f.read(filepath.Join(f.data, "notes.txt"), true); rec.Code != 403 {
		t.Fatalf("data dir under explicit home: code = %d, want 403", rec.Code)
	}
	for _, root := range f.srv.relayFileScope().roots {
		if filepath.Dir(root) == root {
			t.Fatalf("filesystem root accepted as a relay root: %v", f.srv.relayFileScope().roots)
		}
	}
}

func TestBuildRelayFileScopeRules(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	data := filepath.Join(home, ".solu")
	proj := filepath.Join(home, "proj")
	for _, d := range []string{filepath.Join(data, "workflow", "wt"), proj} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(proj, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	derived := []string{home, base, string(filepath.Separator), data, filepath.Join(data, "workflow", "wt"), alias, filepath.Join(base, "missing")}
	s := buildRelayFileScope(derived, []string{proj}, nil, home, data)
	if len(s.roots) != 1 || s.roots[0] != proj {
		t.Fatalf("roots = %v, want only the symlink-resolved project %q", s.roots, proj)
	}
	s = buildRelayFileScope(nil, nil, []string{home, data}, home, data)
	if len(s.roots) != 1 || s.roots[0] != home {
		t.Fatalf("explicit roots = %v, want home only (data dir refused)", s.roots)
	}
}

func TestRelayWriteFileLineConfined(t *testing.T) {
	f := newRelayFixture(t)
	write := func(path string) int {
		body := `{"path":` + strconv.Quote(path) + `,"lineNo":0,"newText":"changed","expectedMtime":0}`
		rec := httptest.NewRecorder()
		f.srv.writeFileLine(rec, relayRequest("POST", "/api/file/line", body, true))
		return rec.Code
	}
	outside := filepath.Join(f.other, "o.txt")
	if code := write(outside); code != 403 {
		t.Fatalf("relay write outside roots: code = %d, want 403", code)
	}
	if data, _ := os.ReadFile(outside); string(data) != "outside\n" {
		t.Fatalf("outside file modified: %q", data)
	}
	if code := write(filepath.Join(f.proj, "link.txt")); code != 403 {
		t.Fatalf("relay write through symlink escape: code = %d, want 403", code)
	}
	if code := write(filepath.Join(f.proj, "a.txt")); code != 200 {
		t.Fatalf("relay write inside root: code = %d, want 200", code)
	}
}

func (f *relayFixture) list(t *testing.T, path string, relay bool) (int, []string) {
	t.Helper()
	rec := httptest.NewRecorder()
	f.srv.listDirContents(rec, relayRequest("GET", "/api/dir/list?path="+url.QueryEscape(path), "", relay))
	if rec.Code != 200 {
		return rec.Code, nil
	}
	var resp dirListResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range resp.Entries {
		names = append(names, e.Name)
	}
	return rec.Code, names
}

func TestRelayDirListConfined(t *testing.T) {
	f := newRelayFixture(t)
	// home is an ancestor of a root: listable, but only the way to the root.
	if code, names := f.list(t, f.home, true); code != 200 || strings.Join(names, ",") != "proj" {
		t.Fatalf("relay list home = %d %v, want 200 [proj]", code, names)
	}
	code, names := f.list(t, f.proj, true)
	joined := "," + strings.Join(names, ",") + ","
	if code != 200 || !strings.Contains(joined, ",a.txt,") || !strings.Contains(joined, ",sub,") {
		t.Fatalf("relay list proj = %d %v", code, names)
	}
	if strings.Contains(joined, ",link.txt,") || strings.Contains(joined, ",dirlink,") {
		t.Fatalf("relay list proj shows symlink escapes: %v", names)
	}
	for _, p := range []string{f.other, filepath.Join(f.proj, "dirlink"), f.data} {
		if code, _ := f.list(t, p, true); code != 403 {
			t.Fatalf("relay list %s = %d, want 403", p, code)
		}
	}
	if code, names := f.list(t, f.home, false); code != 200 || len(names) < 3 {
		t.Fatalf("local list home = %d %v, want full listing", code, names)
	}
}

func TestRelaySearchFilesConfined(t *testing.T) {
	f := newRelayFixture(t)
	search := func(root string) (int, fileSearchResp) {
		rec := httptest.NewRecorder()
		f.srv.searchFiles(rec, relayRequest("GET", "/api/files/search?q=&kind=all&root="+url.QueryEscape(root), "", true))
		var resp fileSearchResp
		if rec.Code == 200 {
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, resp
	}
	for _, root := range []string{f.other, f.home, filepath.Join(f.proj, "dirlink")} {
		if code, _ := search(root); code != 403 {
			t.Fatalf("relay search root %s = %d, want 403", root, code)
		}
	}
	code, resp := search(f.proj)
	if code != 200 || len(resp.Hits) == 0 {
		t.Fatalf("relay search proj = %d %#v", code, resp)
	}
	for _, h := range resp.Hits {
		if h.Name == "link.txt" || h.Name == "dirlink" {
			t.Fatalf("relay search surfaced a symlink escape: %#v", h)
		}
	}
}

func TestRelayGitDiffConfined(t *testing.T) {
	f := newRelayFixture(t)
	repo := gitInit(t)
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	diff := func(srv *apiServer, path string) int {
		rec := httptest.NewRecorder()
		srv.gitDiff(rec, relayRequest("GET", "/api/git/diff?path="+url.QueryEscape(path), "", true))
		return rec.Code
	}
	if code := diff(f.srv, repo); code != 403 {
		t.Fatalf("relay diff of repo outside roots = %d, want 403", code)
	}
	// An explicit root nested inside a repo must not expose the whole repo.
	t.Setenv(relayFileRootsEnv, sub)
	if code := diff(f.srv, sub); code != 403 {
		t.Fatalf("relay diff of nested root with repo outside = %d, want 403", code)
	}
	// A pane cwd inside the repo brings in its top-level.
	paneSrv := &apiServer{dataDir: f.data, relayPaneDirsForTest: []string{sub}}
	if code := diff(paneSrv, sub); code != 200 {
		t.Fatalf("relay diff from pane cwd = %d, want 200", code)
	}
}
