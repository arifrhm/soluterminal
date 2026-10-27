//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const linuxProcessScopeKind = "linux-cgroup-v2-ns-v1"
const linuxProcessScopeTerminalKind = "linux-cgroup-v2-v1"
const processScopeCgroupPrefix = "solu-owned-"
const processScopeProbeArg = "__solu_internal_process_scope_probe_v1"

var processScopeNamespaceProbe struct {
	sync.Once
	err error
}

func init() {
	if len(os.Args) == 2 && os.Args[1] == processScopeProbeArg {
		os.Exit(0)
	}
}

func checkProcessScopeNamespaces(attr *syscall.SysProcAttr) error {
	processScopeNamespaceProbe.Do(func() {
		// Probe the daemon's actual fork context once, before preparing any
		// owned user command. Some hosts mount nsdelegate but disable user
		// namespaces through sysctl/AppArmor. Generic launchers must retain
		// their legacy behavior there, while durable workflow launches fail
		// explicitly rather than acquiring an untrue containment identity.
		probe := exec.Command("/proc/self/exe", processScopeProbeArg)
		probe.SysProcAttr = attr
		processScopeNamespaceProbe.err = probe.Run()
	})
	return processScopeNamespaceProbe.err
}

func checkProcessScopeDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || !ok || stat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("process scope state directory is not private: %s", path)
	}
	return nil
}

func syncProcessScopeDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func lockProcessScope(root, id string) (func(), error) {
	if !validProcessScopeID(id) {
		return nil, fmt.Errorf("invalid process scope id")
	}
	dir := filepath.Join(root, "locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := checkProcessScopeDirectory(dir); err != nil {
		return nil, err
	}
	fd, err := unix.Open(filepath.Join(dir, id), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = unix.Close(fd) }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) || !time.Now().Before(deadline) {
			unix.Close(fd)
			return nil, fmt.Errorf("process scope launch/stop is busy: %w", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func currentProcessScopeBoot() (string, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || strings.TrimSpace(string(raw)) == "" {
		return "", fmt.Errorf("kernel boot identity unavailable: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func processScopeBirth(pid int) string {
	identity, err := retirementReadProcess("/proc", pid)
	if err != nil {
		return ""
	}
	return identity.Start
}

func processScopeCgroupPath(pid int) (string, error) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "0::/") {
			rel := strings.TrimPrefix(line, "0::")
			if strings.Contains(rel, "..") || !filepath.IsAbs(rel) {
				return "", fmt.Errorf("cgroup namespace ancestry unavailable")
			}
			return filepath.Join("/sys/fs/cgroup", rel), nil
		}
	}
	return "", fmt.Errorf("cgroup v2 hierarchy unavailable")
}

func processScopeNSDelegated() bool {
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 || fields[4] != "/sys/fs/cgroup" {
			continue
		}
		for i, field := range fields {
			if field == "-" && i+3 < len(fields) && fields[i+1] == "cgroup2" {
				return strings.Contains(","+fields[i+3]+",", ",nsdelegate,")
			}
		}
	}
	return false
}

func processScopeParent() (string, error) {
	path, err := processScopeCgroupPath(os.Getpid())
	if err != nil {
		return "", err
	}
	// Inside a scope namespace /proc reports '/'. The hint is only a locator:
	// validate the kernel's actual membership before accepting it. Providers
	// retain this non-authority hint even when their SOLU_ credentials are stripped.
	hint := os.Getenv(processScopeHintEnv)
	if path != "/sys/fs/cgroup" && hint == "" {
		return path, nil
	}
	if !validLinuxProcessScopePath(hint, "") {
		if path != "/sys/fs/cgroup" {
			return path, nil
		}
		return "", fmt.Errorf("delegated process scope parent unavailable")
	}
	raw, err := os.ReadFile(filepath.Join(hint, "cgroup.procs"))
	for _, text := range strings.Fields(string(raw)) {
		if text == strconv.Itoa(os.Getpid()) {
			return hint, nil
		}
	}
	if path != "/sys/fs/cgroup" {
		return path, nil
	}
	if err != nil {
		return "", err
	}
	return "", fmt.Errorf("process scope hint does not contain this process")
}

func validLinuxProcessScopePath(path, id string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !strings.HasPrefix(path, "/sys/fs/cgroup/") {
		return false
	}
	base := filepath.Base(path)
	if !strings.HasPrefix(base, processScopeCgroupPrefix) || !validProcessScopeID(strings.TrimPrefix(base, processScopeCgroupPrefix)) {
		return false
	}
	return id == "" || base == processScopeCgroupPrefix+id
}

