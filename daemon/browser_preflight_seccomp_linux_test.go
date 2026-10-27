//go:build linux

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

const browserPreflightSeccompHelper = "SOLU_TEST_BROWSER_PREFLIGHT_SECCOMP"

func TestBrowserPreflightRefusesUnderNetworkSeccomp(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skipf("real seccomp preflight fixture supports amd64/arm64, not %s", runtime.GOARCH)
	}
	node, err := resolveNodeBin()
	if err != nil {
		t.Skipf("real seccomp preflight fixture requires node: %v", err)
	}
	script, err := filepath.Abs(filepath.Join("..", "web", "tools", "browser-preflight.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	control := exec.Command(node, script)
	controlOut, controlErr := control.CombinedOutput()
	if controlErr == nil {
		if !strings.Contains(string(controlOut), `"blocked":false`) {
			t.Fatalf("control preflight output=%q, want blocked=false", controlOut)
		}
	} else {
		// Executor test processes may themselves be nested in the very Codex
		// sandbox this test reproduces. Keep exercising the installed-filter
		// helper below, but accept only the same diagnosed exit 79 here. The
		// engine's unsandboxed verify process exercises the blocked=false arm.
		var controlExit *exec.ExitError
		if !errors.As(controlErr, &controlExit) || controlExit.ExitCode() != 79 || !strings.Contains(string(controlOut), "network-restricted seccomp sandbox") {
			t.Fatalf("control preflight error=%v output=%q", controlErr, controlOut)
		}
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestBrowserPreflightSeccompHelperProcess$", "--", node, script)
	cmd.Env = append(os.Environ(), browserPreflightSeccompHelper+"=1")
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 79 {
		t.Fatalf("filtered preflight error=%v output=%q, want exit 79", err, out)
	}
	text := string(out)
	if !strings.Contains(text, "network-restricted seccomp sandbox") || !strings.Contains(text, "sandbox_host_linux.cc:41") {
		t.Fatalf("filtered preflight output lacks diagnosis: %q", text)
	}
}

func TestBrowserPreflightSeccompHelperProcess(t *testing.T) {
	if os.Getenv(browserPreflightSeccompHelper) != "1" {
		return
	}
	args := os.Args
	sep := -1
	for i, arg := range args {
		if arg == "--" {
			sep = i
			break
		}
	}
	if sep < 0 || len(args) != sep+3 {
		os.Exit(125)
	}
	arch := uint32(unix.AUDIT_ARCH_X86_64)
	if runtime.GOARCH == "arm64" {
		arch = uint32(unix.AUDIT_ARCH_AARCH64)
	}
	const (
		bpfLDWAbs = 0x20
		bpfJEQK   = 0x15
		bpfRETK   = 0x06
	)
	allow := uint32(unix.SECCOMP_RET_ALLOW)
	deny := uint32(unix.SECCOMP_RET_ERRNO) | uint32(syscall.EPERM)
	filter := []unix.SockFilter{
		{Code: bpfLDWAbs, K: 4},
		{Code: bpfJEQK, Jt: 1, Jf: 0, K: arch},
		{Code: bpfRETK, K: allow},
		{Code: bpfLDWAbs, K: 0},
		{Code: bpfJEQK, Jt: 0, Jf: 1, K: uint32(unix.SYS_SHUTDOWN)},
		{Code: bpfRETK, K: deny},
		{Code: bpfJEQK, Jt: 0, Jf: 3, K: uint32(unix.SYS_SOCKET)},
		{Code: bpfLDWAbs, K: 16},
		{Code: bpfJEQK, Jt: 1, Jf: 0, K: uint32(unix.AF_UNIX)},
		{Code: bpfRETK, K: deny},
		{Code: bpfRETK, K: allow},
	}
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	// prctl's no-new-privileges and seccomp filter apply to the calling OS
	// thread, not every thread in this Go helper. Keep both installations and
	// exec on that same thread; a scheduler migration otherwise leaves node
	// unrestricted (or makes PR_SET_SECCOMP fail). Exec/os.Exit never returns,
	// so the helper deliberately stays pinned until its process is replaced.
	runtime.LockOSThread()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		os.Exit(126)
	}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0); err != nil {
		os.Exit(126)
	}
	if err := syscall.Exec(args[sep+1], args[sep+1:], os.Environ()); err != nil {
		os.Exit(127)
	}
}
