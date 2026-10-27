package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func previewRebuildForBasePath(ctx context.Context, appDir, buildDir string, buildArgv []string, extra map[string]string, basePath string) (string, error) {
	if err := previewRunBuildArgv(ctx, appDir, buildArgv, extra, basePath); err != nil {
		return "", err
	}
	return previewBuildDigest(buildDir)
}

func previewProbeMountedForPublicBase(ctx context.Context, worktree, appDir, previewID string, targetPort uint16, healthPath, digest, sourceCommit, publicBase string) (*browserAcceptance, []byte, error) {
	return previewProbeMountedFromOrigin(ctx, worktree, worktree, appDir, previewID, targetPort, healthPath, digest, sourceCommit, publicBase)
}

func previewRunBuildArgv(ctx context.Context, appDir string, argv []string, extra map[string]string, basePath string) error {
	if len(argv) == 0 {
		return nil
	}
	if err := validatePreviewArgv("buildArgv", argv, true); err != nil {
		return err
	}
	env, err := previewCommandEnvForRepo(appDir, extra, basePath)
	if err != nil {
		return err
	}
	// Validation above sees the RAW configured argv; only the exec is wrapped.
	// That ordering is load-bearing — the wrapper's output is a `bash -lc`
	// shell string, which validatePreviewArgv rejects on purpose.
	resolved := previewToolchainArgv(argv)
	cmd := exec.Command(resolved[0], resolved[1:]...)
	cmd.Dir = appDir
	cmd.Env = env
	tail := &tailBuf{limit: 4 << 10}
	cmd.Stdout = tail
	cmd.Stderr = tail
	if err := runCaptureCmd(ctx, cmd); err != nil {
		hint := ""
		if previewBuildInfrastructureUnavailable(err) && (argv[0] == "npm" || argv[0] == "npx") {
			if _, npmErr := resolveNPMBin(); npmErr != nil {
				hint = "; toolchain resolution: " + npmErr.Error()
			} else {
				hint = "; toolchain resolution: node/npm resolved, but the command exited 127; check the login-shell PATH and package manager installation"
			}
		}
		return fmt.Errorf("preview build failed: %w: %s%s", err, strings.TrimSpace(tail.Tail()), hint)
	}
	return nil
}

func previewProbeMountedFromOrigin(ctx context.Context, worktree, originRepo, appDir, previewID string, targetPort uint16, healthPath, digest, sourceCommit, publicBase string) (*browserAcceptance, []byte, error) {
	acceptance, screenshots, err := previewProbeMountedFromOriginWithScreens(ctx, worktree, originRepo, appDir, previewID, targetPort, healthPath, digest, sourceCommit, publicBase)
	return acceptance, screenshots.Dark, err
}

func previewCommandEnvForRepo(dir string, extra map[string]string, basePath string) ([]string, error) {
	if err := validatePreviewEnv(extra); err != nil {
		return nil, err
	}
	env := append([]string(nil), workerEnvForRepo(dir)...)
	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+extra[key])
	}
	// Always set it: relative-base builds must not inherit a stale value from
	// any wrapper, and basePath builds receive only the engine-generated path.
	env = append(env, "SOLU_PREVIEW_BASE_PATH="+basePath)
	return env, nil
}

func previewBuildInfrastructureUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return errors.Is(execErr.Err, exec.ErrNotFound)
	}
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 127
}

func previewProbeMountedFromOriginWithScreens(ctx context.Context, worktree, originRepo, appDir, previewID string, targetPort uint16, healthPath, digest, sourceCommit, publicBase string) (*browserAcceptance, previewAcceptanceScreens, error) {
	var screenshots previewAcceptanceScreens
	if !validDaemonPreviewID(previewID) || targetPort == 0 {
		return nil, screenshots, errors.New("invalid mounted acceptance target")
	}
	prefix := "/preview/" + previewID
	targetBase := ""
	if publicBase == "basePath" {
		targetBase = prefix
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, screenshots, err
	}
	server := &http.Server{Handler: previewMountedProxyHandler(targetPort, prefix, targetBase), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(closeCtx)
	}()

	nodeBin, err := resolveNodeBin()
	if err != nil {
		return nil, screenshots, err
	}
	moduleDir, skipped, err := resolvePlaywrightModuleForApp(worktree, originRepo, appDir)
	if err != nil {
		return nil, screenshots, err
	}
	if moduleDir == "" {
		return nil, screenshots, errors.New(previewPlaywrightUnavailableMessage(appDir, skipped))
	}
	appPath := filepath.Join(worktree, filepath.FromSlash(appDir))
	browsersPath, err := ensurePlaywrightBrowser(ctx, nodeBin, appPath, moduleDir)
	if err != nil {
		return nil, screenshots, err
	}
	tmp, err := os.MkdirTemp("", "solu-preview-accept-")
	if err != nil {
		return nil, screenshots, err
	}
	defer os.RemoveAll(tmp)
	script, err := materializePreviewAcceptScript(tmp)
	if err != nil {
		return nil, screenshots, err
	}
	outDir := filepath.Join(tmp, "out")
	baseURL := "http://" + listener.Addr().String() + prefix + "/"
	cmd := exec.Command(nodeBin, script, "--url", baseURL, "--health-path", healthPath, "--out", outDir)
	cmd.Dir = appPath
	cmd.Env = append(workerEnvForRepo(appPath), "PLAYWRIGHT_MODULE_PATH="+moduleDir)
	_ = browsersPath // presence was verified above; the shared env carries it once
	tail := &tailBuf{limit: 4 << 10}
	cmd.Stdout = tail
	cmd.Stderr = tail
	runErr := runCaptureCmd(ctx, cmd)
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			switch exitErr.ExitCode() {
			case 78:
				guidance := strings.TrimPrefix(previewPlaywrightUnavailableMessage(appDir, skipped), "Playwright is unavailable: ")
				return nil, screenshots, fmt.Errorf("Playwright is unavailable: %s; %s", strings.TrimSpace(tail.Tail()), guidance)
			case 79:
				return nil, screenshots, fmt.Errorf("Playwright browser is unavailable: %s", strings.TrimSpace(tail.Tail()))
			}
		}
		if _, statErr := os.Stat(filepath.Join(outDir, "result.json")); statErr != nil {
			return nil, screenshots, fmt.Errorf("mounted-path browser probe failed: %w: %s", runErr, strings.TrimSpace(tail.Tail()))
		}
	}
	return previewReadProbeOutput(outDir, prefix, digest, sourceCommit, runErr)
}

