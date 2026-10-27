package main

import (
	"reflect"
	"time"
)

// Bindings map exact managed pane IDs to exact task IDs, including history.
// No title or prefix heuristic confers ownership.
func (s *WorkspaceStore) SetOwnerBindings(supplier func() map[string]string) {
	s.bindingMu.Lock()
	s.ownerBindings = supplier
	s.bindingMu.Unlock()
}
func (s *WorkspaceStore) managedOwnerBindings() map[string]string {
	s.bindingMu.RLock()
	supplier := s.ownerBindings
	s.bindingMu.RUnlock()
	if supplier == nil {
		return nil
	}
	return supplier()
}
func detachManagedOwnerLayout(node LayoutNode, bindings map[string]string) LayoutNode {
	if node == nil {
		return nil
	}
	typ, _ := node["type"].(string)
	if typ == "pane" {
		id, _ := node["paneId"].(string)
		if _, ok := bindings[id]; ok {
			return nil
		}
		return node
	}
	if typ == "tasks" {
		id, _ := node["tasksId"].(string)
		task, _ := node["ownerTaskId"].(string)
		if bound, ok := bindings[id]; ok && bound == task {
			return nil
		}
		return node
	}
	if typ != "split" {
		return node
	}
	a, _ := layoutChild(node["a"])
	b, _ := layoutChild(node["b"])
	na, nb := detachManagedOwnerLayout(a, bindings), detachManagedOwnerLayout(b, bindings)
	if na == nil {
		return nb
	}
	if nb == nil {
		return na
	}
	out := cloneLayoutNode(node)
	out["a"] = na
	out["b"] = nb
	return out
}
func sanitizeManagedOwnerWorkspace(w *Workspace, bindings map[string]string) *Workspace {
	if w == nil || len(bindings) == 0 {
		return w
	}
	w.Layout = detachManagedOwnerLayout(w.Layout, bindings)
	for id := range bindings {
		w.PaneIds = removePaneID(w.PaneIds, id)
		w.Panes = removePaneDef(w.Panes, id)
	}
	return w
}
func cloneWorkspaceRecentAdds(input map[string]map[string]time.Time) map[string]map[string]time.Time {
	out := map[string]map[string]time.Time{}
	for name, entries := range input {
		copy := map[string]time.Time{}
		for id, at := range entries {
			copy[id] = at
		}
		out[name] = copy
	}
	return out
}
func pruneManagedOwnerAdds(adds map[string]map[string]time.Time, bindings map[string]string) {
	for name, entries := range adds {
		for id := range bindings {
			delete(entries, id)
		}
		if len(entries) == 0 {
			delete(adds, name)
		}
	}
}

// Migration changes presentation only and preserves ActiveAt. A failed commit
// leaves ALL workspaces and bookkeeping untouched. Repeated calls are no-ops.
func (s *WorkspaceStore) DetachManagedOwnerPresentation() error {
	bindings := s.managedOwnerBindings()
	if len(bindings) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	candidate := s.candidate()
	changed := false
	for name, original := range s.data {
		next := sanitizeManagedOwnerWorkspace(cloneWorkspace(original), bindings)
		if !reflect.DeepEqual(next, original) {
			candidate[name] = next
			changed = true
		}
	}
	adds := cloneWorkspaceRecentAdds(s.recentAdds)
	pruneManagedOwnerAdds(adds, bindings)
	if changed {
		if err := s.commit(candidate); err != nil {
			return err
		}
	}
	s.recentAdds = adds
	return nil
}
