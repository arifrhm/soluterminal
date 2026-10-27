package main

import (
	"strconv"
	"strings"
)

// isObviousMenuReply matches inputs that are unambiguously a Claude
// Code menu choice the user wanted to submit: a single 1-9 digit, a
// single y/n letter, or the words yes/no (case-insensitive). Any other
// input is left alone — multi-character free text has too many
// legitimate "type but don't submit" use cases to auto-Enter.
func isObviousMenuReply(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" || t != s {
		return false // ignore inputs with surrounding whitespace
	}
	switch strings.ToLower(t) {
	case "y", "n", "yes", "no":
		return true
	}
	if len(t) == 1 && t[0] >= '1' && t[0] <= '9' {
		return true
	}
	return false
}

// splitTrailingEnter separates a send_input argument into "body" and
// a "submit" flag. If the input has exactly one trailing line
// terminator (\r\n, \n, or \r) and no other newlines, we return the
// body without that terminator and submit=true — the caller then
// writes body and \r as two separate PTY frames with a timing gap so
// TUI paste-burst detectors don't swallow the submit. Multi-line
// input (embedded newlines) passes through verbatim with submit=false
// so its line semantics are preserved.
func splitTrailingEnter(s string) (body string, submit bool) {
	trim := ""
	switch {
	case strings.HasSuffix(s, "\r\n"):
		trim = "\r\n"
	case strings.HasSuffix(s, "\n"):
		trim = "\n"
	case strings.HasSuffix(s, "\r"):
		trim = "\r"
	}
	if trim == "" {
		return s, false
	}
	head := s[:len(s)-len(trim)]
	if strings.ContainsAny(head, "\r\n") {
		return s, false
	}
	return head, true
}

// agentMessageBody reports whether send_input text is a message for an agent's
// input box — it ends in Enter and carries more than a one- or two-key answer —
// and returns it without the trailing line terminators. Unlike
// splitTrailingEnter, embedded newlines stay part of the message: an agent
// composer takes them as one pasted message, so they must not cancel the
// separate submit (the bug that left multi-line messages unsent).
func agentMessageBody(s string) (string, bool) {
	if !strings.HasSuffix(s, "\n") && !strings.HasSuffix(s, "\r") {
		return "", false
	}
	body := strings.TrimRight(s, "\r\n")
	if len([]rune(strings.TrimSpace(body))) <= 2 {
		return "", false
	}
	// Control sequences (arrow keys, Ctrl chords) are keystrokes, not a
	// message: they keep raw semantics.
	for _, r := range body {
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			return "", false
		}
	}
	// A carriage return inside the body could submit early; keep line breaks
	// as newlines within the one pasted message.
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	return body, true
}

// shouldSubmitFocusedChoiceWithEnter reports whether an autonomous
// menu reply can be reduced from "1" + Enter to a bare Enter. Choice
// prompt detection only treats focused option-1 rows as actionable,
// so Enter confirms the same selection without leaving a stray "1"
// behind if the prompt redraws during submit timing.
func shouldSubmitFocusedChoiceWithEnter(p *Pane, input string) bool {
	body, submit := splitTrailingEnter(input)
	if !submit || body != "1" {
		return false
	}
	return p != nil && p.computeState() == StateChoicePrompt
}

// unescapeAriaInput turns C-style escape sequences in a send_input
// argument into their real bytes. Supports \n \r \t \\ \" \0 \xNN
// \uNNNN — the ones the model actually uses. Unknown escapes
// pass through untouched, so legitimate backslashes in text (e.g.
// Windows paths) survive when they aren't followed by a recognized
// escape letter.
func unescapeAriaInput(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			i++
			continue
		}
		c := s[i+1]
		switch c {
		case 'n':
			b.WriteByte('\n')
			i += 2
		case 'r':
			b.WriteByte('\r')
			i += 2
		case 't':
			b.WriteByte('\t')
			i += 2
		case '\\':
			b.WriteByte('\\')
			i += 2
		case '"':
			b.WriteByte('"')
			i += 2
		case '0':
			b.WriteByte(0)
			i += 2
		case 'x':
			if i+3 < len(s) {
				if v, err := strconv.ParseUint(s[i+2:i+4], 16, 8); err == nil {
					b.WriteByte(byte(v))
					i += 4
					continue
				}
			}
			b.WriteByte(s[i])
			i++
		case 'u':
			if i+5 < len(s) {
				if v, err := strconv.ParseUint(s[i+2:i+6], 16, 32); err == nil {
					b.WriteRune(rune(v))
					i += 6
					continue
				}
			}
			b.WriteByte(s[i])
			i++
		default:
			// Unknown escape — leave the backslash in place so paths
			// and regex-ish text don't get mangled.
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}
