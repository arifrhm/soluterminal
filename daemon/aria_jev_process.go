package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"time"
)

// jevProcessIdentity binds an evaluation to observed process incarnations. It
// is local evidence, not provider input or proof that a model received Enter.
// Linux observes owned foreground groups; Windows conservatively observes the
// entire owned tree because ConPTY has no POSIX foreground-group equivalent.
type jevProcessIdentity struct {
	Source    string
	RootPID   int
	RootStart string
	Digest    [32]byte
}

const (
	jevProcessLinuxSource   = "linux-foreground-set"
	jevProcessWindowsSource = "windows-owned-tree"
	jevProcessProbeTimeout  = time.Second
	jevProcessMaxEntries    = 16384
	jevProcessMaxDepth      = 128
)

var (
	errJevProcessUnavailable = errors.New("Jev process identity unavailable")
	errJevProcessUnsupported = errors.New("Jev process identity unsupported on this platform")
)

type jevProcessRecord struct {
	PID, Parent, Group, TTY, Foreground int
	Start                               string
	StartOrder                          uint64
	Stopped                             bool
}

type jevProcessSnapshot map[int]jevProcessRecord
type jevProcessSnapshotReader func(context.Context, int) (jevProcessSnapshot, error)

// Two uncached observations must agree, including membership and ownership
// paths. Readers must report incomplete evidence as an error, not an empty
// successful snapshot. The context also bounds validation loops; OS metadata
// calls themselves are synchronous and cannot be interrupted mid-system-call.
func probeJevProcessIdentityWithSnapshots(ctx context.Context, root int, source string, read jevProcessSnapshotReader) (jevProcessIdentity, error) {
	if err := ctx.Err(); err != nil {
		return jevProcessIdentity{}, err
	}
	if root <= 0 || read == nil || (source != jevProcessLinuxSource && source != jevProcessWindowsSource) {
		return jevProcessIdentity{}, errJevProcessUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, jevProcessProbeTimeout)
	defer cancel()
	var prior jevProcessIdentity
	for pass := 0; pass < 2; pass++ {
		snapshot, err := read(ctx, root)
		if err != nil {
			return jevProcessIdentity{}, err
		}
		identity, err := jevProcessIdentityFromSnapshot(ctx, snapshot, root, source)
		if err != nil {
			return jevProcessIdentity{}, err
		}
		if pass == 1 && identity != prior {
			return jevProcessIdentity{}, errJevProcessUnavailable
		}
		prior = identity
	}
	if err := ctx.Err(); err != nil {
		return jevProcessIdentity{}, err
	}
	return prior, nil
}

// Ownership is derived only from this snapshot, never the UI process cache.
func jevOwnedProcessIDs(ctx context.Context, snapshot jevProcessSnapshot, root int) ([]int, error) {
	if len(snapshot) == 0 || len(snapshot) > jevProcessMaxEntries {
		return nil, errJevProcessUnavailable
	}
	if record, ok := snapshot[root]; !ok || record.PID != root || record.Parent == root {
		return nil, errJevProcessUnavailable
	}
	children := make(map[int][]int)
	for pid, record := range snapshot {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if pid <= 0 || record.PID != pid || record.Parent < 0 {
			return nil, errJevProcessUnavailable
		}
		children[record.Parent] = append(children[record.Parent], pid)
	}
	type entry struct{ pid, depth int }
	queue := []entry{{root, 0}}
	seen := map[int]bool{root: true}
	ids := make([]int, 0)
	for at := 0; at < len(queue); at++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current := queue[at]
		ids = append(ids, current.pid)
		for _, child := range children[current.pid] {
			if seen[child] || current.depth+1 > jevProcessMaxDepth {
				return nil, errJevProcessUnavailable
			}
			seen[child] = true
			queue = append(queue, entry{child, current.depth + 1})
		}
	}
	return ids, nil
}

func jevProcessIdentityFromSnapshot(ctx context.Context, snapshot jevProcessSnapshot, root int, source string) (jevProcessIdentity, error) {
	if source != jevProcessLinuxSource && source != jevProcessWindowsSource {
		return jevProcessIdentity{}, errJevProcessUnavailable
	}
	owned, err := jevOwnedProcessIDs(ctx, snapshot, root)
	if err != nil {
		return jevProcessIdentity{}, err
	}
	selected := map[int]bool{root: true}
	if source == jevProcessWindowsSource {
		for _, pid := range owned {
			selected[pid] = true
		}
	} else {
		type terminalGroup struct{ tty, group int }
		needed, present := map[terminalGroup]bool{}, map[terminalGroup]bool{}
		for _, pid := range owned {
			if err := ctx.Err(); err != nil {
				return jevProcessIdentity{}, err
			}
			record := snapshot[pid]
			if record.TTY == 0 {
				continue // The agent wrapper need not own a controlling terminal.
			}
			if record.Foreground <= 0 {
				return jevProcessIdentity{}, errJevProcessUnavailable
			}
			key := terminalGroup{record.TTY, record.Foreground}
			needed[key] = true
			if record.Group <= 0 || record.Group != record.Foreground {
				continue
			}
			present[key] = true
			// Include every foreground member and its path, not just the group
			// leader: a Node/TUI child can restart under an unchanged shell.
			for ancestor := pid; ancestor != root; ancestor = snapshot[ancestor].Parent {
				selected[ancestor] = true
			}
		}
		if len(present) == 0 || len(present) != len(needed) {
			return jevProcessIdentity{}, errJevProcessUnavailable
		}
	}
	ids := make([]int, 0, len(selected))
	for pid := range selected {
		record := snapshot[pid]
		if record.Start == "" || record.StartOrder == 0 || record.Stopped {
			return jevProcessIdentity{}, errJevProcessUnavailable
		}
		if pid != root {
			parent := snapshot[record.Parent]
			if !selected[record.Parent] || parent.StartOrder == 0 || parent.StartOrder > record.StartOrder {
				return jevProcessIdentity{}, errJevProcessUnavailable
			}
		}
		ids = append(ids, pid)
	}
	sort.Ints(ids)
	identity := jevProcessIdentity{Source: source, RootPID: root, RootStart: snapshot[root].Start}
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\n%d\n%s\n", source, root, identity.RootStart)
	for _, pid := range ids {
		if err := ctx.Err(); err != nil {
			return jevProcessIdentity{}, err
		}
		record := snapshot[pid]
		fmt.Fprintf(hash, "%d %d %q %d %d %d %d\n", record.PID, record.Parent, record.Start,
			record.StartOrder, record.Group, record.TTY, record.Foreground)
	}
	copy(identity.Digest[:], hash.Sum(nil))
	return identity, nil
}
