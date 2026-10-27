//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Every signal uses a pidfd for this exact process generation. The watchdog
// releases all stops even if a provider read or daemon persistence stalls.
// The owning agent itself is never stopped.
type ownerFrozenTree struct {
	mu          sync.Mutex
	root        int
	fds         map[int]int
	deadline    time.Time
	released    bool
	timer       *time.Timer
	watchdog    *os.File
	watchdogPID int
}

func freezeOwnerChildren(root int, deadline time.Time) (*ownerFrozenTree, error) {
	return freezeOwnerChildrenWithWatchdog(root, deadline, startOwnerFreezeWatchdog)
}

// A dependency seam exercises helper death between STOP and ACK without a
// production fault-injection switch or mutable hook.
func freezeOwnerChildrenWithWatchdog(root int, deadline time.Time, start func(time.Time) (*os.File, int, error)) (*ownerFrozenTree, error) {
	if root != os.Getpid() || !deadline.After(time.Now()) {
		return nil, fmt.Errorf("retirement must run inside the exact owning agent")
	}
	watchdog, watchdogPID, err := start(deadline)
	if err != nil {
		return nil, err
	}
	f := &ownerFrozenTree{root: root, fds: map[int]int{}, deadline: deadline, watchdog: watchdog, watchdogPID: watchdogPID}
	f.timer = time.AfterFunc(time.Until(deadline), f.release)
	fail := func(err error) (*ownerFrozenTree, error) { f.release(); return nil, err }
	for pass := 0; pass < 4; pass++ {
		pids := snapshotProcessTreeMaxAge(0).descendantPids(root)
		if len(pids) < 3 || len(pids) > 66 {
			return fail(fmt.Errorf("owner child tree is absent or exceeds the retirement bound"))
		}
		for _, pid := range pids {
			if pid == root || pid == f.watchdogPID {
				continue
			}
			f.mu.Lock()
			if f.released || time.Now().After(deadline) {
				f.mu.Unlock()
				return fail(fmt.Errorf("retirement freeze deadline elapsed"))
			}
			if _, ok := f.fds[pid]; ok {
				f.mu.Unlock()
				continue
			}
			fd, err := unix.PidfdOpen(pid, 0)
			// Revalidate ancestry AFTER acquiring generation-bound authority. A PID
			// recycled between the snapshot and pidfd_open must not receive a signal.
			if err == nil && !turnProcessDescends("/proc", pid, root) {
				unix.Close(fd)
				err = fmt.Errorf("owner process ancestry changed")
			}
			if err == nil {
				// Keep our own resume authority BEFORE the helper may stop it.
				// A helper crash between STOP and ACK must still unwind safely.
				f.fds[pid] = fd
				err = watchdogStop(f.watchdog, fd, deadline)
			}
			f.mu.Unlock()
			if err != nil {
				return fail(err)
			}
		}
		stable := true
		// Re-scan after parents have stopped: a just-forked child must also be
		// fenced. No fixed pre-stop process list is treated as complete.
		for _, pid := range snapshotProcessTreeMaxAge(0).descendantPids(root) {
			if pid == root || pid == f.watchdogPID {
				continue
			}
			f.mu.Lock()
			_, known := f.fds[pid]
			expired := f.released
			f.mu.Unlock()
			if expired {
				return fail(fmt.Errorf("retirement freeze expired"))
			}
			raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
			end := strings.LastIndexByte(string(raw), ')')
			state := ""
			if err == nil && end >= 0 {
				fields := strings.Fields(string(raw)[end+1:])
				if len(fields) > 0 {
					state = fields[0]
				}
			}
			if !known || (state != "T" && state != "t") {
				stable = false
			}
		}
		if stable {
			return f, nil
		}
		time.Sleep(time.Millisecond)
	}
	return fail(fmt.Errorf("owner child tree did not reach a stable stopped boundary"))
}

func (f *ownerFrozenTree) release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.released {
		return
	}
	f.released = true
	if f.watchdog != nil {
		_ = f.watchdog.Close()
		f.watchdog = nil
	}
	if f.timer != nil {
		f.timer.Stop()
	}
	for _, fd := range f.fds {
		_ = unix.PidfdSendSignal(fd, unix.SIGCONT, nil, 0)
		_ = unix.Close(fd)
	}
	f.fds = nil
}

// Commit is unconditional only AFTER the exact stopped tree was proved idle,
// the daemon retained its history, and both ends revalidated the same lease.
func (f *ownerFrozenTree) commit() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.released || !time.Now().Before(f.deadline) {
		return fmt.Errorf("retirement lease expired; no process closed")
	}
	// The independent watchdog serializes commit against its expiry release;
	// the parent must never race SIGKILL against a timeout SIGCONT.
	if err := watchdogCommit(f.watchdog, f.deadline); err != nil {
		return err
	}
	f.released = true
	if f.watchdog != nil {
		_ = f.watchdog.Close()
		f.watchdog = nil
	}
	if f.timer != nil {
		f.timer.Stop()
	}
	for _, fd := range f.fds {
		_ = unix.Close(fd)
	}
	f.fds = nil
	return nil
}
