package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestDurableLogBootBanner is the core promise: after attaching, a run is
// demarcated on disk and every subsequent slog record is persisted — while
// the in-memory ring the crash reporter samples keeps working unchanged.
func TestDurableLogBootBanner(t *testing.T) {
	dir := t.TempDir()
	prev := slog.Default()
	t.Cleanup(func() {
		slog.SetDefault(prev)
		// Un-aim the runtime's crash output: leaving it pointed at a temp dir
		// that t.TempDir is about to delete would silently swallow the
		// traceback of any later test in this binary. (Crash output is an
		// *additional* sink — stderr always gets it too — so clearing is safe.)
		_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
	})

	if err := attachDurableLogAt(dir); err != nil {
		t.Fatalf("attachDurableLogAt: %v", err)
	}
	slog.Info("hello-durable", "k", "v")

	body, err := os.ReadFile(filepath.Join(dir, durableLogName))
	if err != nil {
		t.Fatalf("read durable log: %v", err)
	}
	out := string(body)
	for _, want := range []string{
		"solud boot",
		"version=" + version,
		"pid=" + strconv.Itoa(os.Getpid()),
		"hello-durable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("durable log missing %q; got:\n%s", want, out)
		}
	}

	// The ring wrap must survive the handler rebuild — crash_report.go's
	// recentLogContext reads from it, and losing it would trade one kind of
	// forensics for another.
	ring := strings.Join(globalLogRing.tail(50), "\n")
	if !strings.Contains(ring, "hello-durable") {
		t.Errorf("record did not reach the in-memory log ring; tail:\n%s", ring)
	}
}

