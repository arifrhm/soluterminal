package main

// Terminal emulators talk back. Besides real keystrokes, an attached
// xterm.js client autonomously writes protocol traffic into the PTY: focus
// in/out reports (Claude Code enables mode 1004), cursor-position reports
// answering DSR queries, device-attribute and mode replies, OSC
// color/clipboard responses, and mouse-wheel reports. None of that is the
// USER typing — but pane.write() advanced LastInputStrippedPos/LastInputMs
// for every byte, so merely opening a workspace (focus + query replies into
// each visible pane) counted as "the user typed since the prompt appeared".
// That re-armed the awaiting-input classifier's answered-gate and re-fired
// "Claude is waiting for you" alerts indefinitely (observed 2026-07-10:
// seven pushes in 2¼h for one idle pane; visible panes firing in lockstep
// ~15s after each app open). It also did the inverse: chatter marked a real
// pending approval menu as "answered", silently retracting a valid alert.
//
// isTerminalReplyOnly reports whether a client→PTY payload consists ENTIRELY
// of such autonomous protocol sequences, i.e. carries no evidence of a human
// typing. Bias on ambiguity: unrecognized bytes count as REAL input (return
// false). Overcounting chatter as typing merely re-notifies once — the
// pre-fix status quo; undercounting real typing would break answered-prompt
// suppression, which is worse.
func isTerminalReplyOnly(b []byte) bool {
	i := 0
	for i < len(b) {
		if b[i] != 0x1b {
			return false
		}
		n := consumeReplySeq(b[i:])
		if n == 0 {
			return false
		}
		i += n
	}
	return true
}

// consumeReplySeq returns the length of one recognized autonomous terminal
// sequence at the start of b (b[0] is ESC), or 0 when the bytes could be —
// or definitely are — user input (Escape key, Alt+key, arrow keys, a
// truncated sequence we can't identify, …).
func consumeReplySeq(b []byte) int {
	if len(b) < 2 {
		return 0 // bare ESC: the Escape KEY, not a reply
	}
	switch b[1] {
	case '[':
		return consumeReplyCSI(b)
	case ']', 'P', '_', '^', 'X':
		// OSC / DCS / APC / PM / SOS strings. Client→PTY strings are query
		// responses (colors, clipboard, termcap) — never keystrokes.
		return consumeStringSeq(b)
	}
	return 0
}

// consumeStringSeq consumes an OSC/DCS/APC/PM/SOS string through its BEL or
// ST (ESC \) terminator. A payload that ends mid-string is consumed to the
// end: a response split across write frames is still not typing.
func consumeStringSeq(b []byte) int {
	for i := 2; i < len(b); i++ {
		switch b[i] {
		case 0x07: // BEL
			return i + 1
		case 0x1b:
			if i+1 < len(b) && b[i+1] == '\\' { // ST
				return i + 2
			}
		}
	}
	return len(b)
}

// consumeReplyCSI matches the CSI-form replies a terminal emits on its own.
// Finals that encode KEYS (A–D arrows, H/F home/end, Z shift-tab, ~ nav
// keys, plain-u kitty key events, …) deliberately fall through to 0.
func consumeReplyCSI(b []byte) int {
	i := 2
	var prefix byte
	if i < len(b) && (b[i] == '?' || b[i] == '>' || b[i] == '<' || b[i] == '=') {
		prefix = b[i]
		i++
	}
	paramStart := i
	hasDigits := false
	for i < len(b) && (b[i] >= '0' && b[i] <= '9' || b[i] == ';' || b[i] == ':') {
		if b[i] != ';' && b[i] != ':' {
			hasDigits = true
		}
		i++
	}
	params := string(b[paramStart:i])
	var intermediate byte
	if i < len(b) && b[i] >= 0x20 && b[i] <= 0x2f {
		intermediate = b[i]
		i++
	}
	if i >= len(b) {
		return 0 // truncated CSI — can't identify, count as real input
	}
	final := b[i]
	length := i + 1

	switch final {
	case 'I', 'O': // focus in / focus out (mode 1004) — bare form only
		if prefix == 0 && params == "" && intermediate == 0 {
			return length
		}
	case 'R': // CPR "ESC[r;cR" / DECXCPR "ESC[?r;c;pR" cursor reports.
		// Known collision: F3-with-modifier also encodes CSI 1;mR. CPR is
		// overwhelmingly more common on this path; accept the ambiguity.
		if hasDigits && intermediate == 0 && (prefix == 0 || prefix == '?') {
			return length
		}
	case 'c': // DA1 "ESC[?…c" / DA2 "ESC[>…c" device-attribute replies
		if hasDigits && intermediate == 0 && (prefix == '?' || prefix == '>') {
			return length
		}
	case 'n': // DSR status reply "ESC[0n" / "ESC[3n"
		if hasDigits && prefix == 0 && intermediate == 0 {
			return length
		}
	case 'y': // DECRPM mode report "ESC[?Pm;Ps$y"
		if intermediate == '$' {
			return length
		}
	case 'u': // kitty keyboard flags reply "ESC[?Nu" — plain "ESC[Nu" is a KEY
		if prefix == '?' {
			return length
		}
	case 't': // XTWINOPS size report "ESC[8;rows;colst" etc.
		if hasDigits && prefix == 0 && intermediate == 0 {
			return length
		}
	case 'M', 'm':
		if prefix == '<' { // SGR mouse report (wheel/press/release)
			return length
		}
		if final == 'M' && prefix == 0 && params == "" && intermediate == 0 {
			// Legacy X10 mouse report: CSI M + 3 raw encoding bytes.
			if len(b) >= length+3 {
				return length + 3
			}
			return 0
		}
	case '~': // bracketed-paste guards; pasted CONTENT between them is input
		if params == "200" || params == "201" {
			return length
		}
	}
	return 0
}