func processScopeParentWritable(path string) bool {
	return unix.Access(path, unix.W_OK) == nil
}

func preparePlatformProcessScope(cmd *exec.Cmd, contained bool) (*ownedProcessScope, error) {
	if cmd.SysProcAttr == nil || (!cmd.SysProcAttr.Setsid && !cmd.SysProcAttr.Setpgid) {
		return nil, nil
	}
	if cmd.SysProcAttr.Credential != nil || cmd.SysProcAttr.UseCgroupFD || cmd.SysProcAttr.Cloneflags&syscall.CLONE_NEWUSER != 0 || cmd.SysProcAttr.Unshareflags != 0 {
		return nil, fmt.Errorf("command already has a separate namespace/cgroup policy")
	}
	if contained && !processScopeNSDelegated() {
		return nil, fmt.Errorf("cgroup v2 nsdelegate is required for migration containment")
	}
	parent, err := processScopeParent()
	if err != nil {
		return nil, err
	}
	if !processScopeParentWritable(parent) && !validLinuxProcessScopePath(parent, "") {
		if err := delegateProcessScopeParent(); err != nil {
			return nil, err
		}
		parent, err = processScopeParent()
		if err != nil {
			return nil, err
		}
	}
	boot, err := currentProcessScopeBoot()
	if err != nil {
		return nil, err
	}
	root, err := processScopeRoot()
	if err != nil {
		return nil, err
	}
	id, err := newProcessScopeID()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(parent, processScopeCgroupPrefix+id)
	if err := os.Mkdir(path, 0700); err != nil {
		return nil, fmt.Errorf("create delegated command cgroup: %w", err)
	}
	fail := func(err error) (*ownedProcessScope, error) { _ = os.Remove(path); return nil, err }
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fail(err)
	}
	file := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		file.Close()
		return fail(err)
	}
	if _, err := os.Stat(filepath.Join(path, "cgroup.kill")); err != nil {
		file.Close()
		return fail(fmt.Errorf("kernel cgroup.kill is unavailable: %w", err))
	}
	attr := *cmd.SysProcAttr
	attr.UseCgroupFD, attr.CgroupFD = true, fd
	kind := linuxProcessScopeTerminalKind
	if contained {
		kind = linuxProcessScopeKind
		// Born in the cgroup atomically; then create its namespace before exec.
		// nsdelegate rejects migration out even through an inherited host mount.
		// Map the same uid/gid (not root), preserving filesystem identity, network,
		// workspace access, and supplementary kernel group credentials.
		attr.Cloneflags |= syscall.CLONE_NEWUSER
		attr.Unshareflags |= syscall.CLONE_NEWCGROUP
		attr.UidMappings = []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}}
		attr.GidMappings = []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}}
		attr.GidMappingsEnableSetgroups = false
		probeAttr := attr
		probeAttr.Setpgid, probeAttr.Pgid, probeAttr.Setsid = false, 0, true
		if err := checkProcessScopeNamespaces(&probeAttr); err != nil {
			file.Close()
			return fail(fmt.Errorf("kernel namespace containment unavailable: %w", err))
		}
	}
	// Ordinary panes and generic launchers keep their existing user namespace
	// and setuid behavior (including sudo). Their cgroup still owns setsid and
	// double-fork children, but is never treated as migration containment.
	scope := &ownedProcessScope{identity: processScopeIdentity{Version: 1, Kind: kind, ID: id, Boot: boot, Path: path, Inode: stat.Ino}, root: root, resource: file, originalAttr: cmd.SysProcAttr}
	if err := scope.writeRecord(processScopeRecord{Scope: scope.identity, Phase: "prepared", CreatorPID: os.Getpid(), CreatorBirth: processScopeBirth(os.Getpid())}); err != nil {
		file.Close()
		return fail(err)
	}
	cmd.SysProcAttr = &attr
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, processScopeHintEnv+"="+path)
	cmd.Env = append(cmd.Env, processScopeStateHintEnv+"="+root)
	return scope, nil
}

