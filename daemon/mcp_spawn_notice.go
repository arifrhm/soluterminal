package main

import "fmt"

// announcePaneSpawn tells the people watching that an agent opened a pane:
// a toast with the starter, the new pane and its name, which the web client
// also keeps in the Alerts "Recent" list so it is still there when they come
// back. Published only after the pane is persisted and attached to its
// workspace, so the notice never names a pane that failed to start.
func (a *apiServer) announcePaneSpawn(p *Pane, origin *PaneOrigin, title, workspace string, number int) {
	if p == nil {
		return
	}
	ev := coordinationEvent{Kind: "spawn", Pane: p.ID, Agent: p.Snapshot().Agent}
	if origin != nil {
		ev.By, ev.Lead, ev.Started = origin.ByPaneID, origin.LeadPaneID, origin.Source
	}
	coordLog.record(ev)
	if a.uiBus == nil {
		return
	}
	who := "An agent"
	if origin != nil && origin.ByPaneTitle != "" {
		who = origin.ByPaneTitle
	}
	pane := "a pane"
	if number > 0 {
		pane = fmt.Sprintf("pane #%d", number)
	}
	where := ""
	if workspace != "" {
		where = " in " + workspace
	}
	a.uiBus.publish(uiEvent{
		Type:      "pane.spawned",
		PaneID:    p.ID,
		PaneTitle: title,
		Workspace: workspace,
		Text:      fmt.Sprintf("%s started %s%s — %s", who, pane, where, title),
	})
}
