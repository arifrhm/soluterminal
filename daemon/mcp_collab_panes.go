package main

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Pane numbering for agents. The UI labels panes #1, #2, … in each workspace
// by their order in that workspace's layout (depth-first, pane leaves only;
// web/src/lib/layout.ts collectPaneIds). list_panes reports the same number so
// "ask Codex in pane 2" means what the user sees, plus the workspace, the live
// state and which pane is the caller.

type paneSlot struct {
	Workspace string
	Number    int
}

// layoutPaneOrder appends the pane leaves of a layout in the UI's order.
func layoutPaneOrder(node LayoutNode, out *[]string) {
	if node == nil {
		return
	}
	switch typ, _ := node["type"].(string); typ {
	case "pane":
		if id, _ := node["paneId"].(string); id != "" {
			*out = append(*out, id)
		}
	case "split":
		if a, ok := layoutChild(node["a"]); ok {
			layoutPaneOrder(a, out)
		}
		if b, ok := layoutChild(node["b"]); ok {
			layoutPaneOrder(b, out)
		}
	}
}

// paneSlotsByID maps every pane in a visible named workspace to its workspace
// and #n. Panes listed in a workspace but missing from its layout are numbered
// after the layout's panes. When a pane appears in more than one workspace the
// alphabetically first workspace wins, so the answer is stable.
func paneSlotsByID(ws *WorkspaceStore) map[string]paneSlot {
	out := map[string]paneSlot{}
	if ws == nil {
		return out
	}
	list := ws.List()
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	for _, w := range list {
		if w == nil || w.Name == "" || w.Name == "_current" {
			continue
		}
		var order []string
		layoutPaneOrder(w.Layout, &order)
		seen := map[string]bool{}
		for _, id := range order {
			seen[id] = true
		}
		for _, id := range w.PaneIds {
			if !seen[id] {
				order = append(order, id)
				seen[id] = true
			}
		}
		for i, id := range order {
			if _, taken := out[id]; !taken {
				out[id] = paneSlot{Workspace: w.Name, Number: i + 1}
			}
		}
	}
	return out
}

// agentDisplayName is the name list_panes gives an agent pane whose title is
// still the shell or binary path ("/bin/bash", "claude").
func agentDisplayName(agent string) string {
	switch agent {
	case agentClaude:
		return "Claude Code"
	case agentCodex:
		return "Codex"
	case agentMuse:
		return "Muse"
	}
	return ""
}

func paneTitleIsGeneric(title string) bool {
	t := strings.TrimSpace(title)
	if t == "" {
		return true
	}
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(t, `\`, "/")))
	switch strings.TrimSuffix(base, ".exe") {
	case "bash", "zsh", "sh", "fish", "pwsh", "powershell", "cmd", "claude", "codex", "muse":
		return true
	}
	return false
}

// paneLabel is the short human label agents can repeat back to the user,
// e.g. "#2 Codex" or "#3 api-errors".
func paneLabel(slot paneSlot, title, agent string) string {
	name := strings.TrimSpace(title)
	if paneTitleIsGeneric(name) {
		if display := agentDisplayName(agent); display != "" {
			name = display
		}
	}
	if slot.Number > 0 {
		if name == "" {
			return "#" + strconv.Itoa(slot.Number)
		}
		return "#" + strconv.Itoa(slot.Number) + " " + name
	}
	return name
}

// resolvePaneRef accepts a pane id, an "@handle" (pane_identity.go), or a "#n" / "n" pane number in the
// caller's workspace (falling back to a number that is unique across
// workspaces). Returns "" when nothing matches.
func (a *apiServer) resolvePaneRef(ref, callerPane string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if _, ok := a.mgr.Get(ref); ok {
		return ref
	}
	if id := a.resolvePaneHandleRef(ref); id != "" {
		return id
	}
	num := strings.TrimPrefix(ref, "#")
	n := atoiSafe(num)
	if n <= 0 || n >= 1<<30 {
		return ""
	}
	slots := paneSlotsByID(a.ws)
	callerWS := ""
	if s, ok := slots[callerPane]; ok {
		callerWS = s.Workspace
	}
	var matches []string
	for id, s := range slots {
		if s.Number != n {
			continue
		}
		if callerWS != "" && s.Workspace == callerWS {
			return id
		}
		matches = append(matches, id)
	}
	if callerWS == "" && len(matches) == 1 {
		return matches[0]
	}
	return ""
}
