package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPreviewHTTPHelperProcess(t *testing.T) {
	if os.Getenv("PREVIEW_HELPER") != "1" {
		return
	}
	port, _ := strconv.Atoi(os.Getenv("PORT"))
	if pidFile := os.Getenv("PREVIEW_PID_FILE"); pidFile != "" {
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
	if delay, _ := strconv.Atoi(os.Getenv("PREVIEW_DELAY_MS")); delay > 0 {
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		os.Exit(3)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/never" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/env" {
			_ = json.NewEncoder(w).Encode(os.Environ())
			return
		}
		if r.URL.Path == "/limits" {
			raw, err := os.ReadFile("/proc/self/limits")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(raw)
			return
		}
		_, _ = io.WriteString(w, "healthy preview")
	})
	_ = http.Serve(listener, handler)
}

func previewHelperArgv() []string {
	return []string{os.Args[0], "-test.run=TestPreviewHTTPHelperProcess", "--"}
}

func TestPreviewSpawnHealthGateAndTimeoutKill(t *testing.T) {
	buildDir := t.TempDir()
	port, err := previewAllocatePort()
	if err != nil {
		t.Fatal(err)
	}
	spawned, err := previewSpawnWithOptions(context.Background(), buildDir, previewHelperArgv(), map[string]string{"PREVIEW_HELPER": "1"}, port, t.TempDir(), "", "/")
	if err != nil {
		t.Fatal(err)
	}
	if !processAlive(spawned.Process.Pid) {
		t.Fatal("healthy child is not alive")
	}
	if err := previewStopTree(spawned); err != nil {
		t.Fatal(err)
	}

	port, err = previewAllocatePort()
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	// The unhealthy child is the race-instrumented test binary started through
	// a login shell. On a busy verification host it can take longer than a
	// fixed 500ms probe window just to reach the pid-write, so a short
	// wall-clock timeout turns this into a race on host load: the gate kills
	// the child before the test can learn its pid, and the read below fails
	// with ENOENT rather than exercising the kill path. Keep a real gate
	// deadline but only fire it once the child has actually recorded its pid,
	// so the assertions below run against a deterministically started child.
	ctx, cancel := context.WithTimeout(context.Background(), previewHealthGateTimeout)
	defer cancel()
	spawnErr := make(chan error, 1)
	go func() {
		_, err := previewSpawnWithOptions(ctx, buildDir, previewHelperArgv(), map[string]string{"PREVIEW_HELPER": "1", "PREVIEW_PID_FILE": pidFile}, port, t.TempDir(), "", "/never")
		spawnErr <- err
	}()
	waitForPreviewPIDFile(t, pidFile, spawnErr)
	cancel()
	if err := <-spawnErr; err == nil {
		t.Fatal("unhealthy child must fail its health gate")
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(string(raw))
	if processAlive(pid) {
		t.Fatalf("unhealthy child %d survived the health timeout", pid)
	}
}

// waitForPreviewPIDFile blocks until the spawned preview helper has written its
// pid file, or the child exits, or a bounded deadline elapses. Waiting on the
// child's own readiness (instead of a fixed short timeout) keeps the health-gate
// test independent of host load while still failing fast when the child never
// starts.
func waitForPreviewPIDFile(t *testing.T, path string, spawnErr <-chan error) {
	t.Helper()
	deadline := time.Now().Add(previewHealthGateTimeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case err := <-spawnErr:
			t.Fatalf("preview child exited before recording its pid: %v", err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("preview child never recorded its pid at %s", path)
}

func TestPreviewNeverKillsMismatchedProcessIdentity(t *testing.T) {
	port, err := previewAllocatePort()
	if err != nil {
		t.Fatal(err)
	}
	spawned, err := previewSpawnWithOptions(context.Background(), t.TempDir(), previewHelperArgv(), map[string]string{"PREVIEW_HELPER": "1"}, port, t.TempDir(), "", "/")
	if err != nil {
		t.Fatal(err)
	}
	defer previewStopTree(spawned)
	manager, err := newPreviewManager(t.TempDir(), "wsl", nil)
	if err != nil {
		t.Fatal(err)
	}
	target := PreviewTarget{Mode: previewModeSpawn, PID: spawned.Process.Pid, ProcessStartIdentity: "wrong-identity"}
	if err := manager.stopOwnedRuntime(target, &previewOwnedRuntime{spawn: spawned}); err == nil {
		t.Fatal("identity mismatch must be reported")
	}
	if !processAlive(spawned.Process.Pid) {
		t.Fatal("identity-mismatched process was killed")
	}
}

func TestPreviewSpawnWakeSingleflightAndInterstitial(t *testing.T) {
	t.Setenv("SOLU_PREVIEWS", "1")
	dataDir := t.TempDir()
	manager, err := newPreviewManager(dataDir, "wsl", nil)
	if err != nil {
		t.Fatal(err)
	}
	buildDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(buildDir, "accepted.txt"), []byte("accepted"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := previewTreeDigest(buildDir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manager.targets[daemonPreviewTestID] = &PreviewTarget{
		ID: daemonPreviewTestID, URL: "https://preview.example/preview/" + daemonPreviewTestID + "/", BackendID: "wsl",
		Mode: previewModeSpawn, State: previewStateIdle, BuildDir: buildDir, ServeArgv: previewHelperArgv(),
		Env: map[string]string{"PREVIEW_HELPER": "1", "PREVIEW_DELAY_MS": "250"}, HealthPath: "/", PublicBase: "relative",
		AcceptedDigest: digest, CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(time.Hour),
	}
	api := &apiServer{previews: manager}
	request := httptest.NewRequest(http.MethodGet, "/api/preview-serve/"+daemonPreviewTestID+"/", nil)
	request.SetPathValue("previewId", daemonPreviewTestID)
	response := httptest.NewRecorder()
	api.servePreview(response, request)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "2" || !strings.Contains(response.Body.String(), "Preview waking up") {
		t.Fatalf("wake response status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}

	const callers = 12
	calls := make([]*previewStartCall, callers)
	var wg sync.WaitGroup
	for i := range calls {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			calls[index], _ = manager.triggerWake(daemonPreviewTestID)
		}(i)
	}
	wg.Wait()
	for i := 1; i < len(calls); i++ {
		if calls[i] != calls[0] {
			t.Fatal("concurrent wake did not share one start call")
		}
	}
	select {
	case <-calls[0].done:
	case <-time.After(10 * time.Second):
		t.Fatal("wake did not finish")
	}
	if calls[0].err != nil {
		t.Fatal(calls[0].err)
	}
	if manager.runningOwned != 1 {
		t.Fatalf("running owned=%d, want 1", manager.runningOwned)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/preview-serve/"+daemonPreviewTestID+"/", nil)
	request.SetPathValue("previewId", daemonPreviewTestID)
	response = httptest.NewRecorder()
	api.servePreview(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "healthy preview") {
		t.Fatalf("healthy response status=%d body=%s", response.Code, response.Body.String())
	}
	manager.shutdownOwned()
}

func TestPreviewSpawnEnvironmentIsScrubbed(t *testing.T) {
	t.Setenv("SOLU_MACHINE_TOKEN", "secret")
	t.Setenv("SUPABASE_SERVICE_KEY", "secret")
	t.Setenv("SSH_AUTH_SOCK", "/secret/socket")
	env, err := previewSpawnEnv(map[string]string{"PREVIEW_HELPER": "1"}, 4321, "/preview/"+daemonPreviewTestID)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	for _, denied := range []string{"SOLU_MACHINE_TOKEN=", "SUPABASE_SERVICE_KEY=", "SSH_AUTH_SOCK="} {
		if strings.Contains(joined, denied) {
			t.Fatalf("spawn env leaked %s", denied)
		}
	}
	if !strings.Contains(joined, "PREVIEW_HELPER=1") || !strings.Contains(joined, "PORT=4321") || !strings.Contains(joined, "SOLU_PREVIEW_BASE_PATH=/preview/") {
		t.Fatalf("spawn env missing generated/allowlisted values: %s", joined)
	}
}

// fakePreviewToolchain swaps the resolution seam for one that records what it
// was handed and returns a command the test machine definitely has. Both
// callers below pass a bare `npm` argv that does NOT exist under `go test` —
// if exec.Command still received the raw argv the spawn would fail with
// exec: "npm": executable file not found in $PATH, which is the production bug.
func fakePreviewToolchain(t *testing.T, resolved []string) *[][]string {
	t.Helper()
	original := previewToolchainArgv
	t.Cleanup(func() { previewToolchainArgv = original })
	var seen [][]string
	previewToolchainArgv = func(argv []string) []string {
		seen = append(seen, append([]string(nil), argv...))
		return resolved
	}
	return &seen
}

func assertResolvedRawArgv(t *testing.T, seen [][]string, want ...string) {
	t.Helper()
	if len(seen) != 1 {
		t.Fatalf("resolution called %d times, want once: %v", len(seen), seen)
	}
	if strings.Join(seen[0], " ") != strings.Join(want, " ") {
		t.Fatalf("resolution saw %v, want the raw configured argv %v", seen[0], want)
	}
}

func TestPreviewBuildUsesResolvedToolchainArgv(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "built")
	seen := fakePreviewToolchain(t, []string{"sh", "-c", "echo ok > " + marker})
	if err := previewRunBuildArgv(context.Background(), t.TempDir(), []string{"npm", "run", "build"}, nil, ""); err != nil {
		t.Fatalf("build = %v", err)
	}
	assertResolvedRawArgv(t, *seen, "npm", "run", "build")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("resolved command never ran: %v", err)
	}
}

func TestPreviewSpawnUsesResolvedToolchainArgv(t *testing.T) {
	seen := fakePreviewToolchain(t, previewHelperArgv())
	port, err := previewAllocatePort()
	if err != nil {
		t.Fatal(err)
	}
	spawned, err := previewSpawnWithOptions(context.Background(), t.TempDir(), []string{"npm", "run", "start"}, map[string]string{"PREVIEW_HELPER": "1"}, port, t.TempDir(), "", "/")
	if err != nil {
		t.Fatalf("spawn = %v", err)
	}
	assertResolvedRawArgv(t, *seen, "npm", "run", "start")
	if err := previewStopTree(spawned); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewOwnedAdmissionCap(t *testing.T) {
	manager, err := newPreviewManager(t.TempDir(), "wsl", nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.runningOwned = previewMaxRunningOwned
	if err := manager.reserveOwnedStart(); err == nil {
		t.Fatal("third owned preview start must be rejected")
	}
	manager.runningOwned = 0
	var admitted, released bool
	manager.admitHeavy = func(_ context.Context, label string) (func(), error) {
		if label != "preview" {
			t.Fatalf("admission label=%q", label)
		}
		admitted = true
		return func() { released = true }, nil
	}
	release, err := manager.acquireStartAdmission(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !admitted || manager.startingOwned != 1 {
		t.Fatalf("admitted=%v startingOwned=%d", admitted, manager.startingOwned)
	}
	release()
	manager.releaseOwnedStart(false)
	if !released || manager.startingOwned != 0 {
		t.Fatalf("released=%v startingOwned=%d", released, manager.startingOwned)
	}
}

func TestPreviewBasePathRebuildCannotActivateWithoutFreshAcceptance(t *testing.T) {
	t.Setenv("SOLU_PREVIEWS", "1")
	dataDir := t.TempDir()
	manager, err := newPreviewManager(dataDir, "wsl", func() (string, string) { return "https://cloud.invalid", "token" })
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests []string
	manager.client = &http.Client{Transport: previewRoundTripper(func(request *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(request.Body)
		mu.Lock()
		requests = append(requests, request.Method+" "+request.URL.Path+" "+string(raw))
		mu.Unlock()
		status, body := http.StatusOK, "{}"
		if request.Method == http.MethodPost && request.URL.Path == "/api/internal/previews" {
			status = http.StatusCreated
			body = `{"previewId":"` + daemonPreviewTestID + `","url":"https://preview.example/preview/` + daemonPreviewTestID + `/","expiresAt":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano) + `"}`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	buildDir := t.TempDir()
	acceptedFile := filepath.Join(buildDir, "accepted.txt")
	if err := os.WriteFile(acceptedFile, []byte("accepted-before-id"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := previewTreeDigest(buildDir)
	if err != nil {
		t.Fatal(err)
	}
	manager.rebuildBasePath = func(_ context.Context, dir string, _ map[string]string, mountedPath string) (string, error) {
		if mountedPath != "/preview/"+daemonPreviewTestID {
			t.Fatalf("rebuild path=%q", mountedPath)
		}
		if err := os.WriteFile(filepath.Join(dir, "accepted.txt"), []byte("rebuilt-for-real-id"), 0o600); err != nil {
			return "", err
		}
		return previewTreeDigest(dir)
	}
	manager.probeMounted = func(_ context.Context, _, _, id string, _ uint16, _, rebuiltDigest, _, publicBase string) (*browserAcceptance, []byte, error) {
		if publicBase != "basePath" {
			t.Fatalf("public base=%q", publicBase)
		}
		return &browserAcceptance{SchemaVersion: 1, Pass: false, MountedPath: "/preview/" + id + "/", Digest: rebuiltDigest, SourceCommit: "abc"}, nil, fmt.Errorf("fresh acceptance rejected rebuilt output")
	}
	recipe := previewRecipe{
		SchemaVersion: 1, Kind: previewModeSpawn, AppDir: ".", ServeArgv: previewHelperArgv(),
		Env: map[string]string{"PREVIEW_HELPER": "1"}, HealthPath: "/", PublicBase: "basePath",
		AcceptedDigest: digest, AcceptanceArtifactID: "art_old_acceptance", SourceCommit: "abc",
	}
	if _, err := manager.CreateFromRecipe(context.Background(), "wft_basepath", buildDir, recipe); err == nil {
		t.Fatal("basePath creation must fail when the rebuilt output lacks fresh acceptance")
	}
	mu.Lock()
	defer mu.Unlock()
	var sawDormant, sawDelete bool
	for _, request := range requests {
		if strings.Contains(request, "POST /api/internal/previews") && strings.Contains(request, `"state":"dormant"`) {
			sawDormant = true
		}
		if strings.HasPrefix(request, "DELETE /api/internal/previews/") {
			sawDelete = true
		}
		if strings.HasPrefix(request, "PATCH ") {
			t.Fatalf("rebuilt output reached cloud-active without acceptance: %v", requests)
		}
	}
	if !sawDormant || !sawDelete {
		t.Fatalf("cloud lifecycle requests=%v", requests)
	}
}
