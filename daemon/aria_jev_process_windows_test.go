//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestJevProcessWindowsOwnRootNativeProbe(t *testing.T) {
	identity, err := probeJevProcessIdentity(context.Background(), os.Getpid())
	if err != nil || identity.Source != jevProcessWindowsSource || identity.RootPID != os.Getpid() || identity.RootStart == "" || identity.Digest == ([32]byte{}) {
		t.Fatalf("native own-root probe: %+v %v", identity, err)
	}
}

func TestJevProcessWindowsCreationTimeAndExit(t *testing.T) {
	created := windows.Filetime{HighDateTime: 30000000, LowDateTime: 123}
	start, order, err := jevWindowsProcessStart(created, windows.Filetime{})
	if err != nil || start == "" || order != uint64(created.HighDateTime)<<32|uint64(created.LowDateTime) {
		t.Fatalf("creation identity: %q %d %v", start, order, err)
	}
	changed := created
	changed.LowDateTime++
	next, nextOrder, err := jevWindowsProcessStart(changed, windows.Filetime{})
	if err != nil || next == start || nextOrder == order {
		t.Fatal("PID reuse creation time was collapsed")
	}
	for _, times := range [][2]windows.Filetime{{{}, {}}, {created, {LowDateTime: 1}}} {
		start, order, err := jevWindowsProcessStart(times[0], times[1])
		if !errors.Is(err, errJevProcessUnavailable) || start != "" || order != 0 {
			t.Fatalf("exited/missing identity accepted: %q %d %v", start, order, err)
		}
	}
}

func TestJevProcessWindowsCancelledBeforeToolhelp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	identity, err := probeJevProcessIdentity(ctx, 100)
	if !errors.Is(err, context.Canceled) || identity != (jevProcessIdentity{}) {
		t.Fatalf("cancelled Windows probe: %+v %v", identity, err)
	}
}
