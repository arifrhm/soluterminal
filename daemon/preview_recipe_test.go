package main

import (
	"context"
	"encoding/json"

	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"

	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func validPreviewRecipeForTest() previewRecipe {
	return previewRecipe{
		SchemaVersion: 1, Kind: previewModeStatic, AppDir: "web", StaticDir: "web/dist",
		HealthPath: "/", SpaFallback: true, PublicBase: "relative",
		AcceptedDigest: "sha256:" + strings.Repeat("a", 64), AcceptanceArtifactID: "art_accept", SourceCommit: "abc123",
	}
}

func TestRepoPreviewConfigValidation(t *testing.T) {
	valid := &repoPreviewConfig{AppDir: "web", Kind: previewModeStatic, BuildArgv: []string{"npm", "run", "build"}, StaticDir: "web/dist", HealthPath: "/health", PublicBase: "relative", Env: map[string]string{"API_ORIGIN": "https://example.invalid"}}
	if err := validateRepoPreviewConfig(valid); err != nil {
		t.Fatalf("valid preview config: %v", err)
	}
	for name, mutate := range map[string]func(*repoPreviewConfig){
		"drive relative path": func(config *repoPreviewConfig) { config.AppDir = `C:secret` },
		"backslash parent":    func(config *repoPreviewConfig) { config.AppDir = `web\..\secret` },
		"host health":         func(config *repoPreviewConfig) { config.HealthPath = "//example.invalid/health" },
		"reserved env":        func(config *repoPreviewConfig) { config.Env = map[string]string{"SOLU_TOKEN": "secret"} },
	} {
		t.Run(name, func(t *testing.T) {
			copy := *valid
			copy.BuildArgv = append([]string(nil), valid.BuildArgv...)
			copy.Env = clonePreviewEnv(valid.Env)
			mutate(&copy)
			if err := validateRepoPreviewConfig(&copy); err == nil {
				t.Fatal("expected config validation error")
			}
		})
	}
}

func writePreviewNpmDepsFixture(t *testing.T, appDir string) {
	t.Helper()
	if err := os.MkdirAll(appDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pkg := `{"scripts":{"build":"tsc -b && vite build","build:preview":"vite build --base=./","node-build":"node scripts/build.mjs"}}`
	if err := os.WriteFile(filepath.Join(appDir, "package.json"), []byte(pkg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "package-lock.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewPlaywrightUnavailableMessage(t *testing.T) {
	message := previewPlaywrightUnavailableMessage("client", []string{"incompatible fixture"})
	for _, want := range []string{
		"major " + strconv.Itoa(playwrightMajor),
		"client/node_modules",
		"client/package.json",
		"devDependency",
		"npm ci",
		"install it globally",
		"incompatible fixture",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("message %q does not contain %q", message, want)
		}
	}
}

func writePreviewFixture(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<!doctype html><body>accepted</body>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "app.js"), []byte("document.body.dataset.ready='1'"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewSealBundleDigestRoundTripAndSymlinkEscape(t *testing.T) {
	source := t.TempDir()
	writePreviewFixture(t, source)
	cache := t.TempDir()
	digest, bundle, err := previewSealBundle(source, cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := previewVerifyBundle(bundle, digest); err != nil {
		t.Fatalf("verify sealed bundle: %v", err)
	}
	if runtime.GOOS != "windows" {
		outside := filepath.Join(t.TempDir(), "outside.txt")
		if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(source, "escape")); err != nil {
			t.Fatal(err)
		}
		if _, _, err := previewSealBundle(source, t.TempDir()); err == nil {
			t.Fatal("symlink escape must be rejected")
		}
	}
}

func TestPreviewCacheSweepPinsLiveBundle(t *testing.T) {
	cache := t.TempDir()
	firstSource := t.TempDir()
	secondSource := t.TempDir()
	if err := os.WriteFile(filepath.Join(firstSource, "index.html"), []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondSource, "index.html"), []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, first, err := previewSealBundle(firstSource, cache)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := previewSealBundle(secondSource, cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := previewCacheSweepToLimit(cache, map[string]bool{second: true}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("unpinned bundle was not evicted: %v", err)
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("pinned bundle was evicted: %v", err)
	}
}

func TestPreviewStaticServerSPAFallbackAndDotfileRejection(t *testing.T) {
	root := t.TempDir()
	writePreviewFixture(t, root)
	server := &previewStaticServer{root: root, spa: true}
	request := httptest.NewRequest(http.MethodGet, "/nested/route/", nil)
	response := httptest.NewRecorder()
	server.serveHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "accepted") {
		t.Fatalf("SPA fallback status=%d body=%q", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/.git/config", nil)
	response = httptest.NewRecorder()
	server.serveHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("dotfile status=%d", response.Code)
	}
}

func TestPreviewStaticServerAPIPathsNeverServeSPAShell(t *testing.T) {
	root := t.TempDir()
	writePreviewFixture(t, root)
	server := &previewStaticServer{root: root, spa: true}
	for _, target := range []string{
		"/api/workflow/reason-codes",
		"/api/workflow-templates?limit=-1",
		"/api/panes",
		"/api",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		server.serveHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d, want 404 instead of the SPA shell", target, response.Code)
		}
		if strings.Contains(response.Body.String(), "accepted") {
			t.Fatalf("%s served the SPA shell: %q", target, response.Body.String())
		}
		if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Fatalf("%s content-type=%q", target, got)
		}
		var body struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s body is not JSON: %v (%q)", target, err, response.Body.String())
		}
		if body.Error != "preview backend unavailable" {
			t.Fatalf("%s error=%q", target, body.Error)
		}
	}

	// A bundle that genuinely ships a file under /api still serves it.
	if err := os.MkdirAll(filepath.Join(root, "api"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api", "catalogue.json"), []byte(`{"kind":"bundled"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/catalogue.json", nil)
	response := httptest.NewRecorder()
	server.serveHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "bundled") {
		t.Fatalf("bundled API asset status=%d body=%q", response.Code, response.Body.String())
	}

	// Non-API routes keep the SPA fallback the app depends on.
	request = httptest.NewRequest(http.MethodGet, "/tasks/wft_00000000", nil)
	response = httptest.NewRecorder()
	server.serveHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "accepted") {
		t.Fatalf("SPA route status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestPreviewRegistryReloadsOwnedModesStopped(t *testing.T) {
	dataDir := t.TempDir()
	cache := filepath.Join(dataDir, "preview-cache")
	source := t.TempDir()
	writePreviewFixture(t, source)
	digest, bundle, err := previewSealBundle(source, cache)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newPreviewManager(dataDir, "wsl", nil, cache)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	staticID := daemonPreviewTestID
	spawnID := "pv_11111111111111111111111111"
	manager.targets[staticID] = &PreviewTarget{ID: staticID, BackendID: "wsl", Mode: previewModeStatic, State: previewStateRunning, URL: "https://preview.example/preview/" + staticID + "/", CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(time.Hour), AcceptedDigest: digest, BundleDir: bundle, HealthPath: "/"}
	manager.targets[spawnID] = &PreviewTarget{ID: spawnID, BackendID: "wsl", Mode: previewModeSpawn, State: previewStateRunning, URL: "https://preview.example/preview/" + spawnID + "/", CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(time.Hour), AcceptedDigest: digest, BuildDir: source, ServeArgv: []string{"server"}, HealthPath: "/", PublicBase: "relative", PID: 99999999, ProcessStartIdentity: "linux:missing"}
	if err := manager.saveLocked(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newPreviewManager(dataDir, "wsl", nil, cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{staticID, spawnID} {
		target, ok := reloaded.Get(id)
		if !ok || target.State != previewStateIdle || target.TargetPort != 0 {
			t.Fatalf("reloaded %s target=%+v ok=%v", id, target, ok)
		}
	}
}

func TestPreviewRegistryPIDReuseBecomesErrorWithoutKilling(t *testing.T) {
	dataDir := t.TempDir()
	manager, err := newPreviewManager(dataDir, "wsl", nil)
	if err != nil {
		t.Fatal(err)
	}
	buildDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(buildDir, "build.txt"), []byte("accepted"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := previewTreeDigest(buildDir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manager.targets[daemonPreviewTestID] = &PreviewTarget{ID: daemonPreviewTestID, BackendID: "wsl", Mode: previewModeSpawn, State: previewStateRunning, URL: "https://preview.example/preview/" + daemonPreviewTestID + "/", CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(time.Hour), AcceptedDigest: digest, BuildDir: buildDir, ServeArgv: []string{"server"}, HealthPath: "/", PublicBase: "relative", PID: os.Getpid(), ProcessStartIdentity: "definitely-not-this-process"}
	if err := manager.saveLocked(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newPreviewManager(dataDir, "wsl", nil)
	if err != nil {
		t.Fatal(err)
	}
	target, ok := reloaded.Get(daemonPreviewTestID)
	if !ok || target.State != previewStateError {
		t.Fatalf("reused-pid target=%+v ok=%v", target, ok)
	}
	if !processAlive(os.Getpid()) {
		t.Fatal("identity mismatch killed an unrelated live process")
	}
}

func TestPreviewStaticIdleSweepStopsListenerButKeepsRegistration(t *testing.T) {
	dataDir := t.TempDir()
	cache := filepath.Join(dataDir, "preview-cache")
	source := t.TempDir()
	writePreviewFixture(t, source)
	digest, bundle, err := previewSealBundle(source, cache)
	if err != nil {
		t.Fatal(err)
	}
	server, err := newPreviewStaticServer(bundle, true)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newPreviewManager(dataDir, "wsl", nil, cache)
	if err != nil {
		t.Fatal(err)
	}
	manager.idleAfter = time.Second
	now := time.Now().UTC()
	manager.targets[daemonPreviewTestID] = &PreviewTarget{ID: daemonPreviewTestID, BackendID: "wsl", Mode: previewModeStatic, State: previewStateRunning, URL: "https://preview.example/preview/" + daemonPreviewTestID + "/", CreatedAt: now.Add(-time.Hour), LastActivityAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), AcceptedDigest: digest, BundleDir: bundle, HealthPath: "/", TargetPort: server.Port()}
	manager.runtimes[daemonPreviewTestID] = &previewOwnedRuntime{static: server}
	manager.runningOwned = 1
	manager.sweep(context.Background())
	target, ok := manager.Get(daemonPreviewTestID)
	if !ok || target.State != previewStateIdle || target.TargetPort != 0 || manager.runningOwned != 0 {
		t.Fatalf("idle target=%+v ok=%v runningOwned=%d", target, ok, manager.runningOwned)
	}
	if server.Port() != 0 {
		t.Fatal("idle static listener remained open")
	}
}

func recipeCloudClient(t *testing.T, methods *atomic.Int32) *http.Client {
	t.Helper()
	return &http.Client{Transport: previewRoundTripper(func(request *http.Request) (*http.Response, error) {
		methods.Add(1)
		status := http.StatusOK
		body := "{}"
		if request.Method == http.MethodPost && request.URL.Path == "/api/internal/previews" {
			status = http.StatusCreated
			body = `{"previewId":"` + daemonPreviewTestID + `","url":"https://preview.example/preview/` + daemonPreviewTestID + `/","expiresAt":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano) + `"}`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
}

func TestPreviewServedBytesMatchAcceptedDigest(t *testing.T) {
	t.Setenv("SOLU_PREVIEWS", "1")
	dataDir := t.TempDir()
	cache := filepath.Join(dataDir, "preview-cache")
	source := t.TempDir()
	writePreviewFixture(t, source)
	digest, _, err := previewSealBundle(source, cache)
	if err != nil {
		t.Fatal(err)
	}
	var methods atomic.Int32
	manager, err := newPreviewManager(dataDir, "wsl", func() (string, string) { return "https://cloud.invalid", "token" }, cache)
	if err != nil {
		t.Fatal(err)
	}
	manager.client = recipeCloudClient(t, &methods)
	recipe := validPreviewRecipeForTest()
	recipe.AppDir, recipe.StaticDir, recipe.AcceptedDigest = ".", "dist", digest
	target, err := manager.CreateFromRecipe(context.Background(), "wft_digest", t.TempDir(), recipe)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Stop(context.Background(), target.ID)

	served := t.TempDir()
	api := &apiServer{previews: manager}
	for _, rel := range []string{"index.html", "assets/app.js"} {
		request := httptest.NewRequest(http.MethodGet, "/api/preview-serve/"+target.ID+"/"+rel, nil)
		request.SetPathValue("previewId", target.ID)
		response := httptest.NewRecorder()
		api.servePreview(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d body=%s", rel, response.Code, response.Body.String())
		}
		path := filepath.Join(served, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, response.Body.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	servedDigest, err := previewTreeDigest(served)
	if err != nil {
		t.Fatal(err)
	}
	if servedDigest != recipe.AcceptedDigest {
		t.Fatalf("served digest=%s accepted=%s", servedDigest, recipe.AcceptedDigest)
	}
}

func TestPreviewMountedProxyRejectsPrefixEscapingAsset(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = io.WriteString(w, `<body>prefix fixture<script src="/asset.js"></script></body>`)
			return
		}
		if r.URL.Path == "/asset.js" {
			_, _ = io.WriteString(w, `document.body.dataset.loaded = "yes"`)
			return
		}
		http.NotFound(w, r)
	}))
	defer backend.Close()
	port := uint16(backend.Listener.Addr().(*net.TCPAddr).Port)
	prefix := "/preview/" + daemonPreviewTestID
	mounted := httptest.NewServer(previewMountedProxyHandler(port, prefix))
	defer mounted.Close()
	response, err := http.Get(mounted.URL + prefix + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("mounted document status=%d", response.StatusCode)
	}
	// A browser resolves the fixture's absolute asset URL at the origin root.
	// The acceptance mount refuses that escape, and the probe's same-origin
	// response>=400 listener consequently makes the acceptance result fail.
	response, err = http.Get(mounted.URL + "/asset.js")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("prefix-escaping asset status=%d, want 404", response.StatusCode)
	}
}

func TestPreviewBasePathProxyPreservesCompiledMountForSpawn(t *testing.T) {
	prefix := "/api/preview-serve/" + daemonPreviewTestID
	targetBase := "/preview/" + daemonPreviewTestID
	proxy := previewLoopbackProxy(4321, prefix, targetBase)
	var forwardedPath string
	proxy.Transport = previewRoundTripper(func(request *http.Request) (*http.Response, error) {
		forwardedPath = request.URL.Path
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: request}, nil
	})
	request := httptest.NewRequest(http.MethodGet, prefix+"/_next/static/app.js", nil)
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)
	if response.Code != http.StatusOK || forwardedPath != targetBase+"/_next/static/app.js" {
		t.Fatalf("status=%d forwarded=%q", response.Code, forwardedPath)
	}
}

func TestPreviewMutatedBundleCannotCreateOrWake(t *testing.T) {
	t.Setenv("SOLU_PREVIEWS", "1")
	dataDir := t.TempDir()
	cache := filepath.Join(dataDir, "preview-cache")
	source := t.TempDir()
	writePreviewFixture(t, source)
	digest, bundle, err := previewSealBundle(source, cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "index.html"), []byte("mutated"), 0o600); err != nil {
		t.Fatal(err)
	}
	var methods atomic.Int32
	manager, err := newPreviewManager(dataDir, "wsl", func() (string, string) { return "https://cloud.invalid", "token" }, cache)
	if err != nil {
		t.Fatal(err)
	}
	manager.client = recipeCloudClient(t, &methods)
	recipe := validPreviewRecipeForTest()
	recipe.AcceptedDigest = digest
	if _, err := manager.CreateFromRecipe(context.Background(), "wft_mutated", t.TempDir(), recipe); err == nil {
		t.Fatal("mutated bundle creation must fail")
	}
	if methods.Load() != 0 {
		t.Fatal("cloud registration must not happen before digest verification")
	}
	now := time.Now().UTC()
	manager.targets[daemonPreviewTestID] = &PreviewTarget{ID: daemonPreviewTestID, TaskID: "wft_mutated", BackendID: "wsl", Mode: previewModeStatic, State: previewStateIdle, URL: "https://preview.example/preview/" + daemonPreviewTestID + "/", CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(time.Hour), AcceptedDigest: digest, BundleDir: bundle, HealthPath: "/"}
	if _, err := manager.Relaunch(context.Background(), daemonPreviewTestID); err == nil {
		t.Fatal("mutated bundle wake must fail")
	}
	got, _ := manager.Get(daemonPreviewTestID)
	if got.State != previewStateError {
		t.Fatalf("wake state=%q, want error", got.State)
	}
}
