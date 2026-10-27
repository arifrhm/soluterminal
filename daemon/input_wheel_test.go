package main

import (
	"bytes"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// The measured ConPTY facts these tests encode: SGR wheel reports do nothing
// (conhost eats them), PageUp/PageDown scroll Claude Code's transcript, and
// arrow keys recall prompt history (so the translator must NEVER emit them).

func wheelUp(n int) string   { return strings.Repeat("\x1b[<68;25;19M", n) } // 64|4 (shift bit set, as the mobile client emits)
func wheelDown(n int) string { return strings.Repeat("\x1b[<69;25;19M", n) }

func translate(t *testing.T, acc *wheelAccum, in string, pageLines int, at time.Time) string {
	t.Helper()
	return string(translateWheelBytes([]byte(in), acc, pageLines, at))
}

func TestNonWheelBytesPassThroughIdentical(t *testing.T) {
	var acc wheelAccum
	now := time.Now()
	for _, in := range []string{
		"hello world\r",
		"\x1b[A\x1b[B",                 // arrow keys someone actually typed
		"\x1b[1;5R",                    // CPR reply
		"\x1b[<0;10;10M\x1b[<0;10;10m", // mouse click press+release
		"\x1b[<66;5;5M",                // horizontal wheel
		"\x1b[<32;5;5M",                // motion event (32 flag)
		"\x1b[200~pasted\x1b[201~",
		"\x1bOA",         // SS3 arrow
		"\x1b[<68;25;19", // TRUNCATED wheel report — can't identify, pass through
	} {
		got := translate(t, &acc, in, 36, now)
		if got != in {
			t.Fatalf("input %q must pass through identical, got %q", in, got)
		}
	}
	if acc.lines != 0 {
		t.Fatalf("non-wheel input must not touch the accumulator, got %d", acc.lines)
	}
}

func TestFullPageOfWheelUpBecomesOnePageUp(t *testing.T) {
	var acc wheelAccum
	got := translate(t, &acc, wheelUp(36), 36, time.Now())
	if got != "\x1b[5~" {
		t.Fatalf("36 wheel-up lines at pageLines=36 = one PageUp, got %q", got)
	}
	if acc.lines != 0 {
		t.Fatalf("accumulator should be drained, got %d", acc.lines)
	}
}

func TestShortSegmentsAccumulateAcrossWrites(t *testing.T) {
	var acc wheelAccum
	now := time.Now()
	for i := 0; i < 5; i++ { // 5 × 8 = 40 lines in quick segments
		got := translate(t, &acc, wheelUp(8), 36, now.Add(time.Duration(i)*100*time.Millisecond))
		want := ""
		if i == 4 { // crosses 36 at the 5th segment
			want = "\x1b[5~"
		}
		if got != want {
			t.Fatalf("segment %d: got %q want %q (acc=%d)", i, got, want, acc.lines)
		}
	}
	if acc.lines != 4 {
		t.Fatalf("leftover should be 40-36=4, got %d", acc.lines)
	}
}

func TestWheelDownBecomesPageDown(t *testing.T) {
	var acc wheelAccum
	got := translate(t, &acc, wheelDown(80), 36, time.Now())
	if got != "\x1b[6~\x1b[6~" {
		t.Fatalf("80 wheel-down at pageLines=36 = two PageDown, got %q", got)
	}
	if acc.lines != -8 {
		t.Fatalf("leftover should be -8, got %d", acc.lines)
	}
}

func TestDirectionFlipResetsAccumulator(t *testing.T) {
	var acc wheelAccum
	now := time.Now()
	translate(t, &acc, wheelUp(30), 36, now) // 30 up, below threshold
	got := translate(t, &acc, wheelDown(10), 36, now.Add(200*time.Millisecond))
	if got != "" {
		t.Fatalf("flip starts fresh: 10 down < page, got %q", got)
	}
	if acc.lines != -10 {
		t.Fatalf("accumulator after flip should be -10, got %d", acc.lines)
	}
}

func TestStaleAccumulatorResets(t *testing.T) {
	var acc wheelAccum
	now := time.Now()
	translate(t, &acc, wheelUp(30), 36, now)
	got := translate(t, &acc, wheelUp(10), 36, now.Add(3*time.Second))
	if got != "" {
		t.Fatalf("stale leftover must not carry into a new gesture, got %q", got)
	}
	if acc.lines != 10 {
		t.Fatalf("fresh gesture should hold 10, got %d", acc.lines)
	}
}

func TestMixedPayloadPreservesNonWheelBytes(t *testing.T) {
	var acc wheelAccum
	in := "\x1b[1;5R" + wheelUp(36) + "\x1b[0n"
	got := translate(t, &acc, in, 36, time.Now())
	if got != "\x1b[1;5R\x1b[0n\x1b[5~" {
		t.Fatalf("replies preserved + page key appended, got %q", got)
	}
}

func TestX10WheelCounted(t *testing.T) {
	var acc wheelAccum
	// X10: ESC [ M cb cx cy with cb = 32+button; wheel-up button 64 → cb 96.
	in := strings.Repeat("\x1b[M\x60\x2a\x2a", 36)
	got := translate(t, &acc, in, 36, time.Now())
	if got != "\x1b[5~" {
		t.Fatalf("36 X10 wheel-up = one PageUp, got %q", got)
	}
}

func TestUnmodifiedSGRWheelCounted(t *testing.T) {
	var acc wheelAccum
	in := strings.Repeat("\x1b[<64;1;1M", 36) + strings.Repeat("\x1b[<65;1;1M", 0)
	got := translate(t, &acc, in, 36, time.Now())
	if got != "\x1b[5~" {
		t.Fatalf("plain button-64 wheel must count, got %q", got)
	}
}

func TestSGRWheelReleaseConsumedNotCounted(t *testing.T) {
	var acc wheelAccum
	got := translate(t, &acc, "\x1b[<64;1;1m", 36, time.Now())
	if got != "" {
		t.Fatalf("a wheel 'release' is dropped, not forwarded, got %q", got)
	}
	if acc.lines != 0 {
		t.Fatalf("release must not count as a line, got %d", acc.lines)
	}
}

// wheelTestPane builds a minimal alt-screen pane whose agent end is a pipe,
// with the Windows translation force-enabled, and a reader goroutine that
// collects every frame payload the pane writes.
func wheelTestPane(t *testing.T) (*Pane, func() string) {
	t.Helper()
	prev := wheelNeedsPageKeys
	wheelNeedsPageKeys = true
	t.Cleanup(func() { wheelNeedsPageKeys = prev })

	paneEnd, agentEnd := net.Pipe()
	p := &Pane{
		ID:        "pane_wheel_flush_test",
		Status:    PaneRunning,
		Cols:      80,
		Rows:      30, // pageLines = 28
		agentConn: paneEnd,
		done:      make(chan struct{}),
		buffer:    newTestRing(),
		subs:      map[int64]*subEntry{},
	}
	p.altScreen = true

	var mu sync.Mutex
	var got bytes.Buffer
	go func() {
		for {
			_, fr, err := ReadFrame(agentEnd)
			if err != nil {
				return
			}
			mu.Lock()
			got.Write(fr)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		p.agentWriteMu.Lock()
		if p.wheelFlushTimer != nil {
			p.wheelFlushTimer.Stop()
		}
		p.agentWriteMu.Unlock()
		paneEnd.Close()
		agentEnd.Close()
	})
	return p, func() string {
		mu.Lock()
		defer mu.Unlock()
		return got.String()
	}
}

func TestShortFlickFlushesOnePageOnGestureEnd(t *testing.T) {
	p, received := wheelTestPane(t)
	// A realistic flick: 12 lines, well under pageLines=28. Measured live:
	// without the flush this scrolled NOTHING and the user reported "still
	// can't scroll".
	if _, err := p.Write([]byte(wheelUp(12))); err != nil {
		t.Fatal(err)
	}
	if got := received(); got != "" {
		t.Fatalf("nothing may be sent mid-gesture below a page, got %q", got)
	}
	time.Sleep(wheelFlushDelay + 200*time.Millisecond)
	if got := received(); got != "\x1b[5~" {
		t.Fatalf("gesture end must flush exactly one PageUp, got %q", got)
	}
	p.agentWriteMu.Lock()
	acc := p.wheelAcc.lines
	p.agentWriteMu.Unlock()
	if acc != 0 {
		t.Fatalf("flush must drain the accumulator, got %d", acc)
	}
}

func TestTinyJitterNeverFlushes(t *testing.T) {
	p, received := wheelTestPane(t)
	if _, err := p.Write([]byte(wheelDown(3))); err != nil { // < minFlickLines
		t.Fatal(err)
	}
	time.Sleep(wheelFlushDelay + 200*time.Millisecond)
	if got := received(); got != "" {
		t.Fatalf("3 jitter lines must not page, got %q", got)
	}
}

func TestLongDragPagesImmediatelyThenFlushesRemainder(t *testing.T) {
	p, received := wheelTestPane(t)
	// 40 lines in one gesture: crosses pageLines=28 mid-drag (immediate
	// PageUp) and leaves 12 — flushed as one more page at gesture end.
	if _, err := p.Write([]byte(wheelUp(40))); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for received() != "\x1b[5~" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := received(); got != "\x1b[5~" {
		t.Fatalf("crossing a page mid-drag pages immediately, got %q", got)
	}
	time.Sleep(wheelFlushDelay + 200*time.Millisecond)
	if got := received(); got != "\x1b[5~\x1b[5~" {
		t.Fatalf("remainder (12 lines) flushes one more page, got %q", got)
	}
}

func TestNormalBufferNeverTranslates(t *testing.T) {
	p, received := wheelTestPane(t)
	p.subsMu.Lock()
	p.altScreen = false
	p.subsMu.Unlock()
	if _, err := p.Write([]byte(wheelUp(40))); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	want := wheelUp(40)
	for received() != want && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := received(); got != want {
		t.Fatalf("normal buffer passes wheel reports through untouched, got %q", got)
	}
	time.Sleep(wheelFlushDelay + 200*time.Millisecond)
	if got := received(); got != want {
		t.Fatalf("and never flushes page keys later, got %q", got)
	}
}

func TestNeverEmitsArrowKeys(t *testing.T) {
	// Arrow keys recall Claude Code prompt history — emitting them from a
	// scroll would type old prompts into the composer. Pin it.
	var acc wheelAccum
	now := time.Now()
	var all bytes.Buffer
	for i := 0; i < 20; i++ {
		all.WriteString(translate(t, &acc, wheelUp(7), 36, now.Add(time.Duration(i)*50*time.Millisecond)))
		all.WriteString(translate(t, &acc, wheelDown(3), 36, now.Add(time.Duration(i)*50*time.Millisecond+25*time.Millisecond)))
	}
	if bytes.Contains(all.Bytes(), []byte("\x1b[A")) || bytes.Contains(all.Bytes(), []byte("\x1b[B")) {
		t.Fatalf("translator emitted arrow keys: %q", all.String())
	}
}
