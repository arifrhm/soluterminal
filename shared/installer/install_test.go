package installer

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"soluterminal-shared/releases"
)

// The release signing key, as compiled into the daemon and the cloud.
const releasePubKey = "sm+MMrCMET4jQkfdIHySZ7RJWHLSV22qcBqKUVeU5k4="

const testCloud = "https://cloud.example.test"

func TestScriptEmbedsReleaseKeyAndRepos(t *testing.T) {
	if got := PublicKey(); got != releasePubKey {
		t.Fatalf("install.sh public key = %q, want %q", got, releasePubKey)
	}
	if got, want := Repos(), strings.Join(releases.Repos(), " "); got != want {
		t.Fatalf("install.sh repos = %q, want %q (shared/releases)", got, want)
	}
}

// curl honours Retry-After (the mirror sends up to an hour on 429), so
// every retrying download must cap its total retry time or a rate-limited
// install hangs silently.
func TestScriptCapsCurlRetryTime(t *testing.T) {
	for i, line := range strings.Split(string(Script), "\n") {
		if strings.Contains(line, "curl ") && strings.Contains(line, "--retry ") && !strings.Contains(line, "--retry-max-time ") {
			t.Fatalf("install.sh:%d retries without --retry-max-time: %s", i+1, strings.TrimSpace(line))
		}
	}
}

func TestScriptPassesShellcheck(t *testing.T) {
	sc, err := exec.LookPath("shellcheck")
	if err != nil {
		t.Skip("shellcheck not installed")
	}
	out, err := exec.Command(sc, "--shell=sh", "install.sh").CombinedOutput()
	if err != nil {
		t.Fatalf("shellcheck: %v\n%s", err, out)
	}
}

// The embedded pure-Python Ed25519 verifier is our own crypto code, so check
// it differentially against Go's crypto/ed25519: valid signatures, edited
// messages, other keys, flipped bits and the S+L malleability case.
func TestEmbeddedPythonVerifierAgreesWithGo(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	const start = "SOLU_PYTHON_VERIFY='\n"
	i := bytes.Index(Script, []byte(start))
	if i < 0 {
		t.Fatal("SOLU_PYTHON_VERIFY not found in install.sh")
	}
	body := Script[i+len(start):]
	code := string(body[:bytes.Index(body, []byte("\n'\n"))])
	dir := t.TempDir()
	order := new(big.Int)
	order.SetString("7237005577332262213973186563042994240857116359379907606001950938285454250989", 10)
	check := func(name string, pub ed25519.PublicKey, msg, sig []byte) {
		t.Helper()
		want := ed25519.Verify(pub, msg, sig)
		msgPath := filepath.Join(dir, "msg")
		sigPath := filepath.Join(dir, "sig")
		if err := os.WriteFile(msgPath, msg, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sigPath, []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := exec.Command(python, "-I", "-c", code, base64.StdEncoding.EncodeToString(pub), msgPath, sigPath).Run()
		if got := err == nil; got != want {
			t.Fatalf("%s: python verifier = %v, Go = %v (err %v)", name, got, want, err)
		}
	}
	for n := 0; n < 12; n++ {
		pub, priv, _ := ed25519.GenerateKey(nil)
		msg := []byte(fmt.Sprintf("manifest %d %s", n, strings.Repeat("x", n*37)))
		sig := ed25519.Sign(priv, msg)
		check("valid", pub, msg, sig)
		check("edited message", pub, append(append([]byte{}, msg...), '!'), sig)
		otherPub, _, _ := ed25519.GenerateKey(nil)
		check("other key", otherPub, msg, sig)
		flipped := append([]byte{}, sig...)
		flipped[n*5%64] ^= 0x10
		check("flipped bit", pub, msg, flipped)
		// S + L is the same scalar mod L; Go (and RFC 8032) reject it.
		s := new(big.Int).SetBytes(reverse(sig[32:]))
		s.Add(s, order)
		malleable := append(append([]byte{}, sig[:32]...), reverse(leftPad(s.Bytes(), 32))...)
		check("S+L", pub, msg, malleable)
	}
}

func reverse(b []byte) []byte {
	out := make([]byte, len(b))
	for i := range b {
		out[len(b)-1-i] = b[i]
	}
	return out
}

func leftPad(b []byte, n int) []byte {
	return append(make([]byte, n-len(b)), b...)
}

func hostPlatform(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("install.sh tests need linux or darwin, have %s", runtime.GOOS)
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skipf("install.sh tests need amd64 or arm64, have %s", runtime.GOARCH)
	}
	return runtime.GOOS + "-" + runtime.GOARCH
}