func openLinuxProcessScope(identity processScopeIdentity) (*os.File, error) {
	if !validLinuxProcessScopeIdentity(identity) {
		return nil, fmt.Errorf("incomplete process scope identity")
	}
	boot, err := currentProcessScopeBoot()
	if err != nil || boot != identity.Boot {
		return nil, fmt.Errorf("process scope belongs to another kernel boot")
	}
	fd, err := unix.Open(identity.Path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), identity.Path)
	var stat unix.Stat_t
	var fs unix.Statfs_t
	if err := errors.Join(unix.Fstat(fd, &stat), unix.Fstatfs(fd, &fs)); err != nil || stat.Ino != identity.Inode || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		file.Close()
		return nil, fmt.Errorf("process scope kernel identity changed")
	}
	return file, nil
}

func validLinuxProcessScopeIdentity(identity processScopeIdentity) bool {
	return identity.Version == 1 && (identity.Kind == linuxProcessScopeKind || identity.Kind == linuxProcessScopeTerminalKind) && identity.Inode != 0 && identity.Boot != "" && validLinuxProcessScopePath(identity.Path, identity.ID)
}

func processScopeHasContainment(identity processScopeIdentity) bool {
	return validLinuxProcessScopeIdentity(identity) && identity.Kind == linuxProcessScopeKind
}

func processScopeFile(dir *os.File, name string, write bool, data []byte) ([]byte, error) {
	flags := unix.O_RDONLY
	if write {
		flags = unix.O_WRONLY
	}
	fd, err := unix.Openat(int(dir.Fd()), name, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	if write {
		_, err = f.Write(data)
		return nil, err
	}
	return io.ReadAll(io.LimitReader(f, 64<<10))
}

func processScopeEmpty(dir *os.File) (bool, error) {
	raw, err := processScopeFile(dir, "cgroup.events", false, nil)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "populated" && (fields[1] == "0" || fields[1] == "1") {
			return fields[1] == "0", nil
		}
	}
	return false, fmt.Errorf("kernel population evidence is unavailable")
}

func recoverPlatformProcessScope(pid int) (*ownedProcessScope, error) {
	path, err := processScopeCgroupPath(pid)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !validLinuxProcessScopePath(path, "") {
		return nil, err
	}
	id := strings.TrimPrefix(filepath.Base(path), processScopeCgroupPrefix)
	root, err := processScopeRoot()
	if err != nil {
		return nil, err
	}
	data, err := readProcessScopeRecord(filepath.Join(root, "active", id+".json"))
	if err != nil {
		return nil, fmt.Errorf("owned process scope provenance is unavailable: %w", err)
	}
	var record processScopeRecord
	if len(data) > 4096 || json.Unmarshal(data, &record) != nil || record.Scope.ID != id || record.Scope.Path != path {
		return nil, fmt.Errorf("owned process scope record is invalid")
	}
	// A member is not the scope's creation-bound root. In particular a PTY
	// child must never acquire authority to stop its enclosing pane agent.
	if record.PID != pid || record.Birth == "" || processScopeBirth(pid) != record.Birth {
		return nil, nil
	}
	scope := &ownedProcessScope{identity: record.Scope, root: root}
	file, err := openLinuxProcessScope(record.Scope)
	if err != nil {
		return nil, err
	}
	file.Close()
	return scope, nil
}

func closeEmptyProcessScope(scope *ownedProcessScope) (bool, error) {
	if !validLinuxProcessScopeIdentity(scope.identity) {
		return false, fmt.Errorf("invalid process scope identity")
	}
	unlock, err := lockProcessScope(scope.root, scope.identity.ID)
	if err != nil {
		return false, err
	}
	defer unlock()
	record, err := scope.readRecord()
	if err != nil {
		return false, err
	}
	if record.Phase == "prepared" {
		if record.CreatorPID <= 0 || record.CreatorBirth == "" {
			return false, nil
		}
		creator, err := retirementReadProcess("/proc", record.CreatorPID)
		if !errors.Is(err, os.ErrNotExist) && (err != nil || creator.Start == record.CreatorBirth) {
			return false, nil
		}
		// Only the process that owns the unstarted exec.Cmd can use its creation
		// FD. Once that exact creator is gone, fence its abandoned preparation.
		record.Phase = "closing"
		if err := scope.writeRecord(record); err != nil {
			return false, err
		}
	}
	if record.Phase == "closed" {
		return true, removeClosedLinuxProcessScope(scope)
	}
	dir, err := openLinuxProcessScope(scope.identity)
	if err != nil {
		return false, err
	}
	defer dir.Close()
	empty, err := processScopeEmpty(dir)
	if err != nil || !empty {
		return false, err
	}
	record.Phase = "closed"
	if err := scope.writeRecord(record); err != nil {
		return false, err
	}
	return true, removeClosedLinuxProcessScope(scope)
}

