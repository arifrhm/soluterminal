package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeProbe is the test injection point for paneForegroundCmd. The
// returned slice is read top-to-bottom; once exhausted the last entry
// is repeated indefinitely so a steady-state test doesn't need to
// preallocate one entry per tick.
type fakeProbe struct {
	mu      sync.Mutex
	calls   int
	entries []probeResult
}

type probeResult struct {
	fgPid int
	cmd   string
	ok    bool
}

func (f *fakeProbe) probe(rootPid int) (int, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.entries) == 0 {
		return 0, "", false
	}
	idx := f.calls
	if idx >= len(f.entries) {
		idx = len(f.entries) - 1
	}
	f.calls++
	r := f.entries[idx]
	return r.fgPid, r.cmd, r.ok
}

// busyPane returns a Pane that computeState classifies as StateBusy on
// every check. The PTY ring buffer holds plain shell output so neither
// looksLikeClaudePrompt nor the choice-prompt detector matches; busy
// status comes from a constantly-fresh LastActivityMs (refreshed by the
// goroutine started below).
func busyPane(t *testing.T, id string, pid int) *Pane {
	t.Helper()
	p := testPaneWithTail(t, id, id, "$ ls\nREADME.md  go.mod  daemon\n$ ")
	p.Pid = pid
	atomic.StoreInt64(&p.LastActivityMs, time.Now().UnixMilli())
	return p
}

// keepBusy keeps p.LastActivityMs within the StateBusy threshold for the
// duration of the test. Returns a stop func.
func keepBusy(p *Pane) func() {
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				atomic.StoreInt64(&p.LastActivityMs, time.Now().UnixMilli())
			}
		}
	}()
	return func() { close(stop) }
}

// collectLongRunning subscribes to bus and returns every cmd.long_running
// event published until stop is closed.
func collectLongRunning(bus *uiBus, stop <-chan struct{}) <-chan uiEvent {
	out := make(chan uiEvent, 16)
	sub := bus.subscribe()
	go func() {
		defer close(out)
		for {
			select {
			case <-stop:
				return
			case ev, ok := <-sub:
				if !ok {
					return
				}
				if ev.Type == "cmd.long_running" {
					select {
					case out <- ev:
					case <-stop:
						return
					}
				}
			}
		}
	}()
	return out
}

