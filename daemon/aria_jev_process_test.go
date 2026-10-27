package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func jevTestProcessTree() jevProcessSnapshot {
	return jevProcessSnapshot{
		100: {PID: 100, Parent: 1, Start: "test:1000", StartOrder: 1000},
		101: {PID: 101, Parent: 100, Group: 101, TTY: 34818, Foreground: 102, Start: "test:1100", StartOrder: 1100},
		102: {PID: 102, Parent: 101, Group: 102, TTY: 34818, Foreground: 102, Start: "test:1200", StartOrder: 1200},
		103: {PID: 103, Parent: 102, Group: 102, TTY: 34818, Foreground: 102, Start: "test:1300", StartOrder: 1300},
	}
}

func cloneJevTestProcessTree(snapshot jevProcessSnapshot) jevProcessSnapshot {
	copy := make(jevProcessSnapshot, len(snapshot))
	for pid, record := range snapshot {
		copy[pid] = record
	}
	return copy
}

func TestJevProcessIdentityStableComparableAndDeterministic(t *testing.T) {
	for _, source := range []string{jevProcessLinuxSource, jevProcessWindowsSource} {
		t.Run(source, func(t *testing.T) {
			calls := 0
			identity, err := probeJevProcessIdentityWithSnapshots(context.Background(), 100, source, func(context.Context, int) (jevProcessSnapshot, error) {
				calls++
				return cloneJevTestProcessTree(jevTestProcessTree()), nil
			})
			if err != nil || calls != 2 || identity.Source != source || identity.RootPID != 100 || identity.RootStart != "test:1000" || identity.Digest == ([32]byte{}) {
				t.Fatalf("identity=%+v calls=%d err=%v", identity, calls, err)
			}
			again, err := jevProcessIdentityFromSnapshot(context.Background(), jevTestProcessTree(), 100, source)
			if err != nil || again != identity {
				t.Fatalf("same tree changed identity: %+v %v", again, err)
			}
		})
	}
}

func TestJevProcessIdentityRevalidationRejectsReplacements(t *testing.T) {
	mutations := map[string]func(jevProcessSnapshot){
		"root PID reuse":              func(s jevProcessSnapshot) { r := s[100]; r.Start, r.StartOrder = "test:1001", 1001; s[100] = r },
		"ancestor PID reuse":          func(s jevProcessSnapshot) { r := s[101]; r.Start, r.StartOrder = "test:1101", 1101; s[101] = r },
		"foreground member PID reuse": func(s jevProcessSnapshot) { r := s[103]; r.Start, r.StartOrder = "test:1301", 1301; s[103] = r },
		"member replaced under same shell": func(s jevProcessSnapshot) {
			delete(s, 103)
			s[104] = jevProcessRecord{PID: 104, Parent: 102, Group: 102, TTY: 34818, Foreground: 102, Start: "test:1400", StartOrder: 1400}
		},
		"new foreground member": func(s jevProcessSnapshot) {
			s[104] = jevProcessRecord{PID: 104, Parent: 102, Group: 102, TTY: 34818, Foreground: 102, Start: "test:1400", StartOrder: 1400}
		},
		"member exit":       func(s jevProcessSnapshot) { delete(s, 103) },
		"ancestry change":   func(s jevProcessSnapshot) { r := s[103]; r.Parent = 101; s[103] = r },
		"foreground switch": func(s jevProcessSnapshot) { r := s[103]; r.Foreground = 101; s[103] = r },
		"terminal switch":   func(s jevProcessSnapshot) { r := s[103]; r.TTY++; s[103] = r },
	}
	for _, source := range []string{jevProcessLinuxSource, jevProcessWindowsSource} {
		for name, mutate := range mutations {
			t.Run(source+"/"+name, func(t *testing.T) {
				calls := 0
				identity, err := probeJevProcessIdentityWithSnapshots(context.Background(), 100, source, func(context.Context, int) (jevProcessSnapshot, error) {
					calls++
					snapshot := jevTestProcessTree()
					if calls == 2 {
						mutate(snapshot)
					}
					return snapshot, nil
				})
				if !errors.Is(err, errJevProcessUnavailable) || identity != (jevProcessIdentity{}) {
					t.Fatalf("changed process evidence returned identity=%+v err=%v", identity, err)
				}
			})
		}
	}
}

