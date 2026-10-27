//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	gopty "github.com/aymanbagabas/go-pty"
)

type fakeAgentPty struct{}

func (fakeAgentPty) Read([]byte) (int, error)             { return 0, os.ErrClosed }
func (fakeAgentPty) Write(b []byte) (int, error)          { return len(b), nil }
func (fakeAgentPty) Close() error                         { return nil }
func (fakeAgentPty) Name() string                         { return "fake-pty" }
func (fakeAgentPty) Command(string, ...string) *gopty.Cmd { return &gopty.Cmd{} }
func (fakeAgentPty) CommandContext(context.Context, string, ...string) *gopty.Cmd {
	return &gopty.Cmd{}
}
func (fakeAgentPty) Resize(int, int) error { return nil }
func (fakeAgentPty) Fd() uintptr           { return 0 }

func unixConnPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skipf("unix socketpair blocked by sandbox: %v", err)
		}
		t.Fatal(err)
	}
	files := []*os.File{
		os.NewFile(uintptr(fds[0]), "agent-test-client"),
		os.NewFile(uintptr(fds[1]), "agent-test-server"),
	}
	conns := make([]*net.UnixConn, 2)
	for i, f := range files {
		c, err := net.FileConn(f)
		_ = f.Close()
		if err != nil {
			if errors.Is(err, syscall.EPERM) {
				t.Skipf("unix socket FileConn blocked by sandbox: %v", err)
			}
			t.Fatal(err)
		}
		uc, ok := c.(*net.UnixConn)
		if !ok {
			_ = c.Close()
			t.Fatalf("conn %d is %T, want *net.UnixConn", i, c)
		}
		conns[i] = uc
	}
	return conns[0], conns[1]
}

func TestHandleAgentClientReturnsOnClientDisconnect(t *testing.T) {
	t.Parallel()
	client, server := unixConnPair(t)
	defer client.Close()

	var subMu sync.Mutex
	var subCh chan []byte
	addSub := func(*net.UnixConn) (chan []byte, []byte, []byte, []byte) {
		subMu.Lock()
		defer subMu.Unlock()
		subCh = make(chan []byte)
		return subCh, nil, nil, []byte(`{"cols":120,"rows":32}`)
	}
	removeSub := func(*net.UnixConn) {
		subMu.Lock()
		defer subMu.Unlock()
		if subCh != nil {
			close(subCh)
			subCh = nil
		}
	}
	var childExited atomic.Bool
	var exitMsg atomic.Pointer[[]byte]
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleAgentClient(server, fakeAgentPty{}, &sync.Mutex{}, addSub, removeSub, func(int, int) {}, nil, &childExited, &exitMsg)
	}()

	tpe, payload, err := ReadFrame(client)
	if err != nil {
		t.Fatalf("read dims frame: %v", err)
	}
	if tpe != FrameDims {
		t.Fatalf("first frame = %v, want FrameDims", tpe)
	}
	if string(payload) != `{"cols":120,"rows":32}` {
		t.Fatalf("dims payload = %q", payload)
	}
	tpe, payload, err = ReadFrame(client)
	if err != nil {
		t.Fatalf("read agent info: %v", err)
	}
	if tpe != FrameAgentInfo {
		t.Fatalf("second frame = %v, want FrameAgentInfo", tpe)
	}
	var info struct {
		Version string `json:"version"`
		PID     int    `json:"pid"`
	}
	if err := json.Unmarshal(payload, &info); err != nil || info.Version != version || info.PID != os.Getpid() {
		t.Fatalf("agent info = %+v err=%v", info, err)
	}
	tpe, _, err = ReadFrame(client)
	if err != nil {
		t.Fatalf("read replay end: %v", err)
	}
	if tpe != FrameReplayEnd {
		t.Fatalf("third frame = %v, want FrameReplayEnd", tpe)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handleAgentClient did not return after client disconnect")
	}
}
