package main

import (
	"path/filepath"
	"testing"
)

// Workflow task worktrees live under <dataDir>/workflow/worktrees; diffs and file
// previews of task branches must keep working while the lane credentials under
// workflow/creds stay refused.
func TestCredentialPathAllowsWorkflowWorktrees(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	data := filepath.Join(home, ".solu")
	readable := []string{
		filepath.Join(data, "workflow", "worktrees", "wft_1234", "daemon", "main.go"),
		filepath.Join(data, "workflow", "tasks", "wft_1234", "task.json"),
		filepath.Join(data, "workflow", "account_health.json"),
	}
	for _, path := range readable {
		if reason := credentialPathRefusal(path, data); reason != "" {
			t.Errorf("%s blocked: %s", path, reason)
		}
	}
	refused := []string{
		filepath.Join(data, "workflow", "creds", "claude", "lane-1", ".credentials.json"),
		filepath.Join(data, "workflow", "Creds", "claude", "lane-1", ".credentials.json"),
	}
	for _, path := range refused {
		if reason := credentialPathRefusal(path, data); reason == "" {
			t.Errorf("%s not refused", path)
		}
	}
}
