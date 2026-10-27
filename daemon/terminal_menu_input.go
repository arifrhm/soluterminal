package main

import (
	"regexp"
	"strings"
)

// terminalPermissionShortcut distinguishes a literal menu key from composer
// text. Codex can consume y/1 immediately, without a following Enter. Recording
// that key as a draft then holds the NEXT approval for two minutes.
// Only consult the screen for single keys; ordinary typing and paste keep the
// existing recorder path. Never clear a draft that predates the menu.
func terminalPermissionShortcut(p *Pane, b []byte) bool {
	if p == nil || p.buffer == nil || len(b) != 1 || p.Snapshot().Kind == "native" {
		return false
	}
	key := b[0]
	if !(key >= '1' && key <= '9') && key != 'y' && key != 'Y' && key != 'n' && key != 'N' && key != 'p' {
		return false
	}
	if p.computeState() != StateChoicePrompt {
		return false
	}
	// A human may answer a permission that automation deliberately declines
	// (for example a focused session-mode escalation). Recognition of that
	// consumed key must not depend on permission to answer it autonomously.
	tail, permission := currentAutoApprovalPromptText(p)
	if tail == "" {
		// Raw terminal menus can be denied without a grid snapshot. State was
		// checked above; use the same bounded scan that admitted that menu.
		tail = approvalScanTail(p)
	}
	choices := choicePromptMatches(tail)
	if len(choices) == 0 {
		return false
	}
	menu := parseAutoApprovalMenu(tail, choices[len(choices)-1][0])
	if len(menu.options) < 2 || !menuHasEscapeOption(menu) {
		return false
	}
	// Keep free-form and user-question input in the recorder. Explicit Yes/No
	// confirmation shortcuts are consumed immediately by terminal dialogs.
	if !permission && !isAcceptOnceFocusedRow(menu.options[0]) && !strings.HasPrefix(menu.options[0], "yes, and switch to ") {
		return false
	}
	if autoApprovalDenyReason("", menu.menu) == "question menu" {
		return false
	}
	if key >= '1' && key <= '9' {
		// options belongs only to the latest consecutive menu, whereas
		// parseMenuOptions also sees numbers from stale menus and code output.
		return int(key-'1') < len(menu.options)
	}
	// Letter shortcuts must actually be advertised by the permission menu.
	label := strings.ReplaceAll(normalizePromptText(menu.menu), " ", "")
	return strings.Contains(label, "("+strings.ToLower(string(b))+")")
}

// menuInput classifies where a keystroke lands, for the prompt recorder.
type menuInput uint8

const (
	// menuInputNone: no menu on screen; the keystroke is composer text.
	menuInputNone menuInput = iota
	// menuInputDrawn: a menu is drawn but computeState already counts it as
	// answered, because input followed it. Free text typed into a question,
	// or the Enter after an arrow key, lands here.
	menuInputDrawn
	// menuInputLive: an unanswered menu is on screen.
	menuInputLive
)

// menuDialogFooterRE matches the key-hint footer Claude and Codex draw under
// an open menu ("Enter to select · ↑/↓ to navigate · Esc to cancel", "Press
// enter to confirm or esc to cancel"). It stays while the highlight moves or a
// question's free text is typed, when the menu no longer has the focused "1."
// row choicePromptRE needs; composer text such as "❯ 1. fix" never has it.
var menuDialogFooterRE = regexp.MustCompile(`(?i)\besc(?:ape)? to cancel\b|\benter to (?:select|confirm)\b`)

// menuDialogFooterLines bounds the footer search to the bottom of the screen,
// where the hint sits; wrapped hints span several short lines.
const menuDialogFooterLines = 8

// menuInputState says whether a keystroke answers a menu. Entering a menu
// needs computeState's live choice prompt, which already rejects composer text
// that merely looks like a list ("❯ 1. fix the bug"). Staying in it needs the
// menu's dialog footer still on screen, since any input makes computeState
// count the menu answered.
func (p *Pane) menuInputState() menuInput {
	if p == nil || p.buffer == nil {
		return menuInputNone
	}
	if p.computeState() == StateChoicePrompt {
		return menuInputLive
	}
	text, ok := p.screenVisibleText()
	if !ok {
		text, ok = p.gridVisibleText()
	}
	if ok && menuDialogFooterOnScreen(text) {
		return menuInputDrawn
	}
	return menuInputNone
}

func menuDialogFooterOnScreen(screen string) bool {
	var tail []string
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0 && len(tail) < menuDialogFooterLines; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			tail = append(tail, line)
		}
	}
	// Reverse to screen order, then flatten so a wrapped hint reads as one.
	for i, j := 0, len(tail)-1; i < j; i, j = i+1, j-1 {
		tail[i], tail[j] = tail[j], tail[i]
	}
	return menuDialogFooterRE.MatchString(strings.Join(strings.Fields(strings.Join(tail, " ")), " "))
}
