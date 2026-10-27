package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListClaudeSessionsInteractiveFirstPromptAndTitle(t *testing.T) {
	cfg := t.TempDir()
	cwd := "/work/repo"
	proj := filepath.Join(cfg, "projects", claudeProjectDirName(cwd))
	writeLines(t, filepath.Join(proj, "aaaaaaaa-0000-4000-8000-000000000001.jsonl"),
		`{"type":"user","entrypoint":"cli","cwd":"/work/repo","message":{"role":"user","content":"<command-name>/clear</command-name>"}}`,
		`{"type":"user","entrypoint":"cli","message":{"role":"user","content":"fix the   flaky login\ntest please"}}`,
		`{"type":"ai-title","aiTitle":"Fix flaky login test"}`,
	)
	writeLines(t, filepath.Join(proj, "bbbbbbbb-0000-4000-8000-000000000002.jsonl"),
		`{"type":"user","entrypoint":"sdk-cli","cwd":"/work/repo","message":{"role":"user","content":"workflow stage prompt"}}`,
	)
	writeLines(t, filepath.Join(proj, "cccccccc-0000-4000-8000-000000000003.jsonl"),
		`{"type":"summary","entrypoint":"cli"}`,
	)
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(proj, "aaaaaaaa-0000-4000-8000-000000000001.jsonl"), old, old)

	got := listClaudeSessions([]string{cfg}, cwd, 10, false, nil)
	if len(got) != 1 {
		t.Fatalf("want only the interactive session with a prompt, got %+v", got)
	}
	s := got[0]
	if s.ID != "aaaaaaaa-0000-4000-8000-000000000001" || s.Title != "Fix flaky login test" || s.FirstPrompt != "fix the flaky login test please" || s.Automated {
		t.Fatalf("unexpected summary: %+v", s)
	}
	all := listClaudeSessions([]string{cfg}, cwd, 10, true, nil)
	if len(all) != 2 || all[0].ID != "bbbbbbbb-0000-4000-8000-000000000002" || !all[0].Automated {
		t.Fatalf("automated=1 should include sdk runs newest-first, got %+v", all)
	}
}

func TestListCodexSessionsMatchesCwdAndSkipsInjectedItems(t *testing.T) {
	root := t.TempDir()
	cwd := "/work/repo"
	day := filepath.Join(root, "2026", "10", "02")
	writeLines(t, filepath.Join(day, "rollout-2026-10-02T10-00-00-01a00000-0000-7000-8000-0000000000e1.jsonl"),
		`{"type":"session_meta","payload":{"id":"01a00000-0000-7000-8000-0000000000e1","cwd":"/work/repo","originator":"codex-tui","source":"cli"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions ..."}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>x</environment_context>"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"add retries to the uploader"}]}}`,
	)
	writeLines(t, filepath.Join(day, "rollout-2026-10-02T11-00-00-01a00000-0000-7000-8000-0000000000e2.jsonl"),
		`{"type":"session_meta","payload":{"id":"01a00000-0000-7000-8000-0000000000e2","cwd":"/elsewhere","originator":"codex-tui","source":"cli"}}`,
		`{"type":"event_msg","payload":{"type":"user_message","message":"other repo"}}`,
	)
	writeLines(t, filepath.Join(day, "rollout-2026-10-02T12-00-00-01a00000-0000-7000-8000-0000000000e3.jsonl"),
		`{"type":"session_meta","payload":{"id":"01a00000-0000-7000-8000-0000000000e3","cwd":"/work/repo","originator":"codex_exec","source":"exec"}}`,
		`{"type":"event_msg","payload":{"type":"user_message","message":"automated stage"}}`,
	)
	got := listCodexSessions([]string{root}, cwd, 10, false, nil)
	if len(got) != 1 || got[0].ID != "01a00000-0000-7000-8000-0000000000e1" || got[0].FirstPrompt != "add retries to the uploader" {
		t.Fatalf("unexpected codex sessions: %+v", got)
	}
	if all := listCodexSessions([]string{root}, cwd, 10, true, nil); len(all) != 2 {
		t.Fatalf("automated=1 should add the exec run, got %+v", all)
	}
}

