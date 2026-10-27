package main

import (
	"net"
	"testing"
)

func TestWorkflowBootSequenceOccupiedListenerPreflight(t *testing.T) {
	t.Setenv("SOLU_SUCCESSOR_OF_UPDATE", "1")
	predecessor, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer predecessor.Close()
	// Exercise the OS's actual bind error, including Winsock WSAEADDRINUSE
	// on Windows, rather than assuming it equals syscall.EADDRINUSE.
	ln, err := preflightUpdateSuccessorListener("tcp", predecessor.Addr().String())
	if ln != nil {
		ln.Close()
		t.Fatal("preflight acquired the predecessor's occupied address")
	}
	if err != nil {
		t.Fatalf("predecessor-owned listener rejected: %v", err)
	}
}