// TestLongRunning_NoFire_WhenRootIsForeground proves the bug fix: a pane
// whose tpgid == rootPid (bash at prompt, claude in its event loop) does
// not produce cmd.long_running no matter how long it stays "busy" by PTY
// activity. Without this gate, every chatty shell prompt fires within 30s.
func TestLongRunning_NoFire_WhenRootIsForeground(t *testing.T) {
	bus := newUIBus()
	p := busyPane(t, "pane_idle_shell", 1234)
	mgr := testManagerWithPane(p)

	probe := &fakeProbe{entries: []probeResult{{0, "", false}}} // root IS foreground

	// Force the long-running threshold low so we don't sit in the test
	// for 30 real seconds. The threshold is a package-level const, so
	// reuse a small tick instead — the watcher only fires after
	// now.Sub(start) >= longRunningThreshold. We need at least
	// longRunningThreshold of wall-clock for a fire to be possible.
	// Workaround: use a long tick that subsumes the threshold and verify
	// no event after a window larger than threshold.
	//
	// We can't shrink longRunningThreshold without making it injectable.
	// Instead, we drive enough ticks that a buggy implementation WOULD
	// fire — and assert it doesn't.

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	releaseBusy := keepBusy(p)
	defer releaseBusy()

	stop := make(chan struct{})
	events := collectLongRunning(bus, stop)
	defer close(stop)

	// Tiny threshold so a buggy implementation would have fired many
	// times over within the test window — and a correct one still
	// doesn't, because the foreground gate excludes this pane.
	go runLongRunningWatcher(ctx, mgr, bus, probe.probe, 5*time.Millisecond, 30*time.Millisecond)

	timeout := time.NewTimer(200 * time.Millisecond)
	defer timeout.Stop()
	select {
	case ev := <-events:
		t.Fatalf("unexpected cmd.long_running fired: %+v", ev)
	case <-timeout.C:
	}

	// The real assertion is above: no event fired. This only guards against a
	// vacuous pass where the watcher never ran at all, so it needs a floor, not
	// a rate. Requiring 10 calls in a fixed 200ms window measured SCHEDULER
	// LUCK: on a loaded machine (parallel workflow tasks, a web build) the
	// ticker goroutine gets starved and lands 5 — a green build and a red one
	// differ only by what else the box was doing. Wait for the floor instead of
	// sampling once.
	deadline := time.Now().Add(2 * time.Second)
	for {
		probe.mu.Lock()
		calls := probe.calls
		probe.mu.Unlock()
		if calls >= 3 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("probe called only %d times in 2s — watcher loop did not run", calls)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestLongRunning_FiresOnRealChild proves the watcher still fires when
// the foreground process is a genuine child of the pane's root (bash
// running `make`, etc.). The real Cmd from the probe — "npm test" —
// reaches the event, NOT the pane's spawn argv ("bash"). That's the
// second half of the bug fix: alerts now say what's actually running.
func TestLongRunning_FiresOnRealChild(t *testing.T) {
	t.Parallel()
	bus := newUIBus()
	p := busyPane(t, "pane_npm", 4242)
	mgr := testManagerWithPane(p)

	probe := &fakeProbe{entries: []probeResult{{9999, "npm test", true}}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	releaseBusy := keepBusy(p)
	defer releaseBusy()

	events := bus.subscribe()
	defer bus.unsubscribe(events)

	const threshold = 30 * time.Millisecond
	ticks := make(chan time.Time)
	go runLongRunningWatcherTicks(ctx, mgr, bus, probe.probe, ticks, threshold)

	// Use exact timestamps instead of a real 5ms ticker. time.Ticker may drop
	// ticks when the race-enabled full suite is under scheduler pressure, which
	// made the one-second assertion below depend on machine load rather than the
	// watcher behavior. Two ticks are sufficient to establish the foreground
	// run and advance it through the firing threshold.
	start := time.Unix(1, 0)
	for _, tick := range []time.Time{start, start.Add(threshold)} {
		atomic.StoreInt64(&p.LastActivityMs, time.Now().UnixMilli())
		select {
		case ticks <- tick:
		case <-time.After(2 * time.Second):
			t.Fatal("watcher stopped consuming deterministic ticks")
		}
	}

	select {
	case ev := <-events:
		if ev.Cmd != "npm test" {
			t.Errorf("expected cmd=%q, got %q", "npm test", ev.Cmd)
		}
		if ev.PaneID != "pane_npm" {
			t.Errorf("expected paneId=%q, got %q", "pane_npm", ev.PaneID)
		}
		if got, want := ev.Ts, start.Add(threshold).UnixMilli(); got != want {
			t.Errorf("event timestamp=%d, want %d", got, want)
		}
		if got, want := ev.ElapsedMs, int64(threshold/time.Millisecond); got != want {
			t.Errorf("elapsed %dms, want %dms", got, want)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timed out waiting for cmd.long_running event")
	}
}

// TestLongRunning_RestartsOnForegroundChange proves that when the
// foreground command changes (one command finishes, another starts)
// the timer restarts — otherwise a long shell session running many
// short commands would all be counted as one busy window and the second
// one would inherit the first one's elapsed time.
func TestLongRunning_RestartsOnForegroundChange(t *testing.T) {
	t.Parallel()
	bus := newUIBus()
	p := busyPane(t, "pane_rotate", 7000)
	mgr := testManagerWithPane(p)

	// First five ticks: foreground = pid 9001 (cmd "make"). Then it
	// changes to pid 9002 (cmd "go test"). The internal timer should
	// reset on the change. Drive exact timestamps here: a real
	// time.Ticker may drop ticks under scheduler load, so five probe
	// calls do not reliably mean only 25ms of elapsed wall-clock time.
	probe := &fakeProbe{entries: []probeResult{
		{9001, "make", true},
		{9001, "make", true},
		{9001, "make", true},
		{9001, "make", true},
		{9001, "make", true},
		{9002, "go test", true},
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	releaseBusy := keepBusy(p)
	defer releaseBusy()

	stop := make(chan struct{})
	events := collectLongRunning(bus, stop)
	defer close(stop)

	const threshold = 30 * time.Millisecond
	ticks := make(chan time.Time)
	go runLongRunningWatcherTicks(ctx, mgr, bus, probe.probe, ticks, threshold)

	start := time.Unix(1, 0)
	for i := 0; i < 12; i++ {
		ticks <- start.Add(time.Duration(i) * 5 * time.Millisecond)
	}

	select {
	case ev := <-events:
		// The only event we should see is for "go test", not "make".
		// Seeing "make" means the timer didn't reset on the foreground
		// change — the bug we're guarding against.
		if ev.Cmd != "go test" {
			t.Errorf("expected go test event (timer reset on fg change), got %q", ev.Cmd)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timed out waiting for cmd.long_running for go test")
	}
}