// verifiers reports which real Ed25519 verifiers this host can run. A tool
// is listed only when it passes the same known-answer test the script uses,
// so a host with LibreSSL or OpenSSL 1.1 simply skips the openssl cases.
func verifiers(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, v := range []string{"openssl", "node", "python3"} {
		if _, err := exec.LookPath(v); err != nil {
			continue
		}
		env := newEnv(t, testKey(t))
		env.writeRelease(env.repo(), "1.0.0", releaseOpts{})
		res := env.run(runOpts{verifier: v, args: []string{"--version", "1.0.0"}})
		if res.err == nil {
			out = append(out, v)
		} else if !strings.Contains(res.output, "cannot verify the release signature") {
			t.Fatalf("probing %s: %v\n%s", v, res.err, res.output)
		}
	}
	if len(out) == 0 {
		t.Skip("no Ed25519 verifier (OpenSSL 3, node, python3) on this host")
	}
	return out
}

type keypair struct {
	pub  string
	priv ed25519.PrivateKey
}

func testKey(t *testing.T) keypair {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return keypair{pub: base64.StdEncoding.EncodeToString(pub), priv: priv}
}

type env struct {
	t        *testing.T
	key      keypair
	root     string // fixture tree: <root>/<host>/<path>
	home     string
	fakeBin  string
	toolDir  string
	curlLog  string
	script   []byte
	platform string
}

