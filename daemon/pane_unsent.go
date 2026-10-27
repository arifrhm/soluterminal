package main

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"unicode/utf8"
)

type unsentPaneMessage struct {
	ID         string `json:"id"`
	Sender     string `json:"sender"`
	body       string
	claude     bool
	revision   uint64
	connection net.Conn
}

var errUnsentChanged = errors.New("input changed or cannot be verified; the pane text was preserved")

// Manual input supersedes a receipt without altering any terminal text. This
// also removes the chip after the user submits or edits the draft themselves.
// Caller holds mu; receipts are immutable, so sharing the pointer is safe.
func (p *Pane) currentUnsentMessageLocked() *unsentPaneMessage {
	if p.unsentMessage != nil && p.Status == PaneRunning && p.Kind != "native" && !p.respawning.Load() && p.unsentMessage.connection == p.agentConn && p.unsentMessage.revision == p.inputRevision.Load() {
		return p.unsentMessage
	}
	return nil
}

// Exact deletion requires the whole literal draft to be visible. A collapsed
// paste, wrapped/multiline text, trailing spaces or unknown chrome cannot prove
// equality. Leave those for the user to edit in the terminal.
func composerShowsExactLine(screen string, codex bool, body string) bool {
	if body == "" || strings.ContainsAny(body, "\r\n") || strings.TrimRight(body, " \t") != body || strings.Contains(body, "[Pasted") {
		return false
	}
	if _, known := composerDraft(screen, codex); !known {
		return false
	}
	lines := strings.Split(screen, "\n")
	start := -1
	for i, line := range lines {
		t := strings.TrimLeft(line, " \t")
		if (codex && codexComposerLineRE.MatchString(line)) || (!codex && strings.HasPrefix(t, "❯")) {
			start = i
		}
	}
	if start < 0 {
		return false
	}
	line := strings.TrimLeft(lines[start], " \t")
	_, size := utf8.DecodeRuneInString(line)
	line = strings.TrimRight(line[size:], " \r")
	// Current Claude draws its prompt separator with NBSP.
	if strings.HasPrefix(line, "\u00a0") {
		line = strings.TrimPrefix(line, "\u00a0")
	} else {
		line = strings.TrimPrefix(line, " ")
	}
	if line != body {
		return false
	}
	for _, rest := range lines[start+1:] {
		if (codex && codexFooterLineRE.MatchString(rest)) || (!codex && newPromptFooterRE.MatchString(rest)) {
			return true
		}
		if strings.Trim(strings.TrimSpace(rest), "─") != "" {
			return false
		}
	}
	return false
}

func (a *apiServer) actOnUnsentMessage(w http.ResponseWriter, r *http.Request) {
	p, ok := a.mgr.Get(r.PathValue("id"))
	if !ok {
		http.Error(w, "pane not found", http.StatusNotFound)
		return
	}
	var req struct {
		ID     string `json:"id"`
		Action string `json:"action"`
	}
	if !decodeJSON(w, r, &req, defaultJSONBodyBytes) {
		return
	}
	if req.ID == "" || (req.Action != "send" && req.Action != "clear") {
		http.Error(w, "id and send/clear action required", http.StatusBadRequest)
		return
	}
	if !a.promptInputGate(r)(p) {
		http.Error(w, "human_required", http.StatusForbidden)
		return
	}
	p.deliveryMu.Lock()
	defer p.deliveryMu.Unlock()
	msg := p.Snapshot().UnsentMessage
	if msg == nil || msg.ID != req.ID {
		http.Error(w, "unsent message changed", http.StatusConflict)
		return
	}
	keys := "\r"
	if req.Action == "clear" {
		keys = "\x01\x0b"
	} // start of line, kill to end
	revision := msg.revision + 1
	// The provider check can scan the process tree, so it runs before the
	// writer lock; the screen and receipt are rechecked under it.
	if agent, claude := paneAgentProvider(p); !agent || claude != msg.claude {
		http.Error(w, errUnsentChanged.Error(), http.StatusConflict)
		return
	}
	// Clear's control bytes are not a prompt; keep them out of prompt history.
	_, err := p.writeWithPrecondition([]byte(keys), req.Action == "send", false, func() error {
		snap := p.Snapshot()
		if snap.Status != PaneRunning || snap.Kind == "native" || snap.UnsentMessage != msg || p.inputRevision.Load() != msg.revision {
			return errUnsentChanged
		}
		screen := deliveryScreenText(p)
		if isGridChoicePromptText(screen, !msg.claude) {
			return errUnsentChanged
		}
		holds, known := composerHolds(screen, !msg.claude, msg.body)
		if !known || !holds {
			return errUnsentChanged
		}
		if req.Action == "clear" && !composerShowsExactLine(screen, !msg.claude, msg.body) {
			return errUnsentChanged
		}
		return nil
	})
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errUnsentChanged) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	holds, known := waitComposer(p, !msg.claude, msg.body, false, deliveryRetryAfter)
	// Resolved only when the input box is confirmed to have changed.
	resolved := req.Action
	if !known || holds {
		resolved += "_unresolved"
	}
	coordLog.record(coordinationEvent{Kind: "unsent", Pane: p.ID, By: msg.Sender, Outcome: resolved})
	outcome := string(deliveryUnconfirmed)
	if known {
		if holds {
			outcome = string(deliveryLeftInComposer)
		} else {
			outcome = string(deliverySubmitted)
			if req.Action == "clear" {
				outcome = "cleared"
			}
		}
	}
	p.mu.Lock()
	if p.unsentMessage == msg {
		if known && !holds {
			p.unsentMessage = nil
		} else {
			next := *msg
			next.revision = revision
			p.unsentMessage = &next
		}
	}
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"outcome": outcome})
}
