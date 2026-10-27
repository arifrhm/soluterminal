//go:build windows

package main

// Windows process-tree walk. The Unix version reads /proc/*/stat for
// each pid to build a pid→ppid map; here we do the equivalent via
// Toolhelp32 snapshots, which give us pid + ppid + exe name in one
// call per process.

import (
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// readProcEnviron has no portable Windows equivalent (you cannot read another
// process's environment block without debug privileges). Degraded-pane
// detection is therefore Linux-only; on Windows it returns "unknown" and the
// pane is never flagged.
func readProcEnviron(pid int) (map[string]string, bool) {
	return nil, false
}

// toolhelp snapshots are expensive (they enumerate every process on
// the machine). Cache for a short window so a single pane's
// HasClaudeDescendant + ChildPids calls share one snapshot.
var (
	snapshotMu    sync.Mutex
	snapshotAt    time.Time
	snapshotTree  map[int]int    // pid → ppid
	snapshotNames map[int]string // pid → exe basename (without .exe)
	snapshotKids  processTreeSnapshot
)

const snapshotTTL = 500 * time.Millisecond

// refreshSnapshot rebuilds the pid/ppid/name maps from a fresh
// Toolhelp32 snapshot. Caller must hold snapshotMu.
func refreshSnapshot() {
	if time.Since(snapshotAt) < snapshotTTL && snapshotTree != nil {
		return
	}
	tree := map[int]int{}
	names := map[int]string{}

	h, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		setSnapshot(tree, names)
		return
	}
	defer windows.CloseHandle(h)

	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if err := windows.Process32First(h, &pe); err != nil {
		setSnapshot(tree, names)
		return
	}
	for {
		pid := int(pe.ProcessID)
		ppid := int(pe.ParentProcessID)
		name := utf16ToStringStripExe(pe.ExeFile[:])
		tree[pid] = ppid
		names[pid] = name
		if err := windows.Process32Next(h, &pe); err != nil {
			break
		}
	}
	setSnapshot(tree, names)
}

func setSnapshot(tree map[int]int, names map[int]string) {
	snapshotTree = tree
	snapshotNames = names
	snapshotKids = processTreeFromParents(tree)
	snapshotAt = time.Now()
}

// descendantPids returns root + every process transitively descended
// from it. Matches the Unix semantics in procfs_unix.go.
func descendantPids(root int) []int {
	return snapshotProcessTree().descendantPids(root)
}

// processTreeSnapshot is shared read-only by every caller inside one snapshot
// window; starts is the only mutable part and guards itself.
type processTreeSnapshot struct {
	children map[int][]int
	starts   *procStartTimes // nil (test literals) = edges are never age-checked
}

func snapshotProcessTree() processTreeSnapshot {
	return snapshotProcessTreeMaxAge(snapshotTTL)
}

func snapshotProcessTreeMaxAge(maxAge time.Duration) processTreeSnapshot {
	snapshotMu.Lock()
	if time.Since(snapshotAt) >= maxAge {
		snapshotAt = time.Time{}
	}
	refreshSnapshot()
	tree := snapshotKids
	snapshotMu.Unlock()
	return tree
}

// freshProcessTreeSnapshot bypasses the UI-oriented short cache. Timeout
// cleanup must see descendants that may have been spawned milliseconds ago.
func freshProcessTreeSnapshot() processTreeSnapshot {
	snapshotMu.Lock()
	snapshotAt = time.Time{}
	refreshSnapshot()
	tree := snapshotKids
	snapshotMu.Unlock()
	return tree
}

func processTreeFromParents(parents map[int]int) processTreeSnapshot {
	children := map[int][]int{}
	for pid, ppid := range parents {
		if pid == ppid {
			continue // System Idle (pid 0) reports itself as its own parent
		}
		children[ppid] = append(children[ppid], pid)
	}
	return processTreeSnapshot{children: children, starts: &procStartTimes{t: map[int]int64{}}}
}