// TestDurableLogRotation pins the size bound: the live file stays under the
// cap and at most two archived generations are kept, so an unattended daemon
// cannot fill a disk with its own diary.
func TestDurableLogRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, durableLogName)
	w, err := newRotatingLogWriter(path, 200)
	if err != nil {
		t.Fatalf("newRotatingLogWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	// ~64 bytes a record, 200-byte cap: comfortably past two rotations.
	for i := 0; i < 40; i++ {
		if _, err := fmt.Fprintf(w, "record %02d %s\n", i, strings.Repeat("x", 50)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected %s.1 after rotation: %v", durableLogName, err)
	}
	if _, err := os.Stat(path + ".2"); err != nil {
		t.Fatalf("expected %s.2 after two rotations: %v", durableLogName, err)
	}
	if _, err := os.Stat(path + ".3"); err == nil {
		t.Errorf("rotation kept a third generation; generations must be bounded at .2")
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat live log: %v", err)
	}
	if st.Size() > 200 {
		t.Errorf("live log is %d bytes, over the 200-byte cap", st.Size())
	}
}

// TestDurableLogPanicCrashOutput is the whole point of the exercise: an
// UNRECOVERED panic is written by the Go runtime to stderr, and a self-update
// successor has no stderr worth the name. debug.SetCrashOutput must land the
// traceback in the durable log instead. Verified out-of-process because the
// panic has to actually kill the program.
func TestDurableLogPanicCrashOutput(t *testing.T) {
	if os.Getenv("SOLU_DURABLE_PANIC_CHILD") == "1" {
		attachDurableLog()
		panic("durable-log-dying-words")
	}

	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestDurableLogPanicCrashOutput", "-test.v")
	cmd.Env = append(os.Environ(),
		"SOLU_DURABLE_PANIC_CHILD=1",
		"SOLU_DURABLE_LOG_DIR="+dir,
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("child was supposed to die of its panic; it exited 0:\n%s", out)
	}

	body, readErr := os.ReadFile(filepath.Join(dir, durableLogName))
	if readErr != nil {
		t.Fatalf("read durable log after child panic: %v (child output:\n%s)", readErr, out)
	}
	got := string(body)
	if !strings.Contains(got, "solud boot") {
		t.Errorf("child never wrote its boot banner; got:\n%s", got)
	}
	if !strings.Contains(got, "panic:") || !strings.Contains(got, "durable-log-dying-words") {
		t.Errorf("panic message missing from durable log; got:\n%s", got)
	}
	if !strings.Contains(got, "goroutine ") {
		t.Errorf("goroutine traceback missing from durable log; got:\n%s", got)
	}
}

// TestDurableLogPanicAfterRotation covers the half of SetCrashOutput that is
// easy to get wrong: the runtime dups the fd, so a rotation that does not
// re-arm leaves the traceback going into the ARCHIVED file — a daemon that
// ran long enough to rotate would die into solud.log.1 while whoever is
// tailing solud.log sees nothing at all.
func TestDurableLogPanicAfterRotation(t *testing.T) {
	if os.Getenv("SOLU_DURABLE_ROTATE_PANIC_CHILD") == "1" {
		attachDurableLog()
		if durableLog == nil {
			fmt.Fprintln(os.Stderr, "durable log never attached")
			os.Exit(3)
		}
		slog.Info("before-rotation")
		durableLog.mu.Lock()
		err := durableLog.rotateLocked()
		durableLog.mu.Unlock()
		if err != nil {
			fmt.Fprintln(os.Stderr, "rotate:", err)
			os.Exit(3)
		}
		slog.Info("after-rotation")
		panic("dying-words-after-rotation")
	}

	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestDurableLogPanicAfterRotation", "-test.v")
	cmd.Env = append(os.Environ(),
		"SOLU_DURABLE_ROTATE_PANIC_CHILD=1",
		"SOLU_DURABLE_LOG_DIR="+dir,
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("child was supposed to die of its panic; it exited 0:\n%s", out)
	}

	live, readErr := os.ReadFile(filepath.Join(dir, durableLogName))
	if readErr != nil {
		t.Fatalf("read live durable log: %v (child output:\n%s)", readErr, out)
	}
	archived, readErr := os.ReadFile(filepath.Join(dir, durableLogName+".1"))
	if readErr != nil {
		t.Fatalf("read archived durable log: %v (child output:\n%s)", readErr, out)
	}
	if !strings.Contains(string(archived), "before-rotation") {
		t.Errorf("pre-rotation record is not in the archive; got:\n%s", archived)
	}
	got := string(live)
	if !strings.Contains(got, "after-rotation") {
		t.Errorf("post-rotation record did not follow the rotation into the live file; got:\n%s", got)
	}
	for _, want := range []string{"panic:", "dying-words-after-rotation", "goroutine "} {
		if !strings.Contains(got, want) {
			t.Errorf("traceback missing %q from the LIVE log after rotation; got:\n%s", want, got)
		}
	}
}

// TestDurableLogPanicAfterConcurrentWriteRotation is the same promise as
// TestDurableLogPanicAfterRotation, taken through the path production actually
// uses.
//
// That test rotates by calling rotateLocked() directly, from the one goroutine
// that then panics. Real rotation happens inside Write, on whichever goroutine
// happened to overflow the cap, while every other goroutine in the daemon is
// still logging — and the panic arrives from a goroutine that did no rotating
// at all. If the re-arm were outside the lock, or if Write's rotation path
// forgot it, the runtime's crash fd would still point at an inode that has
// since been renamed to .1, then .2, then unlinked: the traceback would land in
// an archive, or in nothing at all, and the live log would end mid-sentence.
// That is exactly the "daemon died leaving no evidence" shape this whole change
// exists to remove.
//
// The child uses a small cap so the real Write path rotates dozens of times
// under contention rather than after 8 MiB of records. Run it under -race and
// it is also the lock-ordering test for armCrash/rotateLocked.
func TestDurableLogPanicAfterConcurrentWriteRotation(t *testing.T) {
	if os.Getenv("SOLU_DURABLE_CONCURRENT_CHILD") == "1" {
		durableLogConcurrentRotationChild()
		return
	}

	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestDurableLogPanicAfterConcurrentWriteRotation", "-test.v")
	cmd.Env = append(os.Environ(),
		"SOLU_DURABLE_CONCURRENT_CHILD=1",
		"SOLU_DURABLE_CONCURRENT_DIR="+dir,
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("child was supposed to die of its panic; it exited 0:\n%s", out)
	}

	path := filepath.Join(dir, durableLogName)
	live, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read live durable log: %v (child output:\n%s)", readErr, out)
	}

	// Rotation must actually have happened through Write, and must still be
	// bounded: live + .1 + .2 and nothing else.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read log dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{durableLogName, durableLogName + ".1", durableLogName + ".2"}
	if len(names) != len(want) {
		t.Fatalf("durable log dir holds %v, want exactly %v (either Write never rotated, or generations are unbounded)", names, want)
	}
	for _, w := range want {
		if _, statErr := os.Stat(filepath.Join(dir, w)); statErr != nil {
			t.Fatalf("expected generation %s after concurrent rotation: %v (dir holds %v)", w, statErr, names)
		}
	}

	got := string(live)
	for _, needle := range []string{"last-record-before-panic", "panic:", "dying-words-after-concurrent-rotation", "goroutine "} {
		if !strings.Contains(got, needle) {
			t.Errorf("LIVE durable log is missing %q after Write-triggered rotation; got:\n%s", needle, got)
		}
	}

	// And the archives must NOT hold the traceback: finding it there is the
	// precise signature of a crash fd left pointing at a renamed file.
	for _, archive := range []string{durableLogName + ".1", durableLogName + ".2"} {
		body, readErr := os.ReadFile(filepath.Join(dir, archive))
		if readErr != nil {
			t.Fatalf("read %s: %v", archive, readErr)
		}
		if strings.Contains(string(body), "dying-words-after-concurrent-rotation") {
			t.Errorf("the traceback landed in the archived %s — the crash fd was not re-armed by Write's rotation", archive)
		}
	}
}

// durableLogConcurrentRotationChild mirrors attachDurableLogAt's wiring (same
// handler, same fan-out, same armCrash) with a small cap, hammers the writer
// from several goroutines until it has rotated many times, and then dies.
func durableLogConcurrentRotationChild() {
	dir := os.Getenv("SOLU_DURABLE_CONCURRENT_DIR")
	w, err := newRotatingLogWriter(filepath.Join(dir, durableLogName), 4096)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open durable log:", err)
		os.Exit(3)
	}
	// io.Discard for the console leg: the parent reads the file, and the test
	// runner's own stderr does not need 1600 records.
	slog.SetDefault(slog.New(buildLogHandler(durableSinks(w, io.Discard))))
	w.armCrash()

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				slog.Info("hammer", "goroutine", g, "i", i, "pad", strings.Repeat("x", 64))
			}
		}(g)
	}
	wg.Wait()

	slog.Info("last-record-before-panic")
	panic("dying-words-after-concurrent-rotation")
}

