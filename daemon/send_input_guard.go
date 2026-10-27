package main

import (
	"strings"
	"unicode/utf8"
)

// sendInputMenuAnswerMaxRunes: a deliberate menu answer ("1", "y", "2\n")
// is one or two keys. Anything longer typed into a pane that is showing an
// approval menu is read by the TUI as menu keystrokes — a message such as
// "[from Codex] please …" can select options, including Codex's "p" (don't
// ask again, a persistent grant) — and it also leaves "composer has text"
// behind, holding the pane's auto-approval.
const sendInputMenuAnswerMaxRunes = 2

// sendInputMenuCollision refuses MCP send_input text that is longer than a
// menu answer while the target pane sits at an approval/choice prompt. The
// caller can retry once the prompt resolves, or answer it explicitly with
// answer_prompt. Nothing is written when it refuses.
func sendInputMenuCollision(p *Pane, text string) string {
	if p == nil || p.Snapshot().Kind == "native" {
		return ""
	}
	if !sendInputLongerThanMenuAnswer(text) {
		return "" // a deliberate one/two-key answer: no state probe needed
	}
	return sendInputMenuCollisionFor(p.ID, p.computeStateCached(), text)
}

func sendInputLongerThanMenuAnswer(text string) bool {
	return utf8.RuneCountInString(strings.TrimRight(text, "\r\n")) > sendInputMenuAnswerMaxRunes
}

// sendInputMenuCollisionFor is the decision on an already-computed pane state.
func sendInputMenuCollisionFor(paneID string, state PaneRuntimeState, text string) string {
	if state != StateChoicePrompt || !sendInputLongerThanMenuAnswer(text) {
		return ""
	}
	return "pane " + paneID + " is showing an approval prompt — your text was NOT sent, because the menu would read it as keystrokes (it can select options, including persistent grants). " +
		"Retry after the prompt resolves, or answer the prompt itself with answer_prompt."
}
