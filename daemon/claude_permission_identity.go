package main

import (
	"regexp"
	"strings"
)

var (
	claudePermissionHeaderRE   = regexp.MustCompile(`(?m)^[\t ]*(Bash command|Edit file|Create file|Write file|Read file)(?:[\t ]+[^\n]*)?$`)
	claudePermissionQueueRE    = regexp.MustCompile(`\b([0-9]+)[\t ]+of[\t ]+[0-9]+[\t ]*$`)
	claudePermissionQueueRowRE = regexp.MustCompile(`^[\t ]*([0-9]+)[\t ]+of[\t ]+[0-9]+[\t ]*$`)
)

// Claude's queued tool requests can replace one another without an absent
// frame or a new focused option in the byte stream. Bind the nearest rendered
// permission body, rather than only its identical Yes/No options. The queue
// ordinal distinguishes identical queued commands; the queue total can grow
// while THIS request waits, so it is deliberately excluded.
//
// This only adds identity. The approval classifier still decides whether a
// menu is a permission and whether its focused option may be answered.
func claudePermissionIdentity(s string) string {
	headers := claudePermissionHeaderRE.FindAllStringSubmatchIndex(s, -1)
	if len(headers) == 0 {
		return claudeClippedCommandIdentity(s)
	}
	header := headers[len(headers)-1]
	body := s[header[1]:]
	question := strings.LastIndex(body, "Do you want to ")
	if question < 0 {
		return ""
	}
	var kept []string
	kept = append(kept, s[header[2]:header[3]])
	if queue := claudePermissionQueueRE.FindStringSubmatch(s[header[0]:header[1]]); queue != nil {
		kept = append(kept, "request:"+queue[1])
	} else {
		// A narrow terminal can put the queue position on its own next row.
		// Never treat numbers later in the command as queue chrome.
		line, rest, _ := strings.Cut(strings.TrimLeft(body, "\n"), "\n")
		if queue := claudePermissionQueueRowRE.FindStringSubmatch(line); queue != nil {
			kept = append(kept, "request:"+queue[1])
			body = rest
			question = strings.LastIndex(body, "Do you want to ")
		}
	}
	skipTip := false
	for _, raw := range strings.Split(body[:question], "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "Tip:") {
			skipTip = true
			continue
		}
		if line != "" && strings.Trim(line, "╌─━- ") == "" {
			skipTip = false
			continue
		}
		if skipTip || line == "" {
			continue
		}
		// Claude boxes each physically wrapped command row with a margin.
		line = strings.TrimSpace(strings.TrimPrefix(line, "│"))
		kept = append(kept, line)
	}
	kept = append(kept, body[question:])
	return "claude:" + normalizeMenuLine(strings.Join(kept, " "))
}

// A long Bash request can scroll its header and queue ordinal offscreen.
// Bind only the nearest contiguous boxed command rows and closing delimiter;
// preceding conversation/spinners must not rearm the menu. Classification
// still decides whether this menu may receive an approval.
func claudeClippedCommandIdentity(s string) string {
	question := strings.LastIndex(s, "Do you want to ")
	if question < 0 {
		return ""
	}
	lines := strings.Split(s[:question], "\n")
	var kept []string
	closedBox := false
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !closedBox {
			// Approval explanations (including boxed warnings) sit below the
			// command's dotted closing delimiter. They are not command identity.
			if line != "" && strings.Trim(line, "╌") == "" {
				closedBox = true
			}
			continue
		}
		if strings.HasPrefix(line, "│") {
			kept = append(kept, strings.TrimSpace(strings.TrimPrefix(line, "│")))
			continue
		}
		if len(kept) > 0 {
			break
		}
		if line != "" {
			return ""
		}
	}
	if len(kept) == 0 {
		return ""
	}
	for left, right := 0, len(kept)-1; left < right; left, right = left+1, right-1 {
		kept[left], kept[right] = kept[right], kept[left]
	}
	kept = append(kept, s[question:])
	return "claude-clipped:" + normalizeMenuLine(strings.Join(kept, " "))
}
