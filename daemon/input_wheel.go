package main

import (
	"runtime"
	"time"
)

// ConPTY eats mouse-wheel reports. A client scrolling an alt-screen pane
// (phone drag→wheel translation, desktop mouse wheel) writes SGR wheel
// reports into the PTY; conhost never delivers them to the app, so the
// screen sits still. Measured live 2026-07-26 on a ConPTY Claude pane:
// 5× SGR wheel-up → grid unchanged; PageUp/PageDown → transcript scrolls;
// and arrow keys are actively harmful — Claude Code binds them to prompt-
// HISTORY recall, not scrolling. So on Windows the daemon converts wheel
// lines into page keys: wheel reports accumulate per pane and one
// PageUp/PageDown is emitted per viewport-height of lines. A full-screen
// drag on the phone ≈ one page — the direct-manipulation mapping a touch
// user expects. Linux daemons pass wheel reports through untouched: apps
// under a real PTY receive and handle them natively (which is why WSL
// panes always scrolled and PowerShell panes never did).
//
// Only applied when the pane is in the ALTERNATE screen. In the normal
// buffer clients scroll locally and never emit wheel reports; if one ever
// arrived, injecting page keys into a shell prompt would be worse than
// dropping bytes conhost drops anyway.
var wheelNeedsPageKeys = runtime.GOOS == "windows"

// wheelAccum carries leftover wheel lines between writes so short drag
// segments still add up to a page. A gap or direction flip starts fresh —
// leftovers from an abandoned gesture must not make the next one jumpy.
type wheelAccum struct {
	lines int // signed: >0 = toward history (wheel up)
	at    time.Time
}

const wheelAccumStale = 1200 * time.Millisecond

// A page key is only worth emitting mid-drag once a full page of lines has
// crossed — but most real scroll gestures are SHORT: a flick is 8–30 lines
// and reverses freely (measured live 2026-07-26: the first user test was 25
// lines with direction flips — the accumulator held everything back and the
// screen never moved, "still can't scroll"). So when a gesture goes quiet,
// the leftover lines are flushed as ONE page: flick = one page, long drag =
// page-per-screenful as it crosses plus at most one more at the end. Jitter
// below minFlickLines still dies in the accumulator.
const (
	wheelFlushDelay = 300 * time.Millisecond
	minFlickLines   = 5
)

var (
	seqPageUp   = []byte("\x1b[5~")
	seqPageDown = []byte("\x1b[6~")
)

// inAltScreen reports whether the pane's app is on the alternate screen,
// per the mode tracking readLoop already maintains for replay repair.
func (p *Pane) inAltScreen() bool {
	p.subsMu.RLock()
	v := p.altScreen
	p.subsMu.RUnlock()
	return v
}

// wheelPageLines is how many wheel lines make one page key: the current
// viewport height, so a full-screen drag on a phone scrolls one page.
func (p *Pane) wheelPageLines() int {
	p.mu.Lock()
	rows := int(p.Rows)
	p.mu.Unlock()
	if rows < 8 {
		rows = 8
	}
	return rows - 2
}

// scheduleWheelFlush (re)arms the gesture-end flush after a write that moved
// the accumulator. Caller must hold agentWriteMu.
func (p *Pane) scheduleWheelFlush() {
	if p.wheelFlushTimer != nil {
		p.wheelFlushTimer.Stop()
		p.wheelFlushTimer = nil
	}
	n := p.wheelAcc.lines
	if n >= -minFlickLines && n <= minFlickLines {
		return // too small to ever page — let it die or grow first
	}
	p.wheelFlushTimer = time.AfterFunc(wheelFlushDelay, p.flushWheelRemainder)
}

// flushWheelRemainder turns the leftover lines of a finished gesture into a
// single page key. Bypasses Pane.write on purpose: a synthesized page key
// must not be classified as the user typing (isTerminalReplyOnly would call
// ESC[5~ real input and re-arm the awaiting-input notifier) nor re-enter the
// wheel translator.
func (p *Pane) flushWheelRemainder() {
	p.agentWriteMu.Lock()
	defer p.agentWriteMu.Unlock()
	n := p.wheelAcc.lines
	p.wheelAcc.lines = 0
	if n >= -minFlickLines && n <= minFlickLines {
		return
	}
	key := seqPageUp
	if n < 0 {
		key = seqPageDown
	}
	p.mu.Lock()
	conn := p.agentConn
	p.mu.Unlock()
	if conn != nil {
		_ = WriteFrame(conn, FrameData, key)
	}
}

