//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type windowsHostProbe struct {
	mu             sync.Mutex
	last           hostMemSample
	interopSock    string
	interopAt      time.Time
	run            func(context.Context, []string) (string, error)
	clock          func() time.Time
	commandTimeout time.Duration
	disabled       bool
}

func newWfWindowsHostProbe() *windowsHostProbe {
	return newWfWindowsHostProbeFor(isInsideWSL(), os.Getenv("SOLU_WORKFLOW_HOST_PROBE"))
}

func newWfWindowsHostProbeFor(insideWSL bool, setting string) *windowsHostProbe {
	if !insideWSL {
		return nil
	}
	p := &windowsHostProbe{clock: time.Now}
	switch strings.ToLower(strings.TrimSpace(setting)) {
	case "0", "false", "off", "no":
		p.disabled = true
		p.last = hostMemSample{Status: "disabled", Provenance: "windows"}
	}
	return p
}

func (p *windowsHostProbe) now() time.Time {
	if p != nil && p.clock != nil {
		return p.clock()
	}
	return time.Now()
}

func (p *windowsHostProbe) effectiveCommandTimeout() time.Duration {
	if p != nil && p.commandTimeout > 0 {
		return p.commandTimeout
	}
	return 2 * time.Second
}

// Start refreshes the cache asynchronously. No controller or request caller
// ever waits for WSL interop or PowerShell.
func (p *windowsHostProbe) Start(ctx context.Context) {
	if p == nil || p.disabled {
		return
	}
	go func() {
		p.Refresh(ctx)
		t := time.NewTicker(hostProbeTTL)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				p.Refresh(ctx)
			}
		}
	}()
}

// Refresh is exported to package tests and obeys the same TTL as Start.
func (p *windowsHostProbe) Refresh(parent context.Context) {
	if p == nil || p.disabled {
		return
	}
	now := p.now()
	p.mu.Lock()
	if !p.last.SampledAt.IsZero() && now.Sub(p.last.SampledAt) < hostProbeTTL {
		p.mu.Unlock()
		return
	}
	run := p.run
	p.mu.Unlock()

	env := os.Environ()
	if run == nil {
		ps, err := resolvePowerShell()
		if err != nil {
			p.storeFailure(now, "unavailable", err)
			return
		}
		// Socket discovery is intentionally outside the 2s command box. It is
		// refreshed rarely and this whole method already runs off the caller path.
		p.mu.Lock()
		sock, socketAt := p.interopSock, p.interopAt
		p.mu.Unlock()
		if sock == "" || now.Sub(socketAt) >= 10*time.Minute {
			sock = liveWSLInterop(ps)
			p.mu.Lock()
			p.interopSock, p.interopAt = sock, now
			p.mu.Unlock()
		}
		if sock != "" {
			env = envWithInterop(env, sock)
		}
		run = func(ctx context.Context, env []string) (string, error) {
			cmd := exec.CommandContext(ctx, ps, "-NoProfile", "-NonInteractive", "-Command", `$o=Get-CimInstance Win32_OperatingSystem; Write-Output "$($o.FreePhysicalMemory) $($o.TotalVisibleMemorySize)"`)
			cmd.Env = env
			cmd.WaitDelay = 500 * time.Millisecond
			cmd.Cancel = func() error {
				if cmd.Process == nil {
					return nil
				}
				return killChildTree(cmd.Process)
			}
			out, err := cmd.CombinedOutput()
			return string(out), err
		}
	}
	ctx, cancel := context.WithTimeout(parent, p.effectiveCommandTimeout())
	type runResult struct {
		out string
		err error
	}
	done := make(chan runResult, 1)
	go func() { out, err := run(ctx, env); done <- runResult{out, err} }()
	var out string
	var err error
	select {
	case result := <-done:
		out, err = result.out, result.err
	case <-ctx.Done():
		err = ctx.Err()
	}
	cancel()
	if err != nil {
		status := "error"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			status = "timeout"
		}
		failure := err
		if status == "error" {
			if line := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0]); line != "" {
				failure = errors.New(line)
			}
		}
		p.storeFailure(now, status, failure)
		return
	}
	fields := strings.Fields(out)
	if len(fields) < 2 {
		p.storeFailure(now, "error", fmt.Errorf("unexpected PowerShell output"))
		return
	}
	freeKB, e1 := strconv.ParseInt(fields[0], 10, 64)
	totalKB, e2 := strconv.ParseInt(fields[1], 10, 64)
	if e1 != nil || e2 != nil || freeKB < 0 || totalKB <= 0 {
		p.storeFailure(now, "error", fmt.Errorf("invalid PowerShell memory values"))
		return
	}
	p.mu.Lock()
	p.last = hostMemSample{FreeMB: freeKB / 1024, TotalMB: totalKB / 1024, SampledAt: now, Status: "ok", Provenance: "windows"}
	p.mu.Unlock()
}

func (p *windowsHostProbe) storeFailure(at time.Time, status string, err error) {
	msg := ""
	if err != nil {
		msg = strings.TrimSpace(strings.SplitN(err.Error(), "\n", 2)[0])
		if len(msg) > 200 {
			msg = msg[:200]
		}
	}
	p.mu.Lock()
	p.last = hostMemSample{SampledAt: at, Status: status, Err: msg, Provenance: "windows"}
	p.mu.Unlock()
}

func (p *windowsHostProbe) Sample(now time.Time) hostMemSample {
	if p == nil {
		return hostMemSample{Status: "n/a"}
	}
	p.mu.Lock()
	out := p.last
	disabled := p.disabled
	p.mu.Unlock()
	if disabled {
		return hostMemSample{Status: "disabled", Provenance: "windows"}
	}
	if out.Status == "" {
		out.Status, out.Provenance = "unavailable", "windows"
	}
	if out.Status == "ok" && !out.Fresh(now) {
		out.Status = "stale"
	}
	return out
}
