//go:build linux

package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestWfWindowsHostProbeTimeoutCacheAndStaleness(t *testing.T) {
	clk := newWfFakeClock()
	var runs atomic.Int32
	p := &windowsHostProbe{clock: clk.Now, run: func(context.Context, []string) (string, error) { runs.Add(1); select {} }}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	started := time.Now()
	p.Refresh(ctx)
	cancel()
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("probe exceeded box: %v", elapsed)
	}
	if got := p.Sample(clk.Now()).Status; got != "timeout" {
		t.Fatalf("timeout status=%q", got)
	}
	p = &windowsHostProbe{clock: clk.Now, run: func(context.Context, []string) (string, error) { runs.Add(1); return "8388608 16777216", nil }}
	p.Refresh(context.Background())
	firstRuns := runs.Load()
	if got := p.Sample(clk.Now()); got.Status != "ok" || got.FreeMB != 8192 {
		t.Fatalf("sample=%+v", got)
	}
	clk.Advance(29 * time.Second)
	p.Refresh(context.Background())
	if runs.Load() != firstRuns {
		t.Fatal("cache executed inside TTL")
	}
	clk.Advance(62 * time.Second)
	if got := p.Sample(clk.Now()).Status; got != "stale" {
		t.Fatalf("stale status=%q", got)
	}
	p = &windowsHostProbe{clock: clk.Now, run: func(context.Context, []string) (string, error) {
		return "CIM service unavailable\nmore detail", errors.New("exit status 1")
	}}
	p.Refresh(context.Background())
	if got := p.Sample(clk.Now()); got.Status != "error" || got.Err != "CIM service unavailable" {
		t.Fatalf("PowerShell error sample=%+v", got)
	}
}

func TestWfWindowsHostProbeDisabledAndNonWSLConstruction(t *testing.T) {
	if got := newWfWindowsHostProbeFor(false, ""); got != nil {
		t.Fatalf("non-WSL probe=%+v want nil", got)
	}
	p := newWfWindowsHostProbeFor(true, "off")
	if p == nil || p.Sample(time.Now()).Status != "disabled" {
		t.Fatalf("disabled probe=%+v sample=%+v", p, p.Sample(time.Now()))
	}
	var runs atomic.Int32
	p.run = func(context.Context, []string) (string, error) { runs.Add(1); return "1 2", nil }
	p.Refresh(context.Background())
	if runs.Load() != 0 {
		t.Fatal("disabled probe executed PowerShell")
	}
}

func TestWfWindowsHostProbeEnforcesInternalTimeout(t *testing.T) {
	clk := newWfFakeClock()
	p := &windowsHostProbe{
		clock:          clk.Now,
		commandTimeout: 25 * time.Millisecond,
		run: func(ctx context.Context, _ []string) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	}
	done := make(chan struct{})
	go func() {
		p.Refresh(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("probe did not enforce its own timeout with an unbounded parent context")
	}
	if got := p.Sample(clk.Now()).Status; got != "timeout" {
		t.Fatalf("internally timed out probe status=%q", got)
	}
	if got := (&windowsHostProbe{}).effectiveCommandTimeout(); got != 2*time.Second {
		t.Fatalf("default command timeout=%v want 2s", got)
	}
}