// translateWheelBytes strips vertical wheel reports (SGR and legacy X10)
// from b, accumulates their line count in acc, and appends one page key per
// pageLines full lines. All other bytes — typing, arrows, CPR/DA replies,
// mouse clicks, horizontal wheel — pass through byte-identical, in order.
// The page keys are appended after the passthrough bytes; wheel reports are
// interleaved with at most protocol chatter, never typed text, so ordering
// between them carries no meaning.
func translateWheelBytes(b []byte, acc *wheelAccum, pageLines int, now time.Time) []byte {
	if pageLines < 4 {
		pageLines = 4
	}
	out := make([]byte, 0, len(b))
	delta := 0
	i := 0
	for i < len(b) {
		n, dir := consumeWheelReport(b[i:])
		if n == 0 {
			out = append(out, b[i])
			i++
			continue
		}
		delta += dir
		i += n
	}
	if delta == 0 {
		return out
	}
	signFlip := acc.lines != 0 && (acc.lines > 0) != (delta > 0)
	if now.Sub(acc.at) > wheelAccumStale || signFlip {
		acc.lines = 0
	}
	acc.at = now
	acc.lines += delta
	pages := acc.lines / pageLines
	if pages != 0 {
		acc.lines -= pages * pageLines
		key, count := seqPageUp, pages
		if pages < 0 {
			key, count = seqPageDown, -pages
		}
		for k := 0; k < count; k++ {
			out = append(out, key...)
		}
	}
	return out
}

// consumeWheelReport returns (length, direction) of a VERTICAL wheel report
// at the start of b, or (0, 0) if b starts with anything else. direction is
// +1 for wheel-up (toward history), -1 for wheel-down. Modifier bits
// (shift/meta/ctrl) are ignored; horizontal wheel and non-wheel mouse
// reports are not consumed.
func consumeWheelReport(b []byte) (int, int) {
	if len(b) < 3 || b[0] != 0x1b || b[1] != '[' {
		return 0, 0
	}
	if b[2] == '<' {
		// SGR: ESC [ < button ; x ; y M   (wheel sends press only; a stray
		// 'm' release for a wheel button is consumed and not counted)
		i := 3
		btn := 0
		digits := false
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			btn = btn*10 + int(b[i]-'0')
			digits = true
			i++
		}
		if !digits || !isWheelButton(btn) {
			return 0, 0
		}
		semis := 0
		for i < len(b) && semis < 2 {
			c := b[i]
			if c == ';' {
				semis++
			} else if c < '0' || c > '9' {
				return 0, 0
			}
			i++
		}
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
		if i >= len(b) || (b[i] != 'M' && b[i] != 'm') {
			return 0, 0
		}
		if b[i] == 'm' {
			return i + 1, 0 // consumed, not a scroll line
		}
		return i + 1, wheelDirection(btn)
	}
	if b[2] == 'M' && len(b) >= 6 {
		// Legacy X10: ESC [ M cb cx cy, button = cb-32.
		btn := int(b[3]) - 32
		if !isWheelButton(btn) {
			return 0, 0
		}
		return 6, wheelDirection(btn)
	}
	return 0, 0
}

// isWheelButton reports whether an xterm mouse button code is a VERTICAL
// wheel event: wheel flag (64) set, motion flag (32) clear, and the low
// bits 0/1 (2/3 are horizontal wheel). Modifier bits 4|8|16 are masked.
func isWheelButton(btn int) bool {
	return btn&64 != 0 && btn&32 == 0 && btn&2 == 0
}

func wheelDirection(btn int) int {
	if btn&1 == 0 {
		return +1 // wheel up → toward history → PageUp
	}
	return -1
}