// descendantPids walks the snapshot breadth-first. Windows never rewrites
// ParentProcessID when a parent exits, and PIDs are recycled quickly, so the
// recorded ppid of an orphan can name an unrelated newer process — including
// one of the orphan's own descendants, which forms a cycle. A detached pane
// agent whose spawning daemon has died is exactly such an orphan. Without the
// visited set a cycle made this walk grow without bound (multi-GB heaps and
// a state-cache lock held forever); the creation-time check also stops a
// recycled PID from adopting an unrelated orphan into a pane's tree, which
// matters because killAgentTree kills everything this returns.
func (s processTreeSnapshot) descendantPids(root int) []int {
	out := []int{root}
	if len(s.children) == 0 {
		return out
	}
	seen := map[int]bool{root: true}
	queue := []int{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range s.children[cur] {
			if seen[c] || !s.starts.plausibleChild(cur, c) {
				continue
			}
			seen[c] = true
			out = append(out, c)
			queue = append(queue, c)
		}
	}
	return out
}

// procStartTimes lazily caches process creation times for one snapshot. Walks
// touch only a pane's few dozen processes, so lookups stay cheap; reading every
// process up front would cost an OpenProcess per process per snapshot.
type procStartTimes struct {
	mu sync.Mutex
	t  map[int]int64 // pid → creation time (100ns ticks); 0 = unreadable
}

// plausibleChild reports whether child could really have been created by
// parent: a genuine child is never older than its parent. Unreadable times
// (protected or already-exited processes) keep the edge; the visited set
// still bounds the walk.
func (s *procStartTimes) plausibleChild(parent, child int) bool {
	if s == nil {
		return true
	}
	pt, ct := s.get(parent), s.get(child)
	return pt == 0 || ct == 0 || ct >= pt
}

func (s *procStartTimes) get(pid int) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.t[pid]; ok {
		return v
	}
	v := processCreationTime(pid)
	s.t[pid] = v
	return v
}

func processCreationTime(pid int) int64 {
	if pid <= 0 {
		return 0
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return 0
	}
	return int64(created.HighDateTime)<<32 | int64(created.LowDateTime)
}

// readPpid returns the parent PID for pid, or 0 if unknown. Same
// semantics as the Unix version — callers treat 0 as "unknown".
func readPpid(pid int) int {
	snapshotMu.Lock()
	refreshSnapshot()
	ppid := snapshotTree[pid]
	snapshotMu.Unlock()
	return ppid
}

// processName returns the exe basename for pid, without the `.exe`
// suffix, empty if unknown. This is the Windows equivalent of
// /proc/<pid>/comm and is used by both the ports panel and the
// Claude-in-pane detection in pane.go.
func processName(pid int) string {
	snapshotMu.Lock()
	refreshSnapshot()
	name := snapshotNames[pid]
	snapshotMu.Unlock()
	return name
}

// utf16ToStringStripExe turns a null-terminated UTF-16 array into a
// Go string and strips the trailing `.exe` (case-insensitive) so
// callers can match process names the same way they do on Unix
// (where /proc/<pid>/comm has no extension).
func utf16ToStringStripExe(buf []uint16) string {
	name := windows.UTF16ToString(buf)
	// Strip trailing ".exe" / ".EXE" etc.
	if n := len(name); n >= 4 {
		suf := name[n-4:]
		if equalFoldASCII(suf, ".exe") {
			name = name[:n-4]
		}
	}
	return name
}

// readHarnessCmdline has no portable equivalent on Windows: the managed CLI
// harnesses (Pi, DeepSeek, ZCode) only install on the WSL/Unix backend, so
// there is nothing to detect here. The Unix build reads /proc/<pid>/cmdline.
func readHarnessCmdline(pid int) string {
	return ""
}

// paneForegroundCmd has no portable equivalent on Windows. ConPTY does
// not expose a foreground process group the way POSIX terminals do —
// there is no tcgetpgrp(). The closest approximation would be walking
// pane.Pid's descendants and picking the most-recently-active one, but
// that's both expensive and ambiguous (background pipelines). Until
// someone designs a real Windows equivalent, this stub returns ok=false,
// which disables cmd.long_running on Windows. That is preferable to the
// previous behavior (constant false positives off PTY byte activity).
func paneForegroundCmd(rootPid int) (fgPid int, cmd string, ok bool) {
	return 0, "", false
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
