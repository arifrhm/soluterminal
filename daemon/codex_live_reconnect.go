package main

import (
	"runtime"
	"sync"
	"time"
)

// A queued ("when idle") Codex reconnect. The user asks once; the daemon keeps
// re-running the same guarded attempt until the pane is at an idle prompt, so
// nobody has to watch a busy agent pane and click again. Queued reconnects are
// in memory only: a daemon restart drops them and the badge offers it again.

const (
	codexReconnectQuietNow   = 2 * time.Second
	codexReconnectQuietArmed = 10 * time.Second // never yank a pane the user just touched
	codexReconnectArmPoll    = 3 * time.Second
	codexReconnectArmTTL     = 4 * time.Hour
	// Unverified conversation identity normally settles within seconds.
	codexReconnectSettleAttempts = 20
)

type codexReconnectArms struct {
	mu     sync.Mutex
	armed  map[string]chan struct{}
	failed map[string]string // last permanent refusal, until re-armed or cleared
}

func (r *codexReconnectArms) arm(id string) (chan struct{}, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.armed == nil {
		r.armed, r.failed = map[string]chan struct{}{}, map[string]string{}
	}
	delete(r.failed, id)
	if _, ok := r.armed[id]; ok {
		return nil, false
	}
	stop := make(chan struct{})
	r.armed[id] = stop
	return stop, true
}

func (r *codexReconnectArms) disarm(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if stop, ok := r.armed[id]; ok {
		close(stop)
		delete(r.armed, id)
	}
	delete(r.failed, id)
}

func (r *codexReconnectArms) finish(id string, stop chan struct{}, failure string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.armed[id] != stop {
		return // disarmed or replaced meanwhile
	}
	delete(r.armed, id)
	if failure != "" {
		r.failed[id] = failure
	}
}

func (r *codexReconnectArms) clearFailure(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.failed, id)
}

// prune drops remembered failures for panes that no longer exist.
func (r *codexReconnectArms) prune(exists func(id string) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id := range r.failed {
		if !exists(id) {
			delete(r.failed, id)
		}
	}
}

func (r *codexReconnectArms) status(id string) (armed bool, failure string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, armed = r.armed[id]
	return armed, r.failed[id]
}

func (a *apiServer) armCodexReconnect(id, thread string) {
	a.codexReconnects.prune(func(id string) bool { _, ok := a.mgr.Get(id); return ok })
	stop, started := a.codexReconnects.arm(id)
	if !started {
		return
	}
	go a.runCodexReconnectArm(id, thread, stop)
}

func (a *apiServer) runCodexReconnectArm(id, thread string, stop chan struct{}) {
	failure := ""
	defer func() { a.codexReconnects.finish(id, stop, failure) }()
	deadline := time.NewTimer(codexReconnectArmTTL)
	defer deadline.Stop()
	tick := time.NewTicker(codexReconnectArmPoll)
	defer tick.Stop()
	settling := 0
	for {
		select {
		case <-stop:
			return
		case <-deadline.C:
			failure = "the pane did not reach an idle prompt within 4 hours"
			return
		case <-tick.C:
		}
		p, ok := a.mgr.Get(id)
		if !ok || p.Snapshot().Status != PaneRunning {
			return
		}
		_, refusal := a.tryCodexLiveReconnect(p, codexReconnectQuietArmed, thread)
		if refusal == nil {
			return
		}
		if refusal.settling {
			settling++
		} else {
			settling = 0
		}
		if !refusal.retryable || settling >= codexReconnectSettleAttempts {
			failure = refusal.message
			return
		}
	}
}

// codexReconnectInvocation decides what a reconnect would launch and where,
// from one read of the pane's running Codex.
func codexReconnectInvocation(p *Pane, snap PaneSnapshot) (cmd []string, cwd string, shell, ok bool) {
	fg, running := codexForegroundOf(p, snap.CodexThreadID)
	// On Linux a running Codex that cannot be identified is not replaced on
	// the strength of the pane's recorded command alone.
	unproven := !running && runtime.GOOS == "linux" && p != nil && p.Pid > 0 && p.HasCodexDescendant()
	return codexReconnectChoice(snap, fg, running, unproven)
}

// codexReconnectChoice: Codex at a shell prompt (typed there, or relaunched
// after it exited) is rebuilt from the running process under its replay
// guards, since the pane's recorded command and environment no longer
// describe it. Otherwise the pane's own command is replayed where its Codex
// is running.
func codexReconnectChoice(snap PaneSnapshot, fg codexForeground, running, unproven bool) (cmd []string, cwd string, shell, ok bool) {
	if unproven {
		return nil, "", false, false
	}
	if running && fg.underShell {
		if fg.args == nil {
			return nil, "", true, false
		}
		cmd, ok = codexLiveResumeCommand(fg.args, snap.CodexThreadID)
		return cmd, fg.cwd, true, ok
	}
	cmd, ok = codexLiveResumeCommand(snap.Cmd, snap.CodexThreadID)
	cwd = snap.Cwd
	if running {
		cwd = fg.cwd
	}
	return cmd, cwd, false, ok
}

// decorateCodexPaneAuth adds what the pane list cannot derive from the pure
// projection: Codex typed in a shell pane (proven from its foreground process)
// and any queued reconnect.
func (a *apiServer) decorateCodexPaneAuth(info *codexPaneAuthInfo, p *Pane, snap PaneSnapshot) {
	if info == nil || info.State != "reconnect-required" {
		// Connected (or otherwise moved on): an old failure no longer applies.
		a.codexReconnects.clearFailure(snap.ID)
		return
	}
	// Team and disabled blockers stand; otherwise the running process decides.
	if info.Blocker == "" || info.Blocker == "custom-launch" {
		_, _, shell, ok := codexReconnectInvocation(p, snap)
		info.CanReconnect, info.Blocker, info.ReconnectMode = ok, "", ""
		if !ok {
			info.Blocker = "custom-launch"
		} else if shell {
			info.ReconnectMode = "shell"
		}
	}
	info.ReconnectArmed, info.ReconnectError = a.codexReconnects.status(snap.ID)
}