type previewAcceptanceScreens struct {
	Dark   []byte
	Light  []byte
	Result previewProbeResult
}

func previewPlaywrightUnavailableMessage(appDir string, skipped []string) string {
	return fmt.Sprintf("Playwright is unavailable: no compatible playwright module (major %d) under %s/node_modules in the task worktree or the origin repository — declare playwright as a devDependency in %s/package.json so the worktree `npm ci` provisions it, or install it globally", playwrightMajor, appDir, appDir) + previewResolutionSuffix(skipped)
}

func previewReadProbeOutput(outDir, prefix, digest, sourceCommit string, runErr error) (*browserAcceptance, previewAcceptanceScreens, error) {
	var screenshots previewAcceptanceScreens
	raw, err := os.ReadFile(filepath.Join(outDir, "result.json"))
	if err != nil {
		return nil, screenshots, err
	}
	var result previewProbeResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, screenshots, fmt.Errorf("parse preview acceptance result: %w", err)
	}
	screenshots.Result = result
	acceptance := &browserAcceptance{
		SchemaVersion: 1, Pass: result.Pass, MountedPath: prefix + "/", Digest: digest,
		SourceCommit: sourceCommit, ConsoleErrors: result.ConsoleErrors, FailedRequests: result.FailedRequests,
	}
	screenshots.Dark, err = os.ReadFile(filepath.Join(outDir, "screenshot.png"))
	if err != nil {
		result.Pass = false
		result.ConsoleErrors = append(result.ConsoleErrors, "dark preview acceptance screenshot missing: "+err.Error())
	}
	var lightErr error
	screenshots.Light, lightErr = os.ReadFile(filepath.Join(outDir, "screenshot-light.png"))
	if lightErr != nil {
		result.Pass = false
		result.ConsoleErrors = append(result.ConsoleErrors, "light preview acceptance screenshot missing: "+lightErr.Error())
	}
	if err != nil || lightErr != nil {
		screenshots.Result = result
		acceptance.Pass = false
		acceptance.ConsoleErrors = result.ConsoleErrors
		if validateErr := validateBrowserAcceptance(*acceptance); validateErr != nil {
			return nil, screenshots, validateErr
		}
		return acceptance, screenshots, &previewAcceptanceFailedError{Result: result}
	}
	if err := validateBrowserAcceptance(*acceptance); err != nil {
		return nil, screenshots, err
	}
	if !result.Pass || runErr != nil {
		return acceptance, screenshots, &previewAcceptanceFailedError{Result: result}
	}
	return acceptance, screenshots, nil
}

type previewProbeResult struct {
	Pass              bool     `json:"pass"`
	ConsoleErrors     []string `json:"consoleErrors"`
	FailedRequests    []string `json:"failedRequests"`
	BodyTextLength    int      `json:"bodyTextLength"`
	HealthStatus      int      `json:"healthStatus"`
	DocumentStatus    int      `json:"documentStatus"`
	PreviewMarker     bool     `json:"previewMarker"`
	NavigationCount   int      `json:"navigationCount"`
	StaleBundleBanner bool     `json:"staleBundleBanner"`
}

func previewResolutionSuffix(skipped []string) string {
	if len(skipped) == 0 {
		return ""
	}
	return "; resolution skipped: " + strings.Join(skipped, ", ")
}

// previewAcceptanceFailedError distinguishes an executed browser/build lane
// from infrastructure that prevented the lane from running. The workflow
// engine gates the former and either reports or fails closed on the latter,
// according to the repository's previewRequired policy.
type previewAcceptanceFailedError struct {
	Result previewProbeResult
	Cause  string
}

func (e *previewAcceptanceFailedError) Error() string {
	if e == nil {
		return "preview acceptance failed"
	}
	if strings.TrimSpace(e.Cause) != "" {
		return e.Cause
	}
	result := e.Result
	detail := fmt.Sprintf("mounted-path browser acceptance failed (document=%d health=%d bodyText=%d navigations=%d staleBundleBanner=%t consoleErrors=%d failedRequests=%d)", result.DocumentStatus, result.HealthStatus, result.BodyTextLength, result.NavigationCount, result.StaleBundleBanner, len(result.ConsoleErrors), len(result.FailedRequests))
	appendEvidence := func(label string, entries []string) {
		if len(entries) == 0 {
			return
		}
		limit := len(entries)
		if limit > 5 {
			limit = 5
		}
		detail += "; " + label + ": " + strings.Join(entries[:limit], " | ")
	}
	appendEvidence("console", result.ConsoleErrors)
	appendEvidence("requests", result.FailedRequests)
	return detail
}
