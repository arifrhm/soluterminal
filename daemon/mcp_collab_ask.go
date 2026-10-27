package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ask_pane and wait_for_pane: the waiting half of agent collaboration. Before
// these, an agent that sent work to another pane had to guess how long to
// sleep (a blind 45 s wait in testing) and then dig the answer out of raw
// scrollback. ask_pane sends, waits for that pane's agent to finish its turn
// and returns only the reply; wait_for_pane does the waiting alone.

const (
	askPaneDefaultTimeout  = 240 * time.Second
	askPaneMaxTimeout      = 900 * time.Second
	waitPaneDefaultTimeout = 120 * time.Second
	collabPollInterval     = 500 * time.Millisecond
	collabSettleScreen     = 2 * time.Second
	collabSettlePrompt     = time.Second
	spawnReadyTimeout      = 45 * time.Second
)

// collabSleep is swapped by tests.
var collabSleep = time.Sleep

type collabResult struct {
	Pane          string  `json:"pane"`
	PaneID        string  `json:"pane_id"`
	Status        string  `json:"status"` // replied | needs_approval | still_working | exited | idle
	State         string  `json:"state"`
	Source        string  `json:"source,omitempty"`
	Reply         string  `json:"reply,omitempty"`
	Screen        string  `json:"screen,omitempty"`
	WaitedSeconds float64 `json:"waited_seconds"`
	Hint          string  `json:"hint,omitempty"`
}

func collabTimeout(args map[string]any, def time.Duration) time.Duration {
	if v, ok := args["timeout_seconds"].(float64); ok && v > 0 {
		d := time.Duration(v * float64(time.Second))
		if d > askPaneMaxTimeout {
			d = askPaneMaxTimeout
		}
		return d
	}
	return def
}

func (a *apiServer) collabPaneLabel(p *Pane) string {
	snap := p.Snapshot()
	return paneLabel(paneSlotsByID(a.ws)[snap.ID], snap.Title, paneSummaryAgent(p, snap))
}

func collabJSON(r collabResult) any {
	b, err := json.Marshal(r)
	if err != nil {
		return toolErr("marshal: " + err.Error())
	}
	return toolOK(string(b))
}

// waitForPaneTurn polls until the pane's agent has finished the turn started
// at or after since (or, with a zero since, until the pane settles).
// paneTurnBaseline is the pane's last turn as it stood before ask_pane sent
// its message. A reply only counts when its prompt is newer than this, so an
// earlier turn (or another session's transcript) is never taken as the answer.
type paneTurnBaseline struct {
	Since    time.Time // when the message was sent; zero = wait for any turn
	Path     string
	PromptAt time.Time
}

func (a *apiServer) paneTurnBaselineFor(p *Pane) paneTurnBaseline {
	r := a.paneLatestReply(p)
	b := paneTurnBaseline{Since: time.Now()}
	if r.Source == "transcript" {
		b.Path, b.PromptAt = r.Path, r.PromptAt
	}
	return b
}

// freshTurn reports whether a transcript reply belongs to the asked turn.
func (b paneTurnBaseline) freshTurn(r paneReply) bool {
	if b.Since.IsZero() {
		return true
	}
	if r.PromptAt.IsZero() || r.PromptAt.Before(b.Since.Add(-3*time.Second)) {
		return false
	}
	return b.Path == "" || r.Path != b.Path || r.PromptAt.After(b.PromptAt)
}