// Review finding on #365: Claude's project-dir name is lossy — "/work/a-b"
// and "/work/a/b" both encode to "-work-a-b" — so the list must check each
// transcript's recorded cwd, not just the directory it lives in.
func TestListClaudeSessionsRejectsEncodedNameCollisions(t *testing.T) {
	cfg := t.TempDir()
	proj := filepath.Join(cfg, "projects", claudeProjectDirName("/work/a-b"))
	if claudeProjectDirName("/work/a-b") != claudeProjectDirName("/work/a/b") {
		t.Skip("encoding no longer collides")
	}
	writeLines(t, filepath.Join(proj, "11111111-0000-4000-8000-00000000000a.jsonl"),
		`{"type":"user","entrypoint":"cli","cwd":"/work/a-b","message":{"role":"user","content":"my folder"}}`,
	)
	writeLines(t, filepath.Join(proj, "22222222-0000-4000-8000-00000000000b.jsonl"),
		`{"type":"user","entrypoint":"cli","cwd":"/work/a/b","message":{"role":"user","content":"other folder"}}`,
	)
	writeLines(t, filepath.Join(proj, "33333333-0000-4000-8000-00000000000c.jsonl"),
		`{"type":"user","entrypoint":"cli","message":{"role":"user","content":"no recorded cwd"}}`,
	)
	got := listClaudeSessions([]string{cfg}, "/work/a-b", 10, false, nil)
	if len(got) != 1 || got[0].ID != "11111111-0000-4000-8000-00000000000a" {
		t.Fatalf("want only the session recorded in /work/a-b, got %+v", got)
	}
}

func TestSessionsBudgetStopsReading(t *testing.T) {
	cfg := t.TempDir()
	proj := filepath.Join(cfg, "projects", claudeProjectDirName("/w"))
	for i := 0; i < 5; i++ {
		writeLines(t, filepath.Join(proj, "aaaaaaaa-0000-4000-8000-00000000000"+string(rune('a'+i))+".jsonl"),
			`{"type":"user","entrypoint":"cli","cwd":"/w","message":{"role":"user","content":"hello"}}`)
	}
	if got := listClaudeSessions([]string{cfg}, "/w", 10, false, &sessionsBudget{left: 0}); len(got) != 0 {
		t.Fatalf("exhausted budget still read %d sessions", len(got))
	}
	if got := listClaudeSessions([]string{cfg}, "/w", 10, false, &sessionsBudget{left: 1 << 20}); len(got) != 5 {
		t.Fatalf("budgeted listing = %d sessions, want 5", len(got))
	}
}

// Second review: session ids reach `--resume <id>`, so only real ids are
// listed; and the folder check runs before the costly tail read, with each
// provider budgeted separately and charged for bytes actually read.
func TestSessionsRejectNonUUIDIdsAndChargeActualBytes(t *testing.T) {
	cfg := t.TempDir()
	proj := filepath.Join(cfg, "projects", claudeProjectDirName("/w"))
	writeLines(t, filepath.Join(proj, "--dangerous-flag.jsonl"),
		`{"type":"user","entrypoint":"cli","cwd":"/w","message":{"role":"user","content":"hi"}}`)
	writeLines(t, filepath.Join(proj, "aaaaaaaa-0000-4000-8000-00000000000f.jsonl"),
		`{"type":"user","entrypoint":"cli","cwd":"/w","message":{"role":"user","content":"hi"}}`)
	b := &sessionsBudget{left: 1 << 20}
	got := listClaudeSessions([]string{cfg}, "/w", 10, false, b)
	if len(got) != 1 || got[0].ID != "aaaaaaaa-0000-4000-8000-00000000000f" {
		t.Fatalf("want only the uuid-named session, got %+v", got)
	}
	if spent := (1 << 20) - b.left; spent > 4096 {
		t.Fatalf("budget charged %d bytes for two tiny files (reservation, not actual reads)", spent)
	}
}
