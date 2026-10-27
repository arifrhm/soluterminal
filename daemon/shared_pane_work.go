package main

// clampRunes truncates by runes, not bytes: these are display strings, and a
// byte slice can split a multi-byte character into mojibake.
func clampRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}
