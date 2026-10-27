package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexAgentsBlockNoDrift(t *testing.T) {
	t.Parallel()
	block := codexAgentsManagedBlock()
	if !strings.Contains(block, mcpServerInstructions) {
		t.Fatal("managed block does not contain mcpServerInstructions verbatim")
	}
	if !strings.Contains(block, "Work directly in your own session") || !strings.Contains(block, "scripts/solu-review") {
		t.Fatal("managed block lacks direct-work and independent-review instructions")
	}
	if !strings.Contains(block, ".claude/worktrees/<name>") || !strings.Contains(block, "never in a temporary folder such as /tmp") || !strings.Contains(block, ".gitignore excludes that folder") {
		t.Fatal("managed block lacks the durable-worktree instruction")
	}
	for _, retired := range []string{"Codex-specific: if the user explicitly asks for a workflow task", "workflow-task approval follows the engine policy", "create_workflow_task"} {
		if strings.Contains(block, retired) {
			t.Fatalf("retired engine instruction remains: %s", retired)
		}
	}
	if !strings.Contains(block, "SOLU_CONTEXT_PACK") || !strings.Contains(block, "read its AGENTS.md") {
		t.Fatal("managed block lacks the context-pack instruction")
	}
	if strings.Contains(block, "When creating supervised tasks") {
		t.Fatal("managed block still steers Codex sessions into creating workflow tasks")
	}
}

func TestCodexAgentsApplyFresh(t *testing.T) {
	t.Parallel()
	got, changed, ok := codexAgentsApply("")
	want := codexAgentsManagedBlock() + "\n"
	if !ok || !changed || got != want {
		t.Fatalf("fresh apply = (%q, %v, %v), want (%q, true, true)", got, changed, ok, want)
	}
}

func TestCodexAgentsApplyAppend(t *testing.T) {
	t.Parallel()
	existing := "# My instructions\nKeep this byte-exact."
	got, changed, ok := codexAgentsApply(existing)
	if !ok || !changed {
		t.Fatalf("append changed=%v ok=%v", changed, ok)
	}
	if !strings.HasPrefix(got, existing) {
		t.Fatalf("user content was not preserved as an exact prefix:\n%s", got)
	}
	if got != existing+"\n\n"+codexAgentsManagedBlock()+"\n" {
		t.Fatalf("unexpected appended form:\n%s", got)
	}
}

func TestCodexAgentsApplyHealStale(t *testing.T) {
	t.Parallel()
	prefix := "# User prefix\r\n"
	suffix := "\nUser suffix without normalization\r\n"
	existing := prefix + codexAgentsBeginMarker + "\nstale policy\n" + codexAgentsEndMarker + suffix
	got, changed, ok := codexAgentsApply(existing)
	if !ok || !changed {
		t.Fatalf("heal changed=%v ok=%v", changed, ok)
	}
	if got != prefix+codexAgentsManagedBlock()+suffix {
		t.Fatalf("heal did not preserve outside bytes:\n%q", got)
	}
}

func TestCodexAgentsApplyIdempotent(t *testing.T) {
	t.Parallel()
	first, changed, ok := codexAgentsApply("user\n")
	if !ok || !changed {
		t.Fatalf("first apply changed=%v ok=%v", changed, ok)
	}
	second, changed, ok := codexAgentsApply(first)
	if !ok || changed || second != first {
		t.Fatalf("second apply changed=%v ok=%v equal=%v", changed, ok, second == first)
	}
}

func TestCodexAgentsApplyWeirdShapes(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"end before begin":  codexAgentsEndMarker + "\n" + codexAgentsBeginMarker,
		"begin without end": codexAgentsBeginMarker + "\nstale",
		"duplicate begin":   codexAgentsBeginMarker + "\n" + codexAgentsBeginMarker + "\n" + codexAgentsEndMarker,
	}
	for name, existing := range cases {
		t.Run(name, func(t *testing.T) {
			got, changed, ok := codexAgentsApply(existing)
			if ok || changed || got != existing {
				t.Fatalf("weird apply = (%q, %v, %v)", got, changed, ok)
			}
		})
	}
}

func TestSyncCodexAgentsMd(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "AGENTS.md")
	syncCodexAgentsMd(path)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != codexAgentsManagedBlock()+"\n" {
		t.Fatalf("created file mismatch:\n%s", got)
	}

	weird := codexAgentsEndMarker + "\nuser text\n" + codexAgentsBeginMarker
	if err := os.WriteFile(path, []byte(weird), 0644); err != nil {
		t.Fatal(err)
	}
	syncCodexAgentsMd(path)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != weird {
		t.Fatalf("unrecognized shape changed on disk: %q", after)
	}
}

func TestSyncCodexAgentsMdIdempotent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	syncCodexAgentsMd(path)
	oldTime := time.Unix(123456789, 0)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	syncCodexAgentsMd(path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(oldTime) {
		t.Fatalf("idempotent sync rewrote file: mtime=%v", info.ModTime())
	}
}

func TestSyncCodexAgentsMdReadErrorLeavesPathAlone(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	syncCodexAgentsMd(path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("read-error path was replaced")
	}
}
