package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func previewSourceFunction(t *testing.T, file, start, end string) string {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	from := strings.Index(text, start)
	if from < 0 {
		t.Fatalf("%s does not contain %q", file, start)
	}
	to := strings.Index(text[from+len(start):], end)
	if to < 0 {
		t.Fatalf("%s does not contain %q after %q", file, end, start)
	}
	return text[from : from+len(start)+to]
}

func TestPreviewSpawnDigestExcludesDependencyTree(t *testing.T) {
	worktree := t.TempDir()
	appDir := filepath.Join(worktree, "web")
	buildDir := filepath.Join(appDir, ".next")
	if err := os.MkdirAll(buildDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "BUILD_ID"), []byte("accepted"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideStore := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideStore, "dependency.js"), []byte("pnpm store dependency"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideStore, filepath.Join(appDir, "node_modules")); err != nil {
		if os.IsPermission(err) {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(buildDir, "cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideStore, filepath.Join(buildDir, "cache", "pnpm-store")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "trace"), []byte("startup trace"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := previewBuildDigest(buildDir)
	if err != nil {
		t.Fatal(err)
	}
	recipe := previewRecipe{
		SchemaVersion: 1, Kind: previewModeSpawn, AppDir: "web", BuildDir: "web/.next",
		BuildArgv: []string{"pnpm", "build"}, ServeArgv: []string{"pnpm", "start"},
		HealthPath: "/", PublicBase: "basePath", AcceptedDigest: digest,
		AcceptanceArtifactID: "art_accept", SourceCommit: "abc123",
	}
	manager, err := newPreviewManager(t.TempDir(), "wsl", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, resolvedApp, resolvedBuild, err := manager.verifyRecipeOutput(worktree, recipe)
	if err != nil {
		t.Fatalf("pnpm-style dependency symlink poisoned build digest: %v", err)
	}
	if resolvedApp != appDir || resolvedBuild != buildDir {
		t.Fatalf("resolved app=%q build=%q", resolvedApp, resolvedBuild)
	}
	if err := os.WriteFile(filepath.Join(outsideStore, "dependency.js"), []byte("runtime cache mutation"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "trace"), []byte("runtime-mutated trace"), 0o600); err != nil {
		t.Fatal(err)
	}
	if after, err := previewBuildDigest(buildDir); err != nil || after != digest {
		t.Fatalf("dependency mutation changed accepted production output digest: digest=%q err=%v", after, err)
	}
}

func TestPreviewSpawnBuildDigestIsBounded(t *testing.T) {
	buildDir := t.TempDir()
	oversized := filepath.Join(buildDir, "oversized-output.bin")
	file, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(previewBundleMaxBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := previewBuildDigest(buildDir); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized spawn output was not rejected: %v", err)
	}
}

func TestPreviewBasePathRebuildUsesAcceptedBuildArgv(t *testing.T) {
	source := previewSourceFunction(t, "shared_preview_accept.go", "func previewRebuildForBasePath", "func previewProbeMounted")
	if strings.Contains(source, `[]string{"npm", "run", "build"}`) || !strings.Contains(source, "buildArgv") {
		t.Fatalf("basePath rebuild does not use the recipe's accepted buildArgv:\n%s", source)
	}
}

func TestPreviewBuildArgvHelperProcess(t *testing.T) {
	if os.Getenv("PREVIEW_BUILD_HELPER") != "1" {
		return
	}
	marker := os.Getenv("PREVIEW_BUILD_MARKER")
	if marker == "" || os.Getenv("SOLU_PREVIEW_BASE_PATH") == "" {
		os.Exit(3)
	}
	_ = os.WriteFile(marker, []byte(os.Getenv("SOLU_PREVIEW_BASE_PATH")), 0o600)
	_ = os.MkdirAll(".next", 0o700)
	_ = os.WriteFile(filepath.Join(".next", "BUILD_ID"), []byte("rebuilt"), 0o600)
	os.Exit(0)
}

func TestPreviewBasePathRebuildExecutesRecipeBuildArgv(t *testing.T) {
	appDir := t.TempDir()
	buildDir := filepath.Join(appDir, ".next")
	if err := os.MkdirAll(buildDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "build-command-ran")
	basePath := "/preview/" + daemonPreviewTestID
	digest, err := previewRebuildForBasePath(
		context.Background(), appDir, buildDir,
		[]string{os.Args[0], "-test.run=^TestPreviewBuildArgvHelperProcess$", "--"},
		map[string]string{"PREVIEW_BUILD_HELPER": "1", "PREVIEW_BUILD_MARKER": marker}, basePath,
	)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(marker)
	if err != nil || string(raw) != basePath {
		t.Fatalf("recipe build command marker=%q err=%v", raw, err)
	}
	if actual, err := previewTreeDigest(buildDir); err != nil || actual != digest {
		t.Fatalf("rebuild digest=%q actual=%q err=%v", digest, actual, err)
	}
}

func TestPreviewStopDuringWakeKillsFreshSpawn(t *testing.T) {
	t.Setenv("SOLU_PREVIEWS", "1")
	probe, probeErr := net.Listen("tcp", "127.0.0.1:0")
	if probeErr != nil {
		t.Skipf("loopback listeners unavailable: %v", probeErr)
	}
	_ = probe.Close()
	dataDir := t.TempDir()
	manager, err := newPreviewManager(dataDir, "wsl", func() (string, string) { return "https://cloud.invalid", "token" })
	if err != nil {
		t.Fatal(err)
	}
	manager.client = &http.Client{Transport: previewRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})}
	buildDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(buildDir, "build.txt"), []byte("accepted"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := previewTreeDigest(buildDir)
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "spawn.pid")
	now := time.Now().UTC()
	manager.targets[daemonPreviewTestID] = &PreviewTarget{
		ID: daemonPreviewTestID, TaskID: "wft_wake_stop", BackendID: "wsl", Mode: previewModeSpawn,
		State: previewStateIdle, URL: "https://preview.example/preview/" + daemonPreviewTestID + "/",
		CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(time.Hour), AcceptedDigest: digest,
		BuildDir: buildDir, ServeArgv: previewHelperArgv(), HealthPath: "/", PublicBase: "relative",
		Env: map[string]string{"PREVIEW_HELPER": "1", "PREVIEW_PID_FILE": pidFile, "PREVIEW_DELAY_MS": "350"},
	}
	call, err := manager.triggerWake(daemonPreviewTestID)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, readErr := os.ReadFile(pidFile)
		if readErr == nil {
			pid, _ = strconv.Atoi(string(raw))
			if pid > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("spawn did not reach its health gate")
	}
	defer func() {
		if processAlive(pid) {
			if process, findErr := os.FindProcess(pid); findErr == nil {
				_ = previewStopTree(&previewSpawnProcess{Process: process})
			}
		}
	}()
	if err := manager.Stop(context.Background(), daemonPreviewTestID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-call.done:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight wake did not finish")
	}
	deadline = time.Now().Add(3 * time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatalf("freshly spawned process %d survived a concurrent Stop", pid)
	}
}

func TestPreviewWakeCleanupUsesFreshSpawnIdentity(t *testing.T) {
	idleSnapshot := PreviewTarget{Mode: previewModeSpawn}
	cleanup := previewWakeCleanupTarget(idleSnapshot, 43123, 9876, "linux:accepted-child")
	if cleanup.TargetPort != 43123 || cleanup.PID != 9876 || cleanup.ProcessStartIdentity != "linux:accepted-child" {
		t.Fatalf("wake cleanup retained the idle snapshot instead of the returned child identity: %+v", cleanup)
	}
}

const fakePreviewPlaywrightModule = `
const fs = require('node:fs');
module.exports = { chromium: {
  executablePath: () => process.execPath,
  launch: async () => {
    let contextNumber = 0;
    return ({
    newContext: async () => {
      contextNumber += 1;
      const handlers = {};
      const mainFrame = {};
      const page = {
        on: (name, handler) => { handlers[name] = handler; },
        mainFrame: () => mainFrame,
        goto: async (target) => {
          if (process.env.PREVIEW_FAKE_SCENARIO === 'theme-throw' && contextNumber === 2) {
            throw new Error('fixture light theme timed out');
          }
          if (process.env.PREVIEW_FAKE_SCENARIO === 'pageerror') {
            handlers.pageerror?.(new TypeError('fixture page exploded'));
          }
          if (process.env.PREVIEW_FAKE_SCENARIO === 'absolute-asset') {
            const asset = new URL('/assets/app.js', target).href;
            const request = { url: () => asset, method: () => 'GET', failure: () => null };
            handlers.request?.(request);
            handlers.response?.({ status: () => 200, url: () => asset, request: () => request });
          }
          if (process.env.PREVIEW_FAKE_SCENARIO === 'api-stub-404') {
            const api = new URL('api/workflow/campaign', target).href;
            const request = { url: () => api, method: () => 'GET', failure: () => null };
            handlers.console?.({ type: () => 'error', text: () => 'Failed to load resource: the server responded with a status of 404 (Not Found)', location: () => ({ url: api }) });
            handlers.request?.(request);
            handlers.response?.({ status: () => 404, url: () => api, request: () => request, text: async () => '{"error":"smoke stub: unknown api path"}\n' });
          }
          if (process.env.PREVIEW_FAKE_SCENARIO === 'api-backend-unavailable') {
            const api = new URL('api/workflow/tasks', target).href;
            const request = { url: () => api, method: () => 'GET', failure: () => null };
            handlers.console?.({ type: () => 'error', text: () => 'Failed to load resource: the server responded with a status of 404 (Not Found)', location: () => ({ url: api }) });
            handlers.request?.(request);
            handlers.response?.({ status: () => 404, url: () => api, request: () => request, text: async () => '{"error":"preview backend unavailable"}\n' });
          }
          handlers.framenavigated?.(mainFrame);
          if (process.env.PREVIEW_FAKE_SCENARIO === 'reload-storm') {
            handlers.framenavigated?.(mainFrame);
            handlers.framenavigated?.(mainFrame);
            handlers.framenavigated?.(mainFrame);
          }
          return { status: () => 200 };
        },
        waitForLoadState: async () => {},
        locator: () => ({ innerText: async () => 'fixture body', count: async () => 0 }),
        evaluate: async (_fn, argument) => argument === undefined ? true : 200,
        screenshot: async ({path}) => { fs.writeFileSync(path, 'png'); }
      };
      return { newPage: async () => page, close: async () => {} };
    },
    close: async () => {}
  })}
} };
`

func TestPreviewAcceptanceRejectsPageErrorsAndPrefixEscapes(t *testing.T) {
	node, err := resolveNodeBin()
	if err != nil {
		t.Skipf("node unavailable: %v", err)
	}
	moduleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(moduleDir, "package.json"), []byte(`{"name":"fake-playwright","main":"index.cjs"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "index.cjs"), []byte(fakePreviewPlaywrightModule), 0o600); err != nil {
		t.Fatal(err)
	}
	script, err := materializePreviewAcceptScript(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"pageerror", "absolute-asset", "theme-throw", "reload-storm"} {
		t.Run(scenario, func(t *testing.T) {
			out := t.TempDir()
			cmd := exec.Command(node, script, "--url", "http://127.0.0.1:4173/preview/"+daemonPreviewTestID+"/", "--health-path", "/", "--out", out)
			// This fixture never launches Chromium; bypass the real-socket
			// preflight so the page-result contract remains testable even when
			// the Go test process itself is a sandboxed executor.
			cmd.Env = append(workerEnv(), "PLAYWRIGHT_MODULE_PATH="+moduleDir, "PREVIEW_FAKE_SCENARIO="+scenario, "SOLU_BROWSER_PREFLIGHT=skip")
			runErr := cmd.Run()
			if runErr == nil {
				t.Fatalf("%s acceptance unexpectedly passed", scenario)
			}
			raw, readErr := os.ReadFile(filepath.Join(out, "result.json"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			var result previewProbeResult
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			if result.Pass {
				t.Fatalf("%s result did not record the browser failure: %+v", scenario, result)
			}
			if scenario == "reload-storm" && result.NavigationCount <= 2 {
				t.Fatalf("reload storm did not record excess navigations: %+v", result)
			}
			if scenario != "reload-storm" && len(result.ConsoleErrors)+len(result.FailedRequests) == 0 {
				t.Fatalf("%s result did not retain console/network evidence: %+v", scenario, result)
			}
			if scenario == "theme-throw" && !strings.Contains(strings.Join(result.ConsoleErrors, " "), "fixture light theme timed out") {
				t.Fatalf("theme throw was not captured as browser evidence: %+v", result)
			}
		})
	}
	t.Run("clean-dual-theme", func(t *testing.T) {
		out := t.TempDir()
		cmd := exec.Command(node, script, "--url", "http://127.0.0.1:4173/preview/"+daemonPreviewTestID+"/", "--health-path", "/", "--out", out)
		cmd.Env = append(workerEnv(), "PLAYWRIGHT_MODULE_PATH="+moduleDir, "SOLU_BROWSER_PREFLIGHT=skip")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("clean acceptance failed: %v\n%s", err, output)
		}
		raw, err := os.ReadFile(filepath.Join(out, "result.json"))
		if err != nil {
			t.Fatal(err)
		}
		var result previewProbeResult
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		if !result.Pass || result.DocumentStatus != http.StatusOK || result.HealthStatus != http.StatusOK || result.BodyTextLength == 0 || !result.PreviewMarker {
			t.Fatalf("clean result did not prove strict mounted rendering: %+v", result)
		}
		if len(result.ConsoleErrors) != 0 || len(result.FailedRequests) != 0 {
			t.Fatalf("clean result recorded browser failures: %+v", result)
		}
		if result.NavigationCount != 2 || result.StaleBundleBanner {
			t.Fatalf("clean dual-theme probe navigation/banner evidence=%+v", result)
		}
		for _, name := range []string{"screenshot.png", "screenshot-light.png"} {
			if info, err := os.Stat(filepath.Join(out, name)); err != nil || info.Size() == 0 {
				t.Fatalf("%s missing or empty: info=%v err=%v", name, info, err)
			}
		}
	})
}

func TestPreviewProbeResultWithMissingScreenshotIsExecutedFailure(t *testing.T) {
	out := t.TempDir()
	result := previewProbeResult{
		Pass: true, DocumentStatus: http.StatusOK, HealthStatus: http.StatusOK,
		BodyTextLength: 42, PreviewMarker: true,
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "result.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "screenshot.png"), []byte("dark"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	acceptance, screenshots, err := previewReadProbeOutput(
		out, "/preview/"+"pv_00000000000000000000000000", digest, "fixture-source", errors.New("exit status 1"),
	)
	var failed *previewAcceptanceFailedError
	if !errors.As(err, &failed) {
		t.Fatalf("missing light screenshot error=%v, want typed executed failure", err)
	}
	if acceptance == nil || acceptance.Pass || !strings.Contains(failed.Error(), "light preview acceptance screenshot missing") {
		t.Fatalf("acceptance=%+v failure=%v", acceptance, failed)
	}
	if len(screenshots.Dark) == 0 || len(screenshots.Light) != 0 {
		t.Fatalf("screenshots dark=%d light=%d", len(screenshots.Dark), len(screenshots.Light))
	}
}

func TestPreviewLoopbackProxyWebSocketAndSSE(t *testing.T) {
	releaseSSE := make(chan struct{})
	releasedSSE := false
	defer func() {
		if !releasedSSE {
			close(releaseSSE)
		}
	}()
	backendListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listeners unavailable: %v", err)
	}
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws":
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }, Subprotocols: []string{"preview-echo"}}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			kind, payload, err := conn.ReadMessage()
			if err == nil {
				_ = conn.WriteMessage(kind, payload)
			}
		case "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: first\n\n")
			w.(http.Flusher).Flush()
			<-releaseSSE
			_, _ = io.WriteString(w, "data: second\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	_ = backend.Listener.Close()
	backend.Listener = backendListener
	backend.Start()
	defer backend.Close()
	port := uint16(backend.Listener.Addr().(*net.TCPAddr).Port)
	prefix := "/preview/" + daemonPreviewTestID
	mountedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listeners unavailable: %v", err)
	}
	mounted := httptest.NewUnstartedServer(previewMountedProxyHandler(port, prefix))
	_ = mounted.Listener.Close()
	mounted.Listener = mountedListener
	mounted.Start()
	defer mounted.Close()

	dialer := websocket.Dialer{Subprotocols: []string{"preview-echo"}, HandshakeTimeout: 2 * time.Second}
	conn, response, err := dialer.Dial(strings.Replace(mounted.URL, "http://", "ws://", 1)+prefix+"/ws", nil)
	if err != nil {
		t.Fatalf("websocket dial: %v (response=%v)", err, response)
	}
	if conn.Subprotocol() != "preview-echo" {
		t.Fatalf("websocket subprotocol=%q", conn.Subprotocol())
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte("echo")); err != nil {
		t.Fatal(err)
	}
	_, payload, err := conn.ReadMessage()
	_ = conn.Close()
	if err != nil || string(payload) != "echo" {
		t.Fatalf("websocket echo payload=%q err=%v", payload, err)
	}

	type responseResult struct {
		response *http.Response
		err      error
	}
	responseCh := make(chan responseResult, 1)
	go func() {
		response, err := (&http.Client{Timeout: 3 * time.Second}).Get(mounted.URL + prefix + "/events")
		responseCh <- responseResult{response: response, err: err}
	}()
	var eventResponse *http.Response
	select {
	case result := <-responseCh:
		if result.err != nil {
			t.Fatal(result.err)
		}
		eventResponse = result.response
	case <-time.After(time.Second):
		close(releaseSSE)
		releasedSSE = true
		t.Fatal("SSE response headers/first event were buffered")
	}
	defer eventResponse.Body.Close()
	if got := eventResponse.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		close(releaseSSE)
		releasedSSE = true
		t.Fatalf("SSE content type=%q", got)
	}
	line, err := bufio.NewReader(eventResponse.Body).ReadString('\n')
	close(releaseSSE)
	releasedSSE = true
	if err != nil || line != "data: first\n" {
		t.Fatalf("first SSE line=%q err=%v", line, err)
	}
}

func TestPreviewStaticServerRejectsMutationAfterStart(t *testing.T) {
	cache := t.TempDir()
	source := t.TempDir()
	writePreviewFixture(t, source)
	digest, bundle, err := previewSealBundle(source, cache)
	if err != nil {
		t.Fatal(err)
	}
	integrity, err := previewStaticIntegrity(bundle, digest)
	if err != nil {
		t.Fatal(err)
	}
	server := &previewStaticServer{root: bundle, spa: true, integrity: integrity}
	if err := os.WriteFile(filepath.Join(bundle, "index.html"), []byte("mutated bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	server.serveHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("mutated sealed bundle status=%d body=%q", response.Code, response.Body.String())
	}
}

// TestPreviewAcceptanceKeepsUnknownApiPathLoud pins the mounted-preview
// tripwire behind the smoke harness: an unstubbed boot-time poll answered by
// the harness's honest "smoke stub: unknown api path" 404 must fail the probe
// with its URL, while the genuinely backend-less "preview backend
// unavailable" stand-in stays green. It drives the real accept-preview.mjs
// with the fake Playwright module, so no browser is required.
func TestPreviewAcceptanceKeepsUnknownApiPathLoud(t *testing.T) {
	node, err := resolveNodeBin()
	if err != nil {
		t.Skipf("node unavailable: %v", err)
	}
	moduleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(moduleDir, "package.json"), []byte(`{"name":"fake-playwright","main":"index.cjs"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "index.cjs"), []byte(fakePreviewPlaywrightModule), 0o600); err != nil {
		t.Fatal(err)
	}
	script, err := materializePreviewAcceptScript(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runScenario := func(t *testing.T, scenario string) (error, previewProbeResult) {
		t.Helper()
		out := t.TempDir()
		cmd := exec.Command(node, script, "--url", "http://127.0.0.1:4173/preview/"+daemonPreviewTestID+"/", "--health-path", "/", "--out", out)
		// This fixture never launches Chromium; bypass the real-socket
		// preflight so the page-result contract remains testable even when
		// the Go test process itself is a sandboxed executor.
		cmd.Env = append(workerEnv(), "PLAYWRIGHT_MODULE_PATH="+moduleDir, "PREVIEW_FAKE_SCENARIO="+scenario, "SOLU_BROWSER_PREFLIGHT=skip")
		runErr := cmd.Run()
		raw, readErr := os.ReadFile(filepath.Join(out, "result.json"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		var result previewProbeResult
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return runErr, result
	}
	t.Run("unstubbed-api-path-fails", func(t *testing.T) {
		runErr, result := runScenario(t, "api-stub-404")
		if runErr == nil || result.Pass {
			t.Fatalf("smoke-stub 404 acceptance passed, want loud failure: %+v", result)
		}
		if !strings.Contains(strings.Join(result.FailedRequests, "\n"), "api/workflow/campaign") {
			t.Fatalf("smoke-stub 404 did not record the unstubbed poll URL: %+v", result.FailedRequests)
		}
	})
	t.Run("backend-unavailable-stand-in-stays-green", func(t *testing.T) {
		runErr, result := runScenario(t, "api-backend-unavailable")
		if runErr != nil || !result.Pass {
			t.Fatalf("backend-unavailable stand-in failed, want green: err=%v %+v", runErr, result)
		}
		if len(result.ConsoleErrors) != 0 || len(result.FailedRequests) != 0 {
			t.Fatalf("backend-unavailable stand-in recorded browser failures: %+v", result)
		}
	})
}