// newEnv builds a sandbox whose curl serves files from a fixture tree and
// whose install.sh trusts key (only the key line differs from install.sh).
func newEnv(t *testing.T, key keypair) *env {
	t.Helper()
	platform := hostPlatform(t)
	dir := t.TempDir()
	e := &env{
		t:        t,
		key:      key,
		root:     filepath.Join(dir, "fixtures"),
		home:     filepath.Join(dir, "home"),
		fakeBin:  filepath.Join(dir, "fake-bin"),
		toolDir:  filepath.Join(dir, "tools"),
		curlLog:  filepath.Join(dir, "curl.log"),
		platform: platform,
	}
	for _, d := range []string{e.root, e.home, e.fakeBin, e.toolDir, filepath.Join(dir, "tmp")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.script = Script
	if key.pub != releasePubKey {
		e.script = bytes.Replace(Script,
			[]byte("SOLU_RELEASE_PUBKEY='"+releasePubKey+"'"),
			[]byte("SOLU_RELEASE_PUBKEY='"+key.pub+"'"), 1)
		if bytes.Equal(e.script, Script) {
			t.Fatal("could not substitute the test key into install.sh")
		}
	}
	writeExec(t, filepath.Join(e.fakeBin, "curl"), `#!/bin/sh
out=""; url=""; wfmt=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out=$2; shift 2; continue ;;
    -w) wfmt=$2; shift 2; continue ;;
    --proto|--connect-timeout|--retry|--retry-max-time) shift 2; continue ;;
    -*) shift; continue ;;
  esac
  url=$1; shift
done
printf '%s\n' "$url" >>"$SOLU_TEST_CURL_LOG"
path=${url#https://}
path=${path#http://}
path=$(printf '%s' "$path" | tr '?' '@')
# Without -w the caller used -f: a missing file is curl's exit 22. With -w
# it reads the status: <path>.status overrides it (e.g. 429).
if [ -f "$SOLU_TEST_FIXTURES/$path.status" ]; then
  [ -n "$wfmt" ] || exit 22
  cat "$SOLU_TEST_FIXTURES/$path.status"; exit 0
fi
if [ ! -f "$SOLU_TEST_FIXTURES/$path" ]; then
  [ -n "$wfmt" ] || exit 22
  printf 404; exit 0
fi
cp "$SOLU_TEST_FIXTURES/$path" "$out"
[ -z "$wfmt" ] || printf 200
`)
	writeExec(t, filepath.Join(e.fakeBin, "systemctl"), "#!/bin/sh\nexit 0\n")
	for _, name := range []string{"uname", "tr", "grep", "sed", "awk", "wc", "sort", "mktemp", "mkdir", "mv", "rm", "cp", "chmod", "cat", "id", "sha256sum", "shasum", "dirname"} {
		if path, err := exec.LookPath(name); err == nil {
			if err := os.Symlink(path, filepath.Join(e.toolDir, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return e
}

func (e *env) repo() string       { return releases.Repo }
func (e *env) legacyRepo() string { return releases.LegacyRepo }

type releaseOpts struct {
	payload     []byte             // binary bytes served (default "solud-<version>")
	signedSHA   string             // sha256 written into the manifest (default: of payload)
	urlOverride string             // platform URL written into the manifest
	signWith    ed25519.PrivateKey // default: env key
	tamper      func([]byte) []byte
	sig         []byte // raw .sig file contents override
	version     string // "version" field written (default: the tag version)
	host        string // "github.com" (default) or the mirror host
	noAlias     bool
	aliasBody   []byte
}

func (e *env) put(rel string, body []byte) {
	e.t.Helper()
	path := filepath.Join(e.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// manifestBody mirrors publish.sh's update.json layout, with notes that
// contain manifest-looking text to exercise the JSON tokenizer.
func manifestBody(version, repo, platform, sha, url string) []byte {
	notes, _ := json.Marshal(`- Fixed "platforms": {"` + platform + `": {"sha256": "` + strings.Repeat("0", 64) + `"}} \ and caf` + "\u00e9" + ` }]`)
	var b strings.Builder
	fmt.Fprintf(&b, "{\n  \"version\": %q,\n  \"notes\": %s,\n", version, notes)
	fmt.Fprintf(&b, "  \"source\": {\"sha\":\"%s\",\"waivers\":[{\"taskId\":\"wft_1\",\"reason\":\"x, y\"}]},\n", strings.Repeat("a", 40))
	b.WriteString("  \"platforms\": {\n")
	keys := []string{"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64", "windows-amd64"}
	for i, key := range keys {
		u := "https://github.com/" + repo + "/releases/download/v" + version + "/solud-" + key
		s := strings.Repeat("b", 64)
		if key == platform {
			u, s = url, sha
		}
		comma := ","
		if i == len(keys)-1 {
			comma = ""
		}
		fmt.Fprintf(&b, "    %q: {\n      \"url\": %q,\n      \"sha256\": %q\n    }%s\n", key, u, s, comma)
	}
	b.WriteString("  },\n  \"shell\": {\n    \"version\": \"0.0.1\",\n    \"platforms\": {\n      \"windows-amd64\": {\n")
	fmt.Fprintf(&b, "        \"url\": \"https://github.com/%s/releases/download/v%s/setup.exe\",\n        \"sha256\": %q\n      }\n    }\n  }\n}\n", repo, version, strings.Repeat("c", 64))
	return []byte(b.String())
}

// writeRelease publishes a release fixture (manifest, signature, binary and
// optionally the latest alias) on GitHub under repo, or on the mirror.
func (e *env) writeRelease(repo, tag string, o releaseOpts) []byte {
	e.t.Helper()
	if o.payload == nil {
		o.payload = []byte("solud-" + tag)
	}
	if o.signedSHA == "" {
		o.signedSHA = sha256Hex(o.payload)
	}
	if o.version == "" {
		o.version = tag
	}
	url := o.urlOverride
	if url == "" {
		url = "https://github.com/" + repo + "/releases/download/v" + tag + "/solud-" + e.platform
	}
	body := manifestBody(o.version, repo, e.platform, o.signedSHA, url)
	priv := o.signWith
	if priv == nil {
		priv = e.key.priv
	}
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body)) + "\n")
	if o.sig != nil {
		sig = o.sig
	}
	served := body
	if o.tamper != nil {
		served = o.tamper(body)
	}
	alias := served
	if o.aliasBody != nil {
		alias = o.aliasBody
	}
	if o.host == "" || o.host == "github.com" {
		base := "github.com/" + repo + "/releases/"
		e.put(base+"download/v"+tag+"/update.json", served)
		e.put(base+"download/v"+tag+"/update.json.sig", sig)
		e.put(base+"download/v"+tag+"/solud-"+e.platform, o.payload)
		if !o.noAlias {
			e.put(base+"latest/download/update.json", alias)
		}
	} else {
		base := o.host + "/install/"
		e.put(base+"update.json@v="+tag, served)
		e.put(base+"update.json.sig@v="+tag, sig)
		e.put(base+"solud-"+e.platform+"@v="+tag, o.payload)
		if !o.noAlias {
			e.put(base+"update.json", alias)
		}
	}
	return o.payload
}

type runOpts struct {
	verifier string   // SOLU_INSTALL_VERIFIER
	args     []string // installer arguments
	shell    string   // default /bin/sh
	tools    map[string]string
	noTools  []string // real verifier tools to leave off PATH
}

type result struct {
	output, dest, marker string
	err                  error
	curls                []string
}

func (e *env) run(o runOpts) result {
	e.t.Helper()
	toolDir := e.toolDir + "-run"
	_ = os.RemoveAll(toolDir)
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	skip := map[string]bool{}
	for _, n := range o.noTools {
		skip[n] = true
	}
	for _, name := range []string{"openssl", "node", "python3"} {
		if skip[name] {
			continue
		}
		if _, fake := o.tools[name]; fake {
			continue
		}
		if path, err := exec.LookPath(name); err == nil {
			if err := os.Symlink(path, filepath.Join(toolDir, name)); err != nil {
				e.t.Fatal(err)
			}
		}
	}
	for name, body := range o.tools {
		writeExec(e.t, filepath.Join(toolDir, name), body)
	}
	_ = os.Remove(e.curlLog)
	shell := o.shell
	if shell == "" {
		shell = "/bin/sh"
	}
	args := append([]string{"-s", "--"}, o.args...)
	if !hasArg(o.args, "--cloud") {
		args = append(args, "--cloud", testCloud)
	}
	cmd := exec.Command(shell, args...)
	cmd.Stdin = bytes.NewReader(e.script)
	cmd.Env = []string{
		"HOME=" + e.home,
		"PATH=" + e.fakeBin + ":" + toolDir + ":" + e.toolDir,
		"TMPDIR=" + filepath.Join(filepath.Dir(e.root), "tmp"),
		"SOLU_TEST_FIXTURES=" + e.root,
		"SOLU_TEST_CURL_LOG=" + e.curlLog,
	}
	if o.verifier != "" {
		cmd.Env = append(cmd.Env, "SOLU_INSTALL_VERIFIER="+o.verifier)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	logged, _ := os.ReadFile(e.curlLog)
	dest := filepath.Join(e.home, ".local", "bin", "solud")
	return result{output: out.String(), dest: dest, marker: dest + ".version", err: err, curls: strings.Fields(string(logged))}
}

func hasArg(args []string, name string) bool {
	for _, a := range args {
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (r result) wantOK(t *testing.T, payload []byte, version string) {
	t.Helper()
	if r.err != nil {
		t.Fatalf("install failed: %v\n%s", r.err, r.output)
	}
	got, err := os.ReadFile(r.dest)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("installed bytes = %q (%v), want %q\n%s", got, err, payload, r.output)
	}
	marker, _ := os.ReadFile(r.marker)
	if strings.TrimSpace(string(marker)) != version {
		t.Fatalf("version marker = %q, want %q", marker, version)
	}
}

func (r result) wantFail(t *testing.T, msg string) {
	t.Helper()
	if r.err == nil {
		t.Fatalf("install succeeded, want failure containing %q\n%s", msg, r.output)
	}
	if !strings.Contains(r.output, msg) {
		t.Fatalf("output lacks %q:\n%s", msg, r.output)
	}
	if _, err := os.Stat(r.dest); err == nil {
		got, _ := os.ReadFile(r.dest)
		if string(got) != "existing-solud" {
			t.Fatalf("failed install replaced solud with %q", got)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(r.dest))
	for _, ent := range entries {
		if strings.HasPrefix(ent.Name(), ".solud-") {
			t.Fatalf("temporary file left behind: %s", ent.Name())
		}
	}
}

func (r result) downloadedBinary() bool {
	for _, u := range r.curls {
		if strings.Contains(u, "/solud-") {
			return true
		}
	}
	return false
}

func TestInstallVerifiesSignedManifest(t *testing.T) {
	for _, v := range verifiers(t) {
		t.Run(v, func(t *testing.T) {
			t.Run("good", func(t *testing.T) {
				e := newEnv(t, testKey(t))
				payload := e.writeRelease(e.repo(), "1.2.3", releaseOpts{})
				r := e.run(runOpts{verifier: v})
				r.wantOK(t, payload, "1.2.3")
				if !strings.Contains(r.output, "verified the signed release manifest for v1.2.3 ("+v+")") {
					t.Fatalf("output does not name the verifier:\n%s", r.output)
				}
				if !strings.Contains(r.output, "solud --claim --relay wss://cloud.example.test/relay") {
					t.Fatalf("relay URL not derived from --cloud:\n%s", r.output)
				}
			})
			t.Run("tampered manifest", func(t *testing.T) {
				// The manifest was signed for the genuine binary; the served
				// copy was then edited to point at an attacker's binary.
				e := newEnv(t, testKey(t))
				genuine := sha256Hex([]byte("solud-1.2.3"))
				evil := []byte("evil")
				e.writeRelease(e.repo(), "1.2.3", releaseOpts{payload: evil, signedSHA: genuine, tamper: func(b []byte) []byte {
					return bytes.Replace(b, []byte(genuine), []byte(sha256Hex(evil)), 1)
				}})
				r := e.run(runOpts{verifier: v})
				r.wantFail(t, "signature verification failed")
				if r.downloadedBinary() {
					t.Fatal("binary was downloaded before the manifest verified")
				}
			})
			t.Run("wrong key", func(t *testing.T) {
				e := newEnv(t, testKey(t))
				e.writeRelease(e.repo(), "1.2.3", releaseOpts{signWith: testKey(t).priv})
				e.run(runOpts{verifier: v}).wantFail(t, "signature verification failed")
			})
			t.Run("sha mismatch", func(t *testing.T) {
				e := newEnv(t, testKey(t))
				e.writeRelease(e.repo(), "1.2.3", releaseOpts{signedSHA: strings.Repeat("d", 64)})
				writeExisting(t, e, "1.0.0")
				r := e.run(runOpts{verifier: v})
				r.wantFail(t, "sha256 mismatch")
				assertExisting(t, r, "1.0.0")
			})
			t.Run("malformed signature", func(t *testing.T) {
				e := newEnv(t, testKey(t))
				e.writeRelease(e.repo(), "1.2.3", releaseOpts{sig: []byte("not base64 !!\n")})
				e.run(runOpts{verifier: v}).wantFail(t, "signature verification failed")
			})
			t.Run("truncated signature", func(t *testing.T) {
				e := newEnv(t, testKey(t))
				e.writeRelease(e.repo(), "1.2.3", releaseOpts{sig: []byte(base64.StdEncoding.EncodeToString(make([]byte, 63)) + "\n")})
				e.run(runOpts{verifier: v}).wantFail(t, "signature verification failed")
			})
			t.Run("real v0.0.1216 manifest with the real key", func(t *testing.T) {
				e := newEnv(t, keypair{pub: releasePubKey})
				base := "github.com/" + e.repo() + "/releases/download/v0.0.1216/"
				for _, name := range []string{"update.json", "update.json.sig"} {
					body, err := os.ReadFile(filepath.Join("testdata", "v0.0.1216", name))
					if err != nil {
						t.Fatal(err)
					}
					e.put(base+name, body)
				}
				e.put(base+"solud-"+e.platform, []byte("not the real binary"))
				r := e.run(runOpts{verifier: v, args: []string{"--version", "0.0.1216"}})
				r.wantFail(t, "sha256 mismatch")
				if !strings.Contains(r.output, "verified the signed release manifest for v0.0.1216") {
					t.Fatalf("real signed manifest did not verify:\n%s", r.output)
				}
			})
		})
	}
}

func writeExisting(t *testing.T, e *env, marker string) {
	t.Helper()
	dir := filepath.Join(e.home, ".local", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "solud"), []byte("existing-solud"), 0o755); err != nil {
		t.Fatal(err)
	}
	if marker != "" {
		if err := os.WriteFile(filepath.Join(dir, "solud.version"), []byte(marker+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func assertExisting(t *testing.T, r result, marker string) {
	t.Helper()
	got, _ := os.ReadFile(r.dest)
	if string(got) != "existing-solud" {
		t.Fatalf("existing solud replaced with %q", got)
	}
	m, _ := os.ReadFile(r.marker)
	if strings.TrimSpace(string(m)) != marker {
		t.Fatalf("version marker changed to %q", m)
	}
}

func TestInstallManifestChecks(t *testing.T) {
	v := verifiers(t)[0]
	t.Run("signed version differs from the resolved one", func(t *testing.T) {
		e := newEnv(t, testKey(t))
		// The v1.2.4 tag holds a correctly signed manifest for v1.2.3 (a
		// replayed older release).
		e.writeRelease(e.repo(), "1.2.4", releaseOpts{version: "1.2.3", aliasBody: []byte(`{"version": "1.2.4"}`)})
		e.run(runOpts{verifier: v}).wantFail(t, "signed manifest is for v1.2.3, expected v1.2.4")
	})
	t.Run("unexpected download URL", func(t *testing.T) {
		e := newEnv(t, testKey(t))
		e.writeRelease(e.repo(), "1.2.3", releaseOpts{urlOverride: "https://evil.example/solud"})
		r := e.run(runOpts{verifier: v})
		r.wantFail(t, "unexpected download URL")
		if r.downloadedBinary() {
			t.Fatal("binary was downloaded from an unexpected URL")
		}
	})
	t.Run("URL from another tag", func(t *testing.T) {
		e := newEnv(t, testKey(t))
		e.writeRelease(e.repo(), "1.2.3", releaseOpts{urlOverride: "https://github.com/" + e.repo() + "/releases/download/v1.0.0/solud-" + e.platform})
		e.run(runOpts{verifier: v}).wantFail(t, "unexpected download URL")
	})
	t.Run("malformed alias version never reaches a URL", func(t *testing.T) {
		e := newEnv(t, testKey(t))
		e.writeRelease(e.repo(), "1.2.3", releaseOpts{aliasBody: []byte(`{"version": "1.2.3/../../x"}`)})
		r := e.run(runOpts{verifier: v})
		r.wantFail(t, "latest release version is malformed")
		for _, u := range r.curls {
			if strings.Contains(u, "..") {
				t.Fatalf("malformed version reached a URL: %s", u)
			}
		}
	})
	t.Run("duplicate platform entry", func(t *testing.T) {
		e := newEnv(t, testKey(t))
		e.writeRelease(e.repo(), "1.2.3", releaseOpts{})
		// Re-sign a manifest that lists the host platform twice.
		base := "github.com/" + e.repo() + "/releases/download/v1.2.3/"
		body, _ := os.ReadFile(filepath.Join(e.root, base, "update.json"))
		entry := fmt.Sprintf("    %q: {", e.platform)
		body = bytes.Replace(body, []byte("  \"platforms\": {\n"), []byte("  \"platforms\": {\n"+entry+"\n      \"url\": \"x\",\n      \"sha256\": \"y\"\n    },\n"), 1)
		e.put(base+"update.json", body)
		e.put(base+"update.json.sig", []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(e.key.priv, body))+"\n"))
		e.run(runOpts{verifier: v}).wantFail(t, "no verified download published")
	})
	t.Run("pinned version skips the latest alias", func(t *testing.T) {
		e := newEnv(t, testKey(t))
		e.writeRelease(e.repo(), "1.2.3", releaseOpts{})
		payload := e.writeRelease(e.repo(), "1.2.2", releaseOpts{noAlias: true})
		r := e.run(runOpts{verifier: v, args: []string{"--version", "v1.2.2"}})
		r.wantOK(t, payload, "1.2.2")
		for _, u := range r.curls {
			if strings.Contains(u, "/latest/") {
				t.Fatalf("pinned install fetched the latest alias: %s", u)
			}
		}
	})
}

func TestInstallSourcesAndFallbacks(t *testing.T) {
	v := verifiers(t)[0]
	t.Run("previous releases owner", func(t *testing.T) {
		if releases.LegacyRepo == "" || releases.LegacyRepo == releases.Repo {
			t.Skip("no legacy releases repo")
		}
		e := newEnv(t, testKey(t))
		payload := e.writeRelease(e.legacyRepo(), "1.2.3", releaseOpts{})
		e.run(runOpts{verifier: v}).wantOK(t, payload, "1.2.3")
	})
	t.Run("cloud mirror when GitHub is unreachable", func(t *testing.T) {
		e := newEnv(t, testKey(t))
		payload := e.writeRelease(e.repo(), "1.2.3", releaseOpts{host: "cloud.example.test"})
		r := e.run(runOpts{verifier: v})
		r.wantOK(t, payload, "1.2.3")
		if !strings.Contains(r.output, "from GitHub; using "+testCloud+" as a mirror") {
			t.Fatalf("mirror use not reported:\n%s", r.output)
		}
	})
	t.Run("tampered mirror is rejected", func(t *testing.T) {
		e := newEnv(t, testKey(t))
		e.writeRelease(e.repo(), "1.2.3", releaseOpts{host: "cloud.example.test", tamper: func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"notes": "`), []byte(`"notes": "x`), 1)
		}})
		e.run(runOpts{verifier: v}).wantFail(t, "signature verification failed")
	})
	t.Run("mirror binary must match the GitHub-signed digest", func(t *testing.T) {
		e := newEnv(t, testKey(t))
		e.writeRelease(e.repo(), "1.2.3", releaseOpts{})
		// GitHub serves the manifest but not the binary; the mirror's binary is wrong.
		_ = os.Remove(filepath.Join(e.root, "github.com", e.repo(), "releases", "download", "v1.2.3", "solud-"+e.platform))
		e.put("cloud.example.test/install/solud-"+e.platform+"@v=1.2.3", []byte("evil"))
		e.run(runOpts{verifier: v}).wantFail(t, "sha256 mismatch")
	})
	t.Run("rate-limited mirror is explained", func(t *testing.T) {
		e := newEnv(t, testKey(t))
		e.put("cloud.example.test/install/update.json.status", []byte("429"))
		r := e.run(runOpts{verifier: v})
		r.wantFail(t, "rate limiting this address")
	})
	t.Run("nothing reachable", func(t *testing.T) {
		e := newEnv(t, testKey(t))
		e.run(runOpts{verifier: v}).wantFail(t, "could not reach the release server")
	})
}

func TestInstallVerifierSelection(t *testing.T) {
	alwaysYes := "#!/bin/sh\necho 'Signature Verified Successfully'\nexit 0\n"
	alwaysNo := "#!/bin/sh\nexit 1\n"
	t.Run("no verifier refuses before any download", func(t *testing.T) {
		e := newEnv(t, testKey(t))
		e.writeRelease(e.repo(), "1.2.3", releaseOpts{})
		r := e.run(runOpts{noTools: []string{"openssl", "node", "python3"}})
		r.wantFail(t, "cannot verify the release signature on this machine")
		if !strings.Contains(r.output, "brew install openssl@3") {
			t.Fatalf("no install guidance:\n%s", r.output)
		}
		if len(r.curls) != 0 {
			t.Fatalf("downloaded before refusing: %v", r.curls)
		}
	})
	t.Run("a verifier that accepts everything fails the known-answer test", func(t *testing.T) {
		e := newEnv(t, testKey(t))
		e.writeRelease(e.repo(), "1.2.3", releaseOpts{})
		r := e.run(runOpts{tools: map[string]string{"openssl": alwaysYes, "node": alwaysYes, "python3": alwaysYes}})
		r.wantFail(t, "cannot verify the release signature on this machine")
	})
	vs := verifiers(t)
	real := vs[len(vs)-1]
	t.Run("broken tools fall through to a working one", func(t *testing.T) {
		e := newEnv(t, testKey(t))
		payload := e.writeRelease(e.repo(), "1.2.3", releaseOpts{})
		fakes := map[string]string{}
		for _, name := range []string{"openssl", "node", "python3"} {
			if name == real {
				break
			}
			fakes[name] = alwaysYes
			if len(fakes)%2 == 0 {
				fakes[name] = alwaysNo
			}
		}
		r := e.run(runOpts{tools: fakes})
		r.wantOK(t, payload, "1.2.3")
		if !strings.Contains(r.output, "("+real+")") {
			t.Fatalf("expected %s to be chosen:\n%s", real, r.output)
		}
	})
	t.Run("unknown SOLU_INSTALL_VERIFIER value", func(t *testing.T) {
		e := newEnv(t, testKey(t))
		e.run(runOpts{verifier: "gpg"}).wantFail(t, "SOLU_INSTALL_VERIFIER may list only")
	})
}

func TestInstallDowngradeGuard(t *testing.T) {
	v := verifiers(t)[0]
	cases := []struct {
		name    string
		marker  string
		args    []string
		wantErr string
	}{
		{name: "upgrade", marker: "1.0.0"},
		{name: "same version", marker: "1.2.3"},
		{name: "newer installed", marker: "1.3.0", wantErr: "refusing daemon downgrade"},
		{name: "newer installed, forced", marker: "1.3.0", args: []string{"--force-downgrade"}},
		{name: "no marker", wantErr: "refusing to replace existing solud"},
		{name: "malformed marker, forced", marker: "garbage", args: []string{"--force-downgrade"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, testKey(t))
			payload := e.writeRelease(e.repo(), "1.2.3", releaseOpts{})
			writeExisting(t, e, tc.marker)
			r := e.run(runOpts{verifier: v, args: tc.args})
			if tc.wantErr != "" {
				r.wantFail(t, tc.wantErr)
				assertExisting(t, r, tc.marker)
				if r.downloadedBinary() {
					t.Fatal("binary downloaded although the downgrade guard refused")
				}
				return
			}
			r.wantOK(t, payload, "1.2.3")
		})
	}
}

func TestInstallArguments(t *testing.T) {
	v := verifiers(t)[0]
	for _, args := range [][]string{{"--bogus"}, {"--cloud", "ftp://x"}, {"--cloud", "https://x/path"}, {"--cloud", "http://example.com"}, {"--version", "1.2/../3"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			e := newEnv(t, testKey(t))
			e.writeRelease(e.repo(), "1.2.3", releaseOpts{})
			r := e.run(runOpts{verifier: v, args: args})
			if r.err == nil {
				t.Fatalf("accepted %v:\n%s", args, r.output)
			}
			if len(r.curls) != 0 {
				t.Fatalf("downloaded before rejecting arguments: %v", r.curls)
			}
		})
	}
	t.Run("default cloud", func(t *testing.T) {
		e := newEnv(t, testKey(t))
		e.writeRelease(e.repo(), "1.2.3", releaseOpts{})
		r := e.run(runOpts{verifier: v, args: []string{"--cloud=https://soluterminal-cloud.onrender.com/"}})
		if r.err != nil || !strings.Contains(r.output, "wss://soluterminal-cloud.onrender.com/relay") {
			t.Fatalf("err=%v\n%s", r.err, r.output)
		}
	})
	t.Run("localhost dev cloud uses ws", func(t *testing.T) {
		e := newEnv(t, testKey(t))
		e.writeRelease(e.repo(), "1.2.3", releaseOpts{})
		r := e.run(runOpts{verifier: v, args: []string{"--cloud", "http://localhost:8080"}})
		if r.err != nil || !strings.Contains(r.output, "ws://localhost:8080/relay") {
			t.Fatalf("err=%v\n%s", r.err, r.output)
		}
	})
}

// macOS runs /bin/sh as bash; Debian/Ubuntu as dash. Run the happy path under
// every POSIX shell on this host.
func TestInstallUnderAvailableShells(t *testing.T) {
	v := verifiers(t)[0]
	for _, sh := range []string{"dash", "bash", "busybox", "ksh", "zsh"} {
		path, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		t.Run(sh, func(t *testing.T) {
			shell := path
			e := newEnv(t, testKey(t))
			payload := e.writeRelease(e.repo(), "1.2.3", releaseOpts{})
			if sh == "busybox" {
				wrapper := filepath.Join(t.TempDir(), "busybox-sh")
				writeExec(t, wrapper, "#!/bin/sh\nexec "+path+" sh \"$@\"\n")
				shell = wrapper
			}
			if sh == "zsh" {
				wrapper := filepath.Join(t.TempDir(), "zsh-sh")
				writeExec(t, wrapper, "#!/bin/sh\nexec "+path+" --emulate sh \"$@\"\n")
				shell = wrapper
			}
			e.run(runOpts{verifier: v, shell: shell}).wantOK(t, payload, "1.2.3")
		})
	}
}