func TestJevProcessIdentityPlatformBackgroundPolicy(t *testing.T) {
	for _, source := range []string{jevProcessLinuxSource, jevProcessWindowsSource} {
		calls := 0
		identity, err := probeJevProcessIdentityWithSnapshots(context.Background(), 100, source, func(context.Context, int) (jevProcessSnapshot, error) {
			calls++
			snapshot := jevTestProcessTree()
			if calls == 2 {
				snapshot[200] = jevProcessRecord{PID: 200, Parent: 101, Group: 200, TTY: 34818, Foreground: 102, Start: "test:2000", StartOrder: 2000}
			}
			return snapshot, nil
		})
		if source == jevProcessLinuxSource {
			if err != nil || identity.Digest == ([32]byte{}) {
				t.Fatalf("background group invalidated Linux foreground: %v", err)
			}
		} else if !errors.Is(err, errJevProcessUnavailable) || identity != (jevProcessIdentity{}) {
			t.Fatalf("Windows omitted owned background churn: %+v %v", identity, err)
		}
	}
}

func TestJevProcessIdentityIncompleteEvidenceFailsClosed(t *testing.T) {
	for name, mutate := range map[string]func(jevProcessSnapshot){
		"missing root":                func(s jevProcessSnapshot) { delete(s, 100) },
		"missing root start":          func(s jevProcessSnapshot) { r := s[100]; r.Start = ""; s[100] = r },
		"missing member start":        func(s jevProcessSnapshot) { r := s[103]; r.StartOrder = 0; s[103] = r },
		"recycled parent attribution": func(s jevProcessSnapshot) { r := s[101]; r.StartOrder = 1250; s[101] = r },
		"stopped foreground":          func(s jevProcessSnapshot) { r := s[103]; r.Stopped = true; s[103] = r },
		"no owned foreground": func(s jevProcessSnapshot) {
			for pid, r := range s {
				r.TTY = 0
				s[pid] = r
			}
		},
		"foreign foreground group": func(s jevProcessSnapshot) {
			for pid, r := range s {
				if r.TTY != 0 {
					r.Foreground = 900
					s[pid] = r
				}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			snapshot := jevTestProcessTree()
			mutate(snapshot)
			identity, err := jevProcessIdentityFromSnapshot(context.Background(), snapshot, 100, jevProcessLinuxSource)
			if !errors.Is(err, errJevProcessUnavailable) || identity != (jevProcessIdentity{}) {
				t.Fatalf("incomplete tree returned %+v %v", identity, err)
			}
		})
	}
	// Windows intentionally has no tty requirement.
	snapshot := jevTestProcessTree()
	for pid, r := range snapshot {
		r.TTY, r.Group, r.Foreground = 0, 0, 0
		snapshot[pid] = r
	}
	if _, err := jevProcessIdentityFromSnapshot(context.Background(), snapshot, 100, jevProcessWindowsSource); err != nil {
		t.Fatalf("Windows incorrectly required POSIX terminal ownership: %v", err)
	}
}

func TestJevProcessIdentityBoundedAndCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	read := func(context.Context, int) (jevProcessSnapshot, error) { calls++; return jevTestProcessTree(), nil }
	if identity, err := probeJevProcessIdentityWithSnapshots(ctx, 100, jevProcessLinuxSource, read); !errors.Is(err, context.Canceled) || calls != 0 || identity != (jevProcessIdentity{}) {
		t.Fatalf("cancelled probe read evidence: %+v %v calls=%d", identity, err, calls)
	}
	for _, root := range []int{0, -1} {
		if _, err := probeJevProcessIdentityWithSnapshots(context.Background(), root, jevProcessLinuxSource, read); !errors.Is(err, errJevProcessUnavailable) || calls != 0 {
			t.Fatalf("invalid root %d read evidence", root)
		}
	}
	snapshot := jevTestProcessTree()
	parent := 103
	for i := 0; i < jevProcessMaxDepth; i++ {
		pid := 1000 + i
		snapshot[pid] = jevProcessRecord{PID: pid, Parent: parent, Start: fmt.Sprint(pid), StartOrder: uint64(2000 + i)}
		parent = pid
	}
	if _, err := jevOwnedProcessIDs(context.Background(), snapshot, 100); !errors.Is(err, errJevProcessUnavailable) {
		t.Fatal("unbounded ownership path accepted")
	}
	ctx, cancel = context.WithCancel(context.Background())
	identity, err := probeJevProcessIdentityWithSnapshots(ctx, 100, jevProcessLinuxSource, func(context.Context, int) (jevProcessSnapshot, error) {
		cancel()
		return jevTestProcessTree(), nil
	})
	if !errors.Is(err, context.Canceled) || identity != (jevProcessIdentity{}) {
		t.Fatalf("cancellation after snapshot returned identity: %+v %v", identity, err)
	}
}
