package main

import "unicode/utf8"

// truncateRunes cuts s so that the result (including suffix) is at most max
// bytes, never splitting a multibyte rune. Assumes len(suffix) < max.
func truncateRunes(s string, max int, suffix string) string {
	if len(s) <= max {
		return s
	}
	cut := max - len(suffix)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + suffix
}
