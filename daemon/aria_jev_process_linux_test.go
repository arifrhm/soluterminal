//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// These fixtures contain stat files only: the production reader has no argv,
// environment, transcript, cwd or credential input to depend on or export.
func writeJevProcFixture(t *testing.T, proc string, record jevProcessRecord, state, comm string) {
	t.Helper()
	dir := filepath.Join(proc, strconv.Itoa(record.PID))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields("S 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 1")
	fields[0], fields[1], fields[2], fields[4], fields[5], fields[19] = state, strconv.Itoa(record.Parent), strconv.Itoa(record.Group), strconv.Itoa(record.TTY), strconv.Itoa(record.Foreground), strconv.FormatUint(record.StartOrder, 10)
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(fmt.Sprintf("%d (%s) %s\n", record.PID, comm, strings.Join(fields, " "))), 0600); err != nil {
		t.Fatal(err)
	}
}

func jevProcFixtureTree(t *testing.T) string {
	t.Helper()
	proc := t.TempDir()
	for pid, record := range jevTestProcessTree() {
		comm := map[int]string{100: "agent", 101: "bash", 102: "node weird ) name", 103: "python harness"}[pid]
		writeJevProcFixture(t, proc, record, "S", comm)
	}
	return proc
}

func TestJevProcessLinuxNeutralForegroundWithoutWrapperTTY(t *testing.T) {
	proc := jevProcFixtureTree(t)
	before, err := os.ReadFile(filepath.Join(proc, "102/stat"))
	if err != nil {
		t.Fatal(err)
	}
	identity, err := probeJevProcessIdentityAt(context.Background(), proc, 100)
	if err != nil || identity.Source != jevProcessLinuxSource || identity.RootStart != "linux:1000" || identity.Digest == ([32]byte{}) {
		t.Fatalf("generic Node/Python foreground rejected: %+v %v", identity, err)
	}
	after, err := os.ReadFile(filepath.Join(proc, "102/stat"))
	if err != nil || string(before) != string(after) {
		t.Fatal("probe modified fixture metadata")
	}
}

func TestJevProcessLinuxSnapshotRevalidation(t *testing.T) {
	for _, mutation := range []string{"child restart", "ancestor restart", "member added", "foreground switch", "root exit"} {
		t.Run(mutation, func(t *testing.T) {
			proc := jevProcFixtureTree(t)
			calls := 0
			identity, err := probeJevProcessIdentityWithSnapshots(context.Background(), 100, jevProcessLinuxSource, func(ctx context.Context, _ int) (jevProcessSnapshot, error) {
				calls++
				snapshot, err := readJevLinuxProcessSnapshot(ctx, proc)
				if err == nil && calls == 1 {
					tree := jevTestProcessTree()
					switch mutation {
					case "child restart":
						r := tree[103]
						r.StartOrder++
						writeJevProcFixture(t, proc, r, "S", "python")
					case "ancestor restart":
						r := tree[101]
						r.StartOrder++
						writeJevProcFixture(t, proc, r, "S", "bash")
					case "member added":
						writeJevProcFixture(t, proc, jevProcessRecord{PID: 104, Parent: 102, Group: 102, TTY: 34818, Foreground: 102, StartOrder: 1400}, "S", "arbitrary harness")
					case "foreground switch":
						r := tree[103]
						r.Foreground = 101
						writeJevProcFixture(t, proc, r, "S", "python")
					case "root exit":
						if err := os.Remove(filepath.Join(proc, "100/stat")); err != nil {
							t.Fatal(err)
						}
					}
				}
				return snapshot, err
			})
			if !errors.Is(err, errJevProcessUnavailable) || identity != (jevProcessIdentity{}) {
				t.Fatalf("mixed observation accepted: %+v %v", identity, err)
			}
		})
	}
}

func TestJevProcessLinuxRejectsIncompleteAndDeadForeground(t *testing.T) {
	for _, mode := range []string{"missing root", "malformed metadata", "zero start", "stopped", "zombie", "missing owned foreground"} {
		t.Run(mode, func(t *testing.T) {
			proc := jevProcFixtureTree(t)
			tree := jevTestProcessTree()
			switch mode {
			case "missing root":
				if err := os.Remove(filepath.Join(proc, "100/stat")); err != nil {
					t.Fatal(err)
				}
			case "malformed metadata":
				if err := os.WriteFile(filepath.Join(proc, "102/stat"), []byte("malformed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "zero start":
				r := tree[102]
				r.StartOrder = 0
				writeJevProcFixture(t, proc, r, "S", "node")
			case "stopped":
				writeJevProcFixture(t, proc, tree[102], "T", "node")
			case "zombie":
				writeJevProcFixture(t, proc, tree[102], "Z", "node")
			case "missing owned foreground":
				for pid, r := range tree {
					if pid != 100 {
						r.Foreground = 900
						writeJevProcFixture(t, proc, r, "S", "unknown")
					}
				}
			}
			identity, err := probeJevProcessIdentityAt(context.Background(), proc, 100)
			if !errors.Is(err, errJevProcessUnavailable) || identity != (jevProcessIdentity{}) {
				t.Fatalf("incomplete metadata accepted: %+v %v", identity, err)
			}
		})
	}
}

func TestJevProcessLinuxMultipleOwnedTerminalsAndUnrelatedZombie(t *testing.T) {
	proc := jevProcFixtureTree(t)
	writeJevProcFixture(t, proc, jevProcessRecord{PID: 200, Parent: 100, Group: 200, TTY: 34819, Foreground: 200, StartOrder: 2000}, "S", "different harness")
	writeJevProcFixture(t, proc, jevProcessRecord{PID: 900, Parent: 1, StartOrder: 9000}, "Z", "unrelated zombie")
	identity, err := probeJevProcessIdentityAt(context.Background(), proc, 100)
	if err != nil {
		t.Fatalf("multiple owned foreground sets unavailable: %v", err)
	}
	r := jevProcessRecord{PID: 200, Parent: 100, Group: 200, TTY: 34819, Foreground: 200, StartOrder: 2001}
	writeJevProcFixture(t, proc, r, "S", "different harness")
	next, err := probeJevProcessIdentityAt(context.Background(), proc, 100)
	if err != nil || next == identity {
		t.Fatalf("secondary terminal incarnation omitted: %+v %v", next, err)
	}
}

func TestJevProcessLinuxCancellationBeforeFilesystem(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	identity, err := probeJevProcessIdentityAt(ctx, "/nonexistent-jev-fixture", 100)
	if !errors.Is(err, context.Canceled) || identity != (jevProcessIdentity{}) {
		t.Fatalf("cancelled probe inspected filesystem: %+v %v", identity, err)
	}
}
