package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	previewHealthGateTimeout = 30 * time.Second
	previewStopGrace         = 5 * time.Second
	previewLogMaxBytes       = int64(1 << 20)
)

// previewToolchainArgv resolves a repository-configured preview argv through a
// login shell (see runtime_login_shell.go) before it is exec'd, because the
// daemon's own PATH has no nvm. It is a var so tests can fake resolution and
// prove the RESOLVED command is what actually runs, without depending on the
// machine having npm installed.
var previewToolchainArgv = loginShellWrapArgv

type previewSpawnProcess struct {
	Process *os.Process
	Done    <-chan error
	LogPath string
}

type previewRotatingLog struct {
	ctx  context.Context
	mu   sync.Mutex
	path string
	file *os.File
	size int64
}

func newPreviewRotatingLog(path string) (*previewRotatingLog, error) {
	return newPreviewRotatingLogContext(context.Background(), path)
}

func newPreviewRotatingLogContext(ctx context.Context, path string) (*previewRotatingLog, error) {
	_, release, err := retirementCleanupContext(ctx, nil, "", path)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &previewRotatingLog{ctx: context.WithoutCancel(ctx), path: path, file: file}, nil
}

func (l *previewRotatingLog) Write(payload []byte) (int, error) {
	// Admission precedes the log mutex. A held or contended material guard
	// disables destructive rotation, while allowing truthful draining output.
	_, release, guardErr := retirementCleanupContext(l.ctx, nil, "", l.path)
	if guardErr == nil {
		defer release()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	originalLen := len(payload)
	if l.file == nil {
		return 0, os.ErrClosed
	}
	if guardErr == nil && int64(len(payload)) > previewLogMaxBytes {
		payload = payload[len(payload)-int(previewLogMaxBytes):]
	}
	if guardErr == nil && l.size+int64(len(payload)) > previewLogMaxBytes {
		_ = l.file.Close()
		_ = os.Remove(l.path + ".1")
		_ = os.Rename(l.path, l.path+".1")
		file, err := os.OpenFile(l.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			l.file = nil
			return 0, err
		}
		l.file = file
		l.size = 0
	}
	n, err := l.file.Write(payload)
	l.size += int64(n)
	if err == nil && n == len(payload) {
		return originalLen, nil
	}
	return n, err
}

func (l *previewRotatingLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

func previewAllocatePort() (uint16, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	if err := listener.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

func previewSpawnEnv(extraEnv map[string]string, port uint16, basePath string) ([]string, error) {
	return previewSpawnEnvForRepo("", extraEnv, port, basePath)
}

func previewSpawnEnvForRepo(dir string, extraEnv map[string]string, port uint16, basePath string) ([]string, error) {
	if err := validatePreviewEnv(extraEnv); err != nil {
		return nil, err
	}
	env := append([]string(nil), workerEnvForRepo(dir)...)
	keys := make([]string, 0, len(extraEnv))
	for key := range extraEnv {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+extraEnv[key])
	}
	env = append(env, "PORT="+strconv.Itoa(int(port)))
	if basePath != "" {
		env = append(env, "SOLU_PREVIEW_BASE_PATH="+basePath)
	}
	return env, nil
}

func previewSpawn(ctx context.Context, buildDir string, argv []string, extraEnv map[string]string, port uint16) (*previewSpawnProcess, error) {
	return previewSpawnWithOptions(ctx, buildDir, argv, extraEnv, port, os.TempDir(), "", "/")
}

func previewSpawnWithOptions(ctx context.Context, buildDir string, argv []string, extraEnv map[string]string, port uint16, logDir, basePath, healthPath string) (*previewSpawnProcess, error) {
	if err := validatePreviewArgv("serveArgv", argv, true); err != nil {
		return nil, err
	}
	if port == 0 {
		return nil, errors.New("preview spawn requires a loopback port")
	}
	info, err := os.Stat(buildDir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("preview build directory is unavailable")
	}
	env, err := previewSpawnEnvForRepo(buildDir, extraEnv, port, basePath)
	if err != nil {
		return nil, err
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("preview-%d.log", time.Now().UnixNano()))
	logWriter, err := newPreviewRotatingLogContext(ctx, logPath)
	if err != nil {
		return nil, err
	}
	// Validation above sees the RAW configured argv; only the exec is wrapped.
	// That ordering is load-bearing — the wrapper's output is a `bash -lc`
	// shell string, which validatePreviewArgv rejects on purpose. The script
	// ends in `exec`, so bash is replaced in place: cmd.Process is the serve
	// command itself, leaving previewProcessStartIdentity and
	// previewStopTree/killChildTree semantics unchanged.
	resolved := previewToolchainArgv(argv)
	cmd := exec.Command(resolved[0], resolved[1:]...)
	cmd.Dir = buildDir
	cmd.Env = env
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	applyDetached(cmd)
	finishScope, startErr := startWorkflowCommand(ctx, cmd)
	if startErr != nil {
		_ = logWriter.Close()
		return nil, fmt.Errorf("start preview process: %w", startErr)
	}
	lowerProcessPriority(cmd.Process)
	done := make(chan error, 1)
	go func() {
		defer finishScope()
		done <- waitOwnedCommand(cmd)
		_ = logWriter.Close()
		close(done)
	}()
	runtime := &previewSpawnProcess{Process: cmd.Process, Done: done, LogPath: logPath}
	if err := previewWaitHealthyAtBase(ctx, port, basePath, healthPath, previewHealthGateTimeout, done); err != nil {
		_ = previewStopTree(runtime)
		return nil, fmt.Errorf("preview health gate: %w: %s", err, previewLogTail(logPath, 4096))
	}
	return runtime, nil
}

func previewWaitHealthy(ctx context.Context, port uint16, healthPath string, timeout time.Duration, done <-chan error) error {
	return previewWaitHealthyAtBase(ctx, port, "", healthPath, timeout, done)
}

func previewWaitHealthyAtBase(ctx context.Context, port uint16, basePath, healthPath string, timeout time.Duration, done <-chan error) error {
	if err := validatePreviewHealthPath(healthPath); err != nil {
		return err
	}
	gateCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext:       (&net.Dialer{Timeout: 500 * time.Millisecond}).DialContext,
		},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	url := "http://127.0.0.1:" + strconv.Itoa(int(port)) + strings.TrimSuffix(basePath, "/") + healthPath
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, _ := http.NewRequestWithContext(gateCtx, http.MethodGet, url, nil)
		if response, err := client.Do(request); err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 8<<10))
			_ = response.Body.Close()
			if response.StatusCode < http.StatusBadRequest {
				return nil
			}
		}
		select {
		case err, ok := <-done:
			if !ok || err == nil {
				return errors.New("preview process exited before becoming healthy")
			}
			return fmt.Errorf("preview process exited: %w", err)
		case <-gateCtx.Done():
			return gateCtx.Err()
		case <-ticker.C:
		}
	}
}

func previewStopTree(runtime *previewSpawnProcess) error {
	if runtime == nil || runtime.Process == nil {
		return nil
	}
	_ = terminateChildTree(runtime.Process)
	deadline := time.NewTimer(previewStopGrace)
	defer deadline.Stop()
	if runtime.Done != nil {
		select {
		case <-runtime.Done:
			return killChildTree(runtime.Process)
		case <-deadline.C:
		}
	} else {
		for processAlive(runtime.Process.Pid) {
			select {
			case <-deadline.C:
				goto kill
			case <-time.After(100 * time.Millisecond):
			}
		}
		return killChildTree(runtime.Process)
	}
kill:
	return killChildTree(runtime.Process)
}

func previewLogTail(path string, limit int64) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	if info.Size() > limit {
		_, _ = file.Seek(-limit, io.SeekEnd)
	}
	raw, _ := io.ReadAll(io.LimitReader(file, limit))
	return strings.TrimSpace(string(raw))
}