// TestDurableLogSurvivesBrokenStderr is the launcher-independence promise held
// to its word. io.MultiWriter stops at the first writer that errors, so the
// durable file has to sit AHEAD of the console: a daemon started with stderr
// closed (`solud 2>&-`) or holding a dying inherited fd is precisely the
// daemon whose records nobody else is keeping.
func TestDurableLogSurvivesBrokenStderr(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, durableLogName)
	w, err := newRotatingLogWriter(path, durableLogMaxBytes)
	if err != nil {
		t.Fatalf("newRotatingLogWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	console := &brokenWriter{}
	slog.SetDefault(slog.New(buildLogHandler(durableSinks(w, console))))
	slog.Info("stderr-is-gone", "k", "v")

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read durable log: %v", err)
	}
	if !strings.Contains(string(body), "stderr-is-gone") {
		t.Errorf("a failing console silenced the durable log; file:\n%s", body)
	}
	if console.calls == 0 {
		t.Errorf("the console sink was never even attempted; stderr must still be written")
	}
}

// brokenWriter is a console whose every write fails, the way a closed or
// torn-down stderr fd does (EBADF).
type brokenWriter struct{ calls int }

func (b *brokenWriter) Write(p []byte) (int, error) {
	b.calls++
	return 0, errors.New("bad file descriptor")
}