func (a *apiServer) waitForPaneTurn(p *Pane, base paneTurnBaseline, timeout time.Duration) collabResult {
	start := time.Now()
	res := collabResult{Pane: a.collabPaneLabel(p), PaneID: p.ID}
	var settledSince, promptSince time.Time
	sawWork := base.Since.IsZero()
	for {
		state := p.computeStateCached()
		res.State = string(state)
		res.WaitedSeconds = time.Since(start).Round(100 * time.Millisecond).Seconds()
		if p.Snapshot().Status == PaneExited || state == StateExited {
			res.Status = "exited"
			res.Screen = paneScreenText(p)
			return res
		}
		reply := a.paneLatestReply(p)
		res.Source = reply.Source
		switch state {
		case StateBusy, StateRecent:
			sawWork = true
			settledSince, promptSince = time.Time{}, time.Time{}
		case StateChoicePrompt:
			settledSince = time.Time{}
			if promptSince.IsZero() {
				promptSince = time.Now()
			}
			if time.Since(promptSince) >= collabSettlePrompt {
				res.Status = "needs_approval"
				res.Screen = paneScreenText(p)
				res.Hint = "The pane is waiting on an approval menu. Answer it with answer_prompt (or tell the user), then call wait_for_pane."
				return res
			}
		default:
			promptSince = time.Time{}
			if settledSince.IsZero() {
				settledSince = time.Now()
			}
		}
		if reply.Source == "transcript" {
			if base.freshTurn(reply) && reply.Complete && state != StateBusy && state != StateChoicePrompt {
				res.Status = "replied"
				res.Reply = reply.Reply
				return res
			}
		} else if sawWork && !settledSince.IsZero() && time.Since(settledSince) >= collabSettleScreen {
			res.Status = "replied"
			res.Reply = reply.Reply
			return res
		}
		if time.Since(start) >= timeout {
			res.Status = "still_working"
			res.Reply = reply.Reply
			res.Hint = "Not finished yet. Call wait_for_pane to keep waiting; the reply so far is included."
			return res
		}
		collabSleep(collabPollInterval)
	}
}

