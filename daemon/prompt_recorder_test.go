package main

import (
	"fmt"
	"strings"
	"testing"
)

// observeAll feeds chunks to a fresh recorder and returns all commits.
func observeAll(chunks ...string) []string {
	var r promptRecorder
	var out []string
	for _, c := range chunks {
		out = append(out, r.observe([]byte(c))...)
	}
	return out
}

func assertCommits(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("commits = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("commits[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPromptRecorderPlainCommit(t *testing.T) {
	t.Parallel()
	var r promptRecorder
	assertCommits(t, r.observe([]byte("hello world\r")), "hello world")
	// Buffer must reset after a commit — the next Enter alone is empty.
	assertCommits(t, r.observe([]byte("\r")))
}

func TestPromptRecorderStripsOSCColorReply(t *testing.T) {
	t.Parallel()
	got := observeAll("\x1b]10;rgb:e5e5/e9e9/f0f0\x1b\\can u help to look into flagging\r")
	assertCommits(t, got, "can u help to look into flagging")
}

func TestPromptRecorderSplitOSCAcrossCalls(t *testing.T) {
	t.Parallel()
	got := observeAll("\x1b]10;rgb:e5e5/", "e9e9/f0f0\x1b\\hello after reply\r")
	assertCommits(t, got, "hello after reply")
}

// Transport boundaries must not turn terminal replies into composer text or
// swallow the real prompt after a split string terminator.
func TestPromptRecorderSplitAtEveryOffset(t *testing.T) {
	t.Parallel()
	for _, control := range []string{
		"\x1b]10;rgb:e5e5/e9e9/f0f0\x1b\\",
		"\x1b]11;rgb:0000/0000/0000\x07",
		"\x1bP1$r0m\x1b\\", "\x1b[12;34R", "\x1b[?1;2c", "\x1b[I", "\x1bOA",
	} {
		for k := 1; k < len(control); k++ {
			var r promptRecorder
			r.observe([]byte(control[:k]))
			r.observe([]byte(control[k:]))
			if len(r.buf) != 0 || r.inStringControl {
				t.Fatalf("control %q split at %d left phantom draft %q", control, k, r.buf)
			}
			assertCommits(t, r.observe([]byte("real prompt\r")), "real prompt")
		}
	}
	// Also cover multiple boundaries and split bracketed-paste wrappers.
	for _, seq := range []string{
		"\x1b]10;rgb:aaaa/bbbb/cccc\x1b\\real prompt\r",
		"\x1b[200~real prompt\x1b[201~\r",
	} {
		var r promptRecorder
		var got []string
		for i := range seq {
			got = append(got, r.observe([]byte{seq[i]})...)
		}
		assertCommits(t, got, "real prompt")
	}
}

func TestPromptRecorderBracketedPasteThenDeferredEnter(t *testing.T) {
	t.Parallel()
	var r promptRecorder
	// Mirrors the voice/inject flow: bracketed-paste body in one write,
	// \r as its own write after the enter gap.
	assertCommits(t, r.observe([]byte("\x1b[200~fix the login bug\x1b[201~")))
	assertCommits(t, r.observe([]byte("\r")), "fix the login bug")
}

func TestPromptRecorderBackspace(t *testing.T) {
	t.Parallel()
	assertCommits(t, observeAll("helloo\x7f\r"), "hello")
	// Backspace removes one RUNE, not one byte ('é' is 2 bytes).
	assertCommits(t, observeAll("héé\x7f\r"), "hé")
	// 0x08 behaves like DEL.
	assertCommits(t, observeAll("ab\x08c\r"), "ac")
}

func TestPromptRecorderCtrlCClears(t *testing.T) {
	t.Parallel()
	assertCommits(t, observeAll("garbage\x03real prompt\r"), "real prompt")
	assertCommits(t, observeAll("junk\x15kept\r"), "kept")   // Ctrl+U
	assertCommits(t, observeAll("junk\x17kept2\r"), "kept2") // Ctrl+W
}

func TestPromptRecorderSwallowsCSI(t *testing.T) {
	t.Parallel()
	assertCommits(t, observeAll("\x1b[Aup arrow then\r"), "up arrow then")
	// SGR mid-text must not split or pollute the prompt.
	assertCommits(t, observeAll("a\x1b[32mb\r"), "ab")
}

func TestPromptRecorderLengthGates(t *testing.T) {
	t.Parallel()
	// Single menu digit — noise, skipped.
	assertCommits(t, observeAll("1\r"))
	// 2001 runes — paste blob, skipped.
	assertCommits(t, observeAll(strings.Repeat("x", 2001)+"\r"))
	// Exactly 2000 — commits.
	got := observeAll(strings.Repeat("x", 2000) + "\r")
	if len(got) != 1 || len(got[0]) != 2000 {
		t.Fatalf("2000-rune prompt: commits = %d entries", len(got))
	}
}

func TestPromptRecorderCollapsesWhitespace(t *testing.T) {
	t.Parallel()
	assertCommits(t, observeAll("a   b\t\tc\r"), "a b c")
}

// Auto-approval answers a choice prompt with a digit; when a reply lands
// without a committing Enter the next iteration types another digit and
// the buffer accumulates "11"/"111"/"11111". The promptMinRunes gate
// only catches a single "1", so these multi-digit runs used to slip into
// the strip. They must all be dropped as menu-choice noise.
func TestPromptRecorderDropsAccumulatedMenuDigits(t *testing.T) {
	t.Parallel()
	assertCommits(t, observeAll("11\r"))
	assertCommits(t, observeAll("111\r"))
	assertCommits(t, observeAll("11111\r"))
	assertCommits(t, observeAll("22\r"))
	// Accumulation across separate writes (digit, digit, digit+Enter)
	// with no Enter between them — the real-world shape of the bug.
	assertCommits(t, observeAll("1", "1", "1\r"))
}

func TestPromptRecorderDropsYesNoReplies(t *testing.T) {
	t.Parallel()
	assertCommits(t, observeAll("yes\r"))
	assertCommits(t, observeAll("no\r"))
	assertCommits(t, observeAll("YES\r"))
}

// Boundary: a line with any non-digit character is a real prompt and
// must still commit — including one that merely starts with a menu
// digit. (The rarer "2/usage"-style carry-over, where an uncommitted
// menu digit prepends to the next typed prompt, is a separate mechanism
// and intentionally left to commit rather than risk dropping real text.)
func TestPromptRecorderKeepsDigitLedPrompts(t *testing.T) {
	t.Parallel()
	assertCommits(t, observeAll("2 reasons this fails\r"), "2 reasons this fails")
	assertCommits(t, observeAll("2/usage\r"), "2/usage")
}

func TestSanitizePromptsClearsLegacyNoise(t *testing.T) {
	t.Parallel()
	got := sanitizePrompts([]string{"11", "fix the login bug", "11111", "no"})
	assertCommits(t, got, "fix the login bug")
	// All-noise history sanitizes to empty (nil), so omitempty drops it.
	if out := sanitizePrompts([]string{"11", "111"}); len(out) != 0 {
		t.Fatalf("all-noise history sanitized to %q, want empty", out)
	}
}

func TestSanitizePromptsRepairsLegacyNativeEnvelopes(t *testing.T) {
	t.Parallel()
	got := sanitizePrompts([]string{
		`{"id":"solu-1","method":"turn/start","params":{"threadId":"thread-1","input":[{"type":"text","text":"what model are you"}]}}`,
		`{"type":"user","message":{"role":"user","content":"fix the focus switch"}}`,
	})
	assertCommits(t, got, "what model are you", "fix the focus switch")
}

func TestPromptRecorderPushPromptNewestFirstCapped(t *testing.T) {
	t.Parallel()
	// Push well past the cap to prove the oldest entries fall off.
	total := lastPromptsMax + 5
	var h []string
	for i := 0; i < total; i++ {
		h = pushPrompt(h, fmt.Sprintf("p%d", i))
	}
	if len(h) != lastPromptsMax {
		t.Fatalf("history len = %d, want %d", len(h), lastPromptsMax)
	}
	// Newest first: the last pushed sits at index 0, and the oldest
	// retained entry is exactly lastPromptsMax back from the newest.
	if want := fmt.Sprintf("p%d", total-1); h[0] != want {
		t.Fatalf("history[0] = %q, want newest %q", h[0], want)
	}
	if want := fmt.Sprintf("p%d", total-lastPromptsMax); h[lastPromptsMax-1] != want {
		t.Fatalf("history[%d] = %q, want oldest retained %q", lastPromptsMax-1, h[lastPromptsMax-1], want)
	}
}

func TestPromptRecorderBufferCapDropsOversized(t *testing.T) {
	t.Parallel()
	var r promptRecorder
	// 5000 unsubmitted bytes cap to the 4000-byte tail; the eventual
	// Enter then presents a >2000-rune line that the commit gate
	// rejects — oversized buffers must not produce garbage commits.
	assertCommits(t, r.observe([]byte(strings.Repeat("a", 5000))))
	assertCommits(t, r.observe([]byte("tail\r")))
	// And the buffer reset after the rejected commit: a short real
	// prompt right after still works.
	assertCommits(t, r.observe([]byte("next prompt\r")), "next prompt")
}

func TestPromptRecorderMultilinePasteCommitsPerLine(t *testing.T) {
	t.Parallel()
	got := observeAll("\x1b[200~line one\nline two\x1b[201~\r")
	assertCommits(t, got, "line one", "line two")
}

// Menu input sits after the composer draft: Enter, Ctrl+U and Backspace touch
// only the menu segment, and leaving the menu discards it.
func TestPromptRecorderMenuSegment(t *testing.T) {
	var r promptRecorder
	r.observe([]byte("draft"))
	r.lastRecordedInputMs = 100
	r.enterMenu()
	r.lastRecordedInputMs = 200
	r.observe([]byte("3free text\x7f\x7f"))
	if n, at := r.draft(); n != len("draft") || at != 100 {
		t.Fatalf("draft() = %d,%d; want the pre-menu draft and its age", n, at)
	}
	r.observe([]byte("\x15\x7f\x7f"))
	if string(r.buf) != "draft" {
		t.Fatalf("Ctrl+U/Backspace in a menu reached the draft: %q", r.buf)
	}
	assertCommits(t, r.observe([]byte("typed answer\r")), "typed answer")
	if string(r.buf) != "draft" {
		t.Fatalf("Enter in a menu submitted the draft: %q", r.buf)
	}
	r.observe([]byte("1"))
	r.enterMenu() // a following menu keeps the original segment start
	r.leaveMenu()
	if n, at := r.draft(); string(r.buf) != "draft" || n != 5 || at != 100 {
		t.Fatalf("leaveMenu = %q,%d,%d; want the draft back", r.buf, n, at)
	}
	assertCommits(t, r.observe([]byte(" more\r")), "draft more")
}

func TestPromptRecorderMenuPasteKeepsDraft(t *testing.T) {
	var r promptRecorder
	r.observe([]byte(strings.Repeat("d", 10)))
	r.enterMenu()
	r.observe([]byte(strings.Repeat("m", promptBufCap)))
	r.observe([]byte(strings.Repeat("m", promptBufCap)))
	if n, _ := r.draft(); n != 10 || len(r.buf) != 10+promptBufCap {
		t.Fatalf("a long menu paste cost the draft: draft=%d buf=%d", n, len(r.buf))
	}
	r.leaveMenu()
	if string(r.buf) != strings.Repeat("d", 10) {
		t.Fatalf("leaving the menu = %q, want the draft", r.buf)
	}
}
