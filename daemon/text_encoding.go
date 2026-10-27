package main

import (
	"strings"
	"unicode/utf8"
)

// validUTF8 makes a string safe to hand to a runtime CLI. Runtime CLIs
// (observed: claude) treat invalid UTF-8 on their prompt input as a fatal
// error — "input is not valid UTF-8 (invalid byte at offset N)" — so one stray
// byte from captured process output kills the invocation and can fail the
// task. Every byte sequence written to a runtime input channel (stdin, prompt
// file) passes through here, as does captured output at the point it is
// embedded in a prompt. Valid input is returned unchanged and without
// allocation, so this never perturbs prompts that were already clean.
// Same idiom as sanitizeActiveLabel in cloud/team_creds.go.
func validUTF8(s string) string {
	return strings.ToValidUTF8(s, "�")
}

// runeSafeTailBytes keeps at most limit bytes from the end of buf without
// beginning inside a multi-byte rune. Process output can still contain invalid
// bytes; those remain verbatim for persistence and are replaced only when the
// text enters a worker prompt.
func runeSafeTailBytes(buf []byte, limit int) []byte {
	if limit <= 0 {
		return buf[:0]
	}
	if len(buf) <= limit {
		return buf
	}
	start := len(buf) - limit
	for start < len(buf) && !utf8.RuneStart(buf[start]) {
		start++
	}
	return buf[start:]
}
