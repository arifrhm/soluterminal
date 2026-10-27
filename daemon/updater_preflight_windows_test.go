//go:build windows

package main

import (
	"errors"
	"net"
	"os"
	"syscall"
	"testing"
)

func TestWorkflowBootSequenceWindowsListenerPreflight(t *testing.T) {
	t.Setenv("SOLU_SUCCESSOR_OF_UPDATE", "1")
	for _, tc := range []struct {
		name  string
		code  syscall.Errno
		fatal bool
	}{
		{name: "WSAEADDRINUSE", code: 10048},
		{name: "WSAEACCES", code: 10013, fatal: true},
		{name: "WSAEADDRNOTAVAIL", code: 10049, fatal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bindErr := &net.OpError{Op: "listen", Net: "tcp", Err: &os.SyscallError{Syscall: "bind", Err: tc.code}}
			ln, err := preflightUpdateSuccessorListenerWithListen("tcp", "127.0.0.1:8787", func(string, string) (net.Listener, error) {
				return nil, bindErr
			})
			if ln != nil || (err != nil) != tc.fatal {
				t.Fatalf("preflight = (%v, %v), want fatal=%v", ln, err, tc.fatal)
			}
			if tc.fatal && !errors.Is(err, bindErr) {
				t.Fatalf("preflight lost bind error: %v", err)
			}
		})
	}
}
