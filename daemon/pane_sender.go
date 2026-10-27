package main

import (
	"fmt"
	"strings"
)

// Only the daemon-observed caller identifies the sender. Never accept a label
// supplied in tool arguments, and never label raw keys or short menu answers.
func (a *apiServer) labelPaneMessage(body, caller, target string) string {
	// Slash commands, shell escapes and memory shortcuts need their prefix
	// at column zero. They are control input, rather than attributed chat.
	if strings.HasPrefix(body, "/") || strings.HasPrefix(body, "!") || strings.HasPrefix(body, "#") {
		return body
	}
	// Attribution is informational, never an authorization signal. Labels
	// quoted inside the body are ordinary untrusted message content.
	if sender := a.paneMessageSender(caller, target); sender != "" {
		return "[from " + sender + "] " + body
	}
	return body
}

func (a *apiServer) paneMessageSender(caller, target string) string {
	if caller == "" || caller == target {
		return ""
	}
	p, ok := a.mgr.Get(caller)
	if !ok {
		return ""
	}
	snap := p.Snapshot()
	agent := paneSummaryAgent(p, snap)
	name := agentDisplayName(agent)
	if name == "" {
		return ""
	}
	if agent == agentClaude {
		name = "Claude"
	}
	identity := caller
	if slot := paneSlotsByID(a.ws)[caller]; slot.Number > 0 {
		identity = fmt.Sprintf("#%d", slot.Number)
	}
	// The handle comes first: it is how the receiver should address a reply.
	if h := identityHandle(snap.Identity); h != "" {
		return "@" + h + " · " + identity + " " + name
	}
	return identity + " " + name
}