// TestDurableLogOversizedRecordBound pins the honest version of the size
// bound: a record is never split (half a traceback is worth nothing), so one
// oversized record lands whole and the very next write rotates it away.
func TestDurableLogOversizedRecordBound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, durableLogName)
	const capBytes = 200
	w, err := newRotatingLogWriter(path, capBytes)
	if err != nil {
		t.Fatalf("newRotatingLogWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	huge := strings.Repeat("B", 4*capBytes) + "\n"
	if _, err := w.Write([]byte(huge)); err != nil {
		t.Fatalf("write oversized record: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read live log: %v", err)
	}
	if string(body) != huge {
		t.Fatalf("oversized record was split or dropped; live log is %d bytes", len(body))
	}

	if _, err := w.Write([]byte("back-under-the-cap\n")); err != nil {
		t.Fatalf("write follow-up record: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat live log: %v", err)
	}
	if st.Size() > capBytes {
		t.Errorf("live log stayed %d bytes after the oversized record; the next write must rotate it out", st.Size())
	}
	archived, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("read archived log: %v", err)
	}
	if string(archived) != huge {
		t.Errorf("the oversized record did not survive into the archive")
	}
}

// TestDurableLogHonoursLogFormat: the durable file and the console are built
// from one handler, so LOG_FORMAT has to reach both. A JSON deployment whose
// solud.log came out as text would be unparseable exactly when it matters.
func TestDurableLogHonoursLogFormat(t *testing.T) {
	dir := t.TempDir()
	prev := slog.Default()
	t.Cleanup(func() {
		slog.SetDefault(prev)
		_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
	})

	t.Setenv("LOG_FORMAT", "json")
	if err := attachDurableLogAt(dir); err != nil {
		t.Fatalf("attachDurableLogAt: %v", err)
	}
	slog.Info("hello-json", "k", "v")

	body, err := os.ReadFile(filepath.Join(dir, durableLogName))
	if err != nil {
		t.Fatalf("read durable log: %v", err)
	}
	found := false
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if line == "" || strings.HasPrefix(line, "===") {
			continue // the plain-text boot banner, deliberately not JSON
		}
		// Lines that do not parse are not failed on directly: another test's
		// stray goroutine may share slog.Default() for the moment this test
		// owns it. The record we asked for has to be there, as JSON — under a
		// text handler it would read `msg=hello-json` and never parse, which
		// is precisely the drift this test is here to catch.
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec["msg"] == "hello-json" && rec["k"] == "v" {
			found = true
		}
	}
	if !found {
		t.Errorf("LOG_FORMAT=json did not reach the durable log; no JSON record for hello-json in:\n%s", body)
	}
}

// TestDurableLogOffDisablesIt keeps the escape hatch honest: side-port dev
// daemons and tests must be able to opt out of touching the real file.
func TestDurableLogOffDisablesIt(t *testing.T) {
	dir := t.TempDir()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	t.Setenv("SOLU_DURABLE_LOG_DIR", "off")
	attachDurableLog()
	if entries, err := os.ReadDir(dir); err == nil && len(entries) != 0 {
		t.Errorf("SOLU_DURABLE_LOG_DIR=off still wrote %d entries", len(entries))
	}

	// And an unwritable directory is a warning, not a boot failure.
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker file: %v", err)
	}
	t.Setenv("SOLU_DURABLE_LOG_DIR", filepath.Join(blocked, "sub"))
	attachDurableLog() // must not panic or exit
}
