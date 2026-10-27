package main

import (
	"bytes"

	"fmt"

	"strings"
	"sync"

	"time"
	"unicode"
)

func formatArgvLine(argv []string, fallbackName string) string {
	if len(argv) == 0 {
		return fallbackName
	}
	parts := make([]string, len(argv))
	for i, arg := range argv {
		if strings.ContainsRune(arg, '"') || strings.IndexFunc(arg, unicode.IsSpace) >= 0 {
			parts[i] = fmt.Sprintf("%q", arg)
		} else {
			parts[i] = arg
		}
	}
	return strings.Join(parts, " ")
}

// tailBuf keeps the LAST limit bytes written — verification failures put
// their evidence at the end of the output. It is safe for concurrent writers:
// os/exec uses separate stdout and stderr pump goroutines when the two streams
// have distinct writer values. Readers are covered by the same lock, so Tail()
// may be called while those pumps are still writing (release-train step
// reporting does exactly that) — read through Tail(), never the buf field. The
// abandoned-wait paths in the verify runner and releaseTrainRunCmd still skip
// the buffer entirely, not for safety but because a tail captured mid-teardown
// is not evidence (see the comment at the abandoned branch below).
//
// A buffer with firstLimit > 0 additionally keeps a bounded window around
// the run's FIRST failure/race marker, because that evidence lives at the
// start of a long run's output and a tail-only policy discards it. Tail()
// keeps its exact previous meaning; Evidence() is the composed view.
type tailBuf struct {
	mu    sync.Mutex
	limit int
	buf   []byte

	// first is filled once and then frozen. carry holds the bounded diagnostic
	// prefix plus marker overlap needed across two writes; it is dropped
	// as soon as the window opens.
	firstLimit int
	first      []byte
	firstFound bool
	firstAt    int64
	carry      []byte
	written    int64
}

func waitOrAbandon(done <-chan error, grace time.Duration) (err error, abandoned bool) {
	select {
	case err = <-done:
		return err, false
	case <-time.After(grace):
		return nil, true
	}
}

const (
	verifyCmdTimeoutDefault = 10 * time.Minute
	verifyLogTailLimit      = 8 * 1024
	// verifyLogFirstFailLimit bounds the window kept from the FIRST failure
	// of a run. `go test` prints `--- FAIL:` records and race reports where
	// they happen, so a long suite's terminal 8 KiB routinely retains only the
	// package footer: the named test is clipped away, the failure class
	// collapses to package_alarm, and repair rounds oscillate having never
	// seen a test name. docs/audits/2026-09-01-package-alarm-recurrence-
	// diagnosis.md documents that exact evidence path.
	verifyLogFirstFailLimit = 4 * 1024
	repairTailLimit         = 8 << 10
	setupCmdTimeoutDefault  = 20 * time.Minute

	verifyOutcomePassed          = "passed"
	verifyOutcomeFailed          = "failed"
	verifyOutcomeNothingRan      = "nothing_ran"
	verifyOutcomeNothingRequired = "nothing_required"
)

func (b *tailBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.captureFirstFailure(p)
	b.written += int64(len(p))
	b.buf = append(b.buf, p...)
	b.buf = runeSafeTailBytes(b.buf, b.limit)
	return len(p), nil
}

// captureFirstFailure copies at most firstLimit bytes around the first
// failure/race marker. Go prints assertion diagnostics BEFORE its FAIL line;
// retain a bounded prefix so owners and repair workers see the actual cause.
// The scan costs one search per write until
// the first red and nothing at all after the window fills. Passing runs keep
// only the small bounded prefix in addition to the ordinary output tail.
func (b *tailBuf) captureFirstFailure(p []byte) {
	if b.firstLimit <= 0 || len(b.first) >= b.firstLimit {
		return
	}
	if b.firstFound {
		b.first = append(b.first, p[:min(len(p), b.firstLimit-len(b.first))]...)
		return
	}
	scan, offset := p, b.written
	if len(b.carry) > 0 {
		scan = append(append(make([]byte, 0, len(b.carry)+len(p)), b.carry...), p...)
		offset -= int64(len(b.carry))
	}
	if i := firstFailureIndex(scan); i >= 0 {
		start := max(0, i-min(1024, b.firstLimit/4))
		b.firstFound, b.firstAt, b.carry = true, offset+int64(start), nil
		b.first = append(b.first, scan[start:min(len(scan), start+b.firstLimit)]...)
		return
	}
	keep := firstFailureScanBack + min(1024, b.firstLimit/4)
	if len(scan) > keep {
		scan = scan[len(scan)-keep:]
	}
	b.carry = append(b.carry[:0], scan...)
}

func (b *tailBuf) Tail() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// Evidence is Tail() plus the retained first-failure window when clipping
// actually dropped it. When the tail still covers the window — a short run, or
// a failure near the end — the tail is returned verbatim, so nothing is
// duplicated and green output is untouched.
func (b *tailBuf) Evidence() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	tail := string(b.buf)
	if len(b.first) == 0 || b.firstAt >= b.written-int64(len(b.buf)) {
		return tail
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "… (first failure context: %d bytes from offset %d of the output)\n", len(b.first), b.firstAt)
	sb.Write(b.first)
	if !bytes.HasSuffix(b.first, []byte("\n")) {
		sb.WriteByte('\n')
	}
	sb.WriteString(evidenceElision + "\n")
	sb.WriteString(tail)
	return sb.String()
}

func firstFailureIndex(buf []byte) int {
	best := -1
	for _, marker := range firstFailureMarkers {
		if i := bytes.Index(buf, []byte(marker)); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best
}

// firstFailureMarkers anchor the first diagnostic record of a verification
// run. Each begins its own line (subtest FAIL records are only indented), so a
// window opened at the marker stays line-anchored for the failure classifier's
// `^\s*--- FAIL:` pattern.
var firstFailureMarkers = []string{"--- FAIL: ", "WARNING: DATA RACE", "panic: ", "fatal error: "}

// firstFailureScanBack is the longest marker minus one byte: the overlap a
// scan must retain so a marker split across two writes is still found.
const firstFailureScanBack = len("WARNING: DATA RACE") - 1

// evidenceElision separates a retained first-failure window from the run
// tail. It deliberately matches neither `--- FAIL:` nor `FAIL <pkg> <n>s`, so
// the separator itself can never be read as a failure record.
const evidenceElision = "… (earlier output elided; run tail follows)"