// A closed parent has no live members in its inclusive hierarchy. Fence and
// receipt any nested owned scopes before deleting their kernel directories, so
// their separately persisted identities remain useful after a restart.
func removeClosedLinuxProcessScope(scope *ownedProcessScope) error {
	dir, err := openLinuxProcessScope(scope.identity)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	empty, err := processScopeEmpty(dir)
	if err != nil || !empty {
		return fmt.Errorf("closed scope is no longer empty: %w", err)
	}
	var removeChildren func(string) error
	removeChildren = func(path string) error {
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			child := filepath.Join(path, entry.Name())
			id := strings.TrimPrefix(entry.Name(), processScopeCgroupPrefix)
			if strings.HasPrefix(entry.Name(), processScopeCgroupPrefix) && validProcessScopeID(id) {
				data, readErr := readProcessScopeRecord(filepath.Join(scope.root, "active", id+".json"))
				if errors.Is(readErr, os.ErrNotExist) {
					data, readErr = readProcessScopeRecord(filepath.Join(scope.root, "closed", id+".json"))
				}
				var record processScopeRecord
				if readErr != nil || json.Unmarshal(data, &record) != nil || record.Scope.Path != child {
					return fmt.Errorf("nested process scope provenance unavailable")
				}
				if gone, err := stopOwnedProcessScope(&ownedProcessScope{identity: record.Scope, root: scope.root}, true); err != nil || !gone {
					return fmt.Errorf("nested process scope cleanup: %w", err)
				}
				continue
			}
			if err := removeChildren(child); err != nil {
				return err
			}
			if err := os.Remove(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := removeChildren(scope.identity.Path); err != nil {
		return err
	}
	return os.Remove(scope.identity.Path)
}

func stopOwnedProcessScope(scope *ownedProcessScope, force bool) (bool, error) {
	if !validLinuxProcessScopeIdentity(scope.identity) {
		return false, fmt.Errorf("invalid process scope identity")
	}
	boot, err := currentProcessScopeBoot()
	if err != nil {
		return false, err
	}
	if boot != scope.identity.Boot {
		return true, nil
	}
	unlock, err := lockProcessScope(scope.root, scope.identity.ID)
	if err != nil {
		return false, err
	}
	defer unlock()
	record, err := scope.readRecord()
	if err != nil {
		return false, err
	}
	if record.Phase == "closed" {
		_ = removeClosedLinuxProcessScope(scope)
		return true, nil
	}
	dir, err := openLinuxProcessScope(scope.identity)
	if err != nil {
		return false, err
	}
	defer dir.Close()
	record.Phase = "closing"
	if err := scope.writeRecord(record); err != nil {
		return false, err
	}
	if !force {
		return false, signalLinuxProcessScope(dir, syscall.SIGTERM)
	}
	if _, err := processScopeFile(dir, "cgroup.kill", true, []byte("1")); err != nil {
		return false, err
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		empty, err := processScopeEmpty(dir)
		if err != nil {
			return false, err
		}
		if empty {
			record.Phase = "closed"
			if err := scope.writeRecord(record); err != nil {
				return false, err
			}
			_ = removeClosedLinuxProcessScope(scope)
			return true, nil
		}
		if !time.Now().Before(deadline) {
			return false, fmt.Errorf("owned processes are still stopping")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func signalLinuxProcessScope(dir *os.File, signal syscall.Signal) error {
	raw, err := processScopeFile(dir, "cgroup.procs", false, nil)
	if err != nil {
		return err
	}
	for _, text := range strings.Fields(string(raw)) {
		pid, err := strconv.Atoi(text)
		if err != nil || pid == os.Getpid() {
			return fmt.Errorf("invalid/self process in scope signal")
		}
		fd, err := unix.PidfdOpen(pid, 0)
		if errors.Is(err, unix.ESRCH) {
			continue
		}
		if err != nil {
			return err
		}
		// Membership is rechecked after pidfd_open. A recycled PID outside this
		// exact scope never receives a signal.
		path, readErr := processScopeCgroupPath(pid)
		if readErr == nil && path == dir.Name() {
			err = unix.PidfdSendSignal(fd, unix.Signal(signal), nil, 0)
		}
		unix.Close(fd)
		if err != nil && !errors.Is(err, unix.ESRCH) {
			return err
		}
	}
	return nil
}