func (a *apiServer) mcpToolAskPane(args map[string]any, callerPane string) any {
	ref, _ := args["pane_id"].(string)
	text, _ := args["text"].(string)
	if strings.TrimSpace(text) == "" {
		return toolErr("text is required: the message to send to the other pane's agent")
	}
	paneID := a.resolvePaneRef(ref, callerPane)
	if paneID == "" {
		return toolErr("no pane matches " + ref + " (pass a pane id or #number from list_panes)")
	}
	if paneID == callerPane {
		return toolErr("that is your own pane; ask a different pane")
	}
	p, ok := a.mgr.Get(paneID)
	if !ok {
		return toolErr("no pane with id " + paneID)
	}
	switch state := p.computeStateCached(); state {
	case StateExited:
		return toolErr("pane " + paneID + " has exited")
	case StateChoicePrompt:
		return toolErr("pane " + paneID + " is waiting on an approval menu; answer it with answer_prompt first, or tell the user")
	case StateBusy:
		return toolErr("pane " + paneID + " is busy; call wait_for_pane first, then ask again")
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	base := a.paneTurnBaselineFor(p)
	sent := a.mcpToolSendInput(map[string]any{"pane_id": paneID, "text": text}, callerPane)
	if m, ok := sent.(map[string]any); ok && m["isError"] == true {
		return sent
	}
	return collabJSON(a.waitForPaneTurn(p, base, collabTimeout(args, askPaneDefaultTimeout)))
}

func (a *apiServer) mcpToolWaitForPane(args map[string]any, callerPane string) any {
	ref, _ := args["pane_id"].(string)
	paneID := a.resolvePaneRef(ref, callerPane)
	if paneID == "" {
		return toolErr("no pane matches " + ref + " (pass a pane id or #number from list_panes)")
	}
	if paneID == callerPane {
		return toolErr("that is your own pane")
	}
	p, ok := a.mgr.Get(paneID)
	if !ok {
		return toolErr("no pane with id " + paneID)
	}
	res := a.waitForPaneTurn(p, paneTurnBaseline{}, collabTimeout(args, waitPaneDefaultTimeout))
	if res.Status == "replied" {
		res.Status = "idle"
	}
	return collabJSON(res)
}

// paneAgentReady reports whether a freshly spawned agent shows its input box.
func paneAgentReady(p *Pane, agent string) bool {
	state := p.computeStateCached()
	if state == StateAwaitingInput {
		return true
	}
	if state == StateBusy || state == StateRecent || state == StateChoicePrompt || state == StateExited {
		return false
	}
	screen := paneVisibleScreen(p)
	low := strings.ToLower(screen)
	if strings.Contains(low, "loading") || strings.Contains(low, "starting") || strings.Contains(low, "booting") {
		return false
	}
	return agentInputBoxVisible(screen, agent)
}

// agentInputBoxVisible requires the agent's real input box, not just its
// glyph: a first-run "trust this folder?" menu also draws ❯/› on its focused
// option, and a prompt sent there lands in the menu instead of the composer.
func agentInputBoxVisible(screen, agent string) bool {
	switch agent {
	case agentCodex:
		return looksLikeCodexPrompt(screen)
	case agentClaude:
		if _, known := composerDraft(screen, false); known {
			return true
		}
		// Unrecognised status line: fall back to the input glyph, but never
		// while a menu (trust prompt, picker) is on screen.
		low := strings.ToLower(screen)
		menu := strings.Contains(low, "enter to confirm") || isGridChoicePromptText(screen, false)
		return !menu && (strings.Contains(screen, "❯") || strings.Contains(screen, "│ >"))
	}
	return strings.TrimSpace(screen) != ""
}

// waitAgentReady waits for a spawned agent to accept input. Returns how long
// it took and whether it got there.
func waitAgentReady(p *Pane, agent string, timeout time.Duration) (time.Duration, bool) {
	start := time.Now()
	var readySince time.Time
	for time.Since(start) < timeout {
		if paneWaitingForFolderTrust(p) {
			return time.Since(start), false
		}
		if p.Snapshot().Status == PaneExited {
			return time.Since(start), false
		}
		if paneAgentReady(p, agent) {
			if readySince.IsZero() {
				readySince = time.Now()
			}
			if time.Since(readySince) >= time.Second {
				return time.Since(start), true
			}
		} else {
			readySince = time.Time{}
		}
		collabSleep(collabPollInterval)
	}
	return time.Since(start), false
}

// spawnFollowUp runs after spawn_pane created an agent pane: it waits for the
// agent to be ready and, when asked, sends the first prompt.
func (a *apiServer) spawnFollowUp(p *Pane, cmd []string, args map[string]any) map[string]any {
	agent := agentFromCmd(cmd)
	prompt, _ := args["prompt"].(string)
	wait := agent != ""
	if v, ok := args["wait_ready"].(bool); ok {
		wait = v
	}
	out := map[string]any{}
	if !wait && strings.TrimSpace(prompt) == "" {
		return out
	}
	took, ready := waitAgentReady(p, agent, spawnReadyTimeout)
	out["ready"] = ready
	out["ready_after_seconds"] = took.Round(100 * time.Millisecond).Seconds()
	if !ready && paneWaitingForFolderTrust(p) {
		out["status"] = "needs_user"
		out["state"] = string(StateChoicePrompt)
		out["hint"] = folderTrustHint
		if strings.TrimSpace(prompt) != "" {
			out["prompt_sent"] = false
			out["hint"] = folderTrustHint + " The initial prompt was not sent. After trusting the folder, use ask_pane to send the original prompt."
		}
		return out
	}
	if strings.TrimSpace(prompt) == "" {
		return out
	}
	if !ready {
		out["prompt_sent"] = false
		out["hint"] = "The agent did not show its input box in time, so the prompt was not sent. Check it with get_pane_state, then use ask_pane."
		return out
	}
	if !strings.HasSuffix(prompt, "\n") {
		prompt += "\n"
	}
	sent := a.mcpToolSendInput(map[string]any{"pane_id": p.ID, "text": prompt})
	if m, ok := sent.(map[string]any); ok && m["isError"] == true {
		out["prompt_sent"] = false
		out["hint"] = fmt.Sprint(m["content"])
		return out
	}
	out["prompt_sent"] = true
	out["hint"] = "The prompt was sent. Call wait_for_pane to get the reply."
	return out
}
