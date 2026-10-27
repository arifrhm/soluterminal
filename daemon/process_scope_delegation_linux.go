//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

var processScopeDelegation struct {
	sync.Once
	err error
}

// A Windows wsl.exe launch starts in /non-systemd, whose cgroup is not
// delegated to the user. Ask the user's systemd manager to adopt this exact
// daemon PID into a transient delegated scope. The PID and all open handles
// remain unchanged, including the updater's successor handshake. Only this
// process moves; existing panes and legacy workers stay where they were.
func delegateProcessScopeParent() error {
	processScopeDelegation.Do(func() {
		id, err := newProcessScopeID()
		if err != nil {
			processScopeDelegation.err = err
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "busctl", "--user", "--timeout=4s", "call",
			"org.freedesktop.systemd1", "/org/freedesktop/systemd1",
			"org.freedesktop.systemd1.Manager", "StartTransientUnit", "ssa(sv)a(sa(sv))",
			"soluterminal-processes-"+id+".scope", "fail", "3",
			"PIDs", "au", "1", strconv.Itoa(os.Getpid()),
			"Delegate", "b", "true", "CollectMode", "s", "inactive-or-failed", "0")
		if out, err := cmd.CombinedOutput(); err != nil {
			processScopeDelegation.err = fmt.Errorf("delegate daemon process scope: %w: %s", err, strings.TrimSpace(string(out)))
			return
		}
		// StartTransientUnit returns a job, before migration necessarily finishes.
		for ctx.Err() == nil {
			parent, err := processScopeParent()
			if err == nil && processScopeParentWritable(parent) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		processScopeDelegation.err = fmt.Errorf("delegated daemon cgroup did not become writable: %w", ctx.Err())
	})
	return processScopeDelegation.err
}
