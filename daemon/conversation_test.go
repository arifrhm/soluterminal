package main

import (
	"os"
	"path/filepath"
	"testing"
)

// projectConversation is the pure line→DTO core; this exercises every relevant
// line shape over a synthetic JSONL window and asserts the right rows are
// kept/skipped and that tool hints are redacted consistently with the Activity
// feed.
func TestConversationProjection(t *testing.T) {
	// One synthetic transcript window. Order matters — it mimics a real turn:
	// the user asks, the assistant thinks (dropped), replies (kept), runs a
	// tool (kept, redacted), the tool result comes back as a user line
	// (dropped), and a sidechain/meta/command-wrapper line are all dropped.
	lines := []string{
		// real user prompt → kept
		`{"type":"user","timestamp":"2026-06-20T10:00:00Z","message":{"role":"user","content":"fix the env bug"}}`,
		// assistant thinking → dropped
		`{"type":"assistant","timestamp":"2026-06-20T10:00:01Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hmm let me reason","signature":"x"}]}}`,
		// assistant prose → kept
		`{"type":"assistant","timestamp":"2026-06-20T10:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"I'll read the file first."}]}}`,
		// assistant tool_use (Read) → kept, basename-only hint
		`{"type":"assistant","timestamp":"2026-06-20T10:00:03Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/home/user/project/secret/config.go"}}]}}`,
		// assistant tool_use (Bash, secret-shaped) → kept, hint redacted to "(redacted)"
		`{"type":"assistant","timestamp":"2026-06-20T10:00:04Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"export API_KEY=sk-12345 && deploy"}}]}}`,
		// user line that is ONLY a tool_result → dropped (tool output, not human)
		`{"type":"user","timestamp":"2026-06-20T10:00:05Z","message":{"role":"user","content":[{"type":"tool_result","content":"file contents here"}]}}`,
		// sidechain (subagent) line → dropped even though it has prose
		`{"type":"assistant","timestamp":"2026-06-20T10:00:06Z","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"subagent chatter"}]}}`,
		// isMeta user line → dropped
		`{"type":"user","timestamp":"2026-06-20T10:00:07Z","isMeta":true,"message":{"role":"user","content":"<system reminder injected>"}}`,
		// command-wrapper user stub → dropped
		`{"type":"user","timestamp":"2026-06-20T10:00:08Z","message":{"role":"user","content":"<command-name>/compact</command-name>"}}`,
		// ai-title line → captured as title, not a turn
		`{"type":"ai-title","aiTitle":"Debug the env bug","sessionId":"abc"}`,
		// malformed line → skipped, must not panic
		`{"type":"assistant","message":{"content":[{"type":"text",`,
	}
	buf := []byte(joinLines(lines))

	turns, title := projectConversation(buf, 30)

	if title != "Debug the env bug" {
		t.Fatalf("aiTitle = %q, want %q", title, "Debug the env bug")
	}

	want := []convTurn{
		{Role: "user", Kind: "text", Text: "fix the env bug", Ts: "2026-06-20T10:00:00Z"},
		{Role: "assistant", Kind: "text", Text: "I'll read the file first.", Ts: "2026-06-20T10:00:02Z"},
		{Role: "assistant", Kind: "tool_use", Tool: "Read", ToolHint: "config.go", Ts: "2026-06-20T10:00:03Z"},
		{Role: "assistant", Kind: "tool_use", Tool: "Bash", ToolHint: "(redacted)", Ts: "2026-06-20T10:00:04Z"},
	}
	if len(turns) != len(want) {
		t.Fatalf("got %d turns, want %d: %+v", len(turns), len(want), turns)
	}
	for i := range want {
		if turns[i] != want[i] {
			t.Errorf("turn[%d] = %+v, want %+v", i, turns[i], want[i])
		}
	}

	// Redaction sanity: the basename-only path must NOT leak the directory,
	// and the secret-shaped Bash command must NOT appear verbatim.
	for _, tn := range turns {
		if tn.ToolHint == "/home/user/project/secret/config.go" {
			t.Errorf("tool hint leaked full path: %q", tn.ToolHint)
		}
		if tn.ToolHint == "export API_KEY=sk-12345 && deploy" {
			t.Errorf("tool hint leaked secret command: %q", tn.ToolHint)
		}
	}
}

// TestConversationProjectionLimit asserts the newest `limit` turns are kept.
func TestConversationProjectionLimit(t *testing.T) {
	lines := []string{
		`{"type":"user","timestamp":"t1","message":{"role":"user","content":"one"}}`,
		`{"type":"user","timestamp":"t2","message":{"role":"user","content":"two"}}`,
		`{"type":"user","timestamp":"t3","message":{"role":"user","content":"three"}}`,
	}
	turns, _ := projectConversation([]byte(joinLines(lines)), 2)
	if len(turns) != 2 {
		t.Fatalf("got %d turns, want 2", len(turns))
	}
	if turns[0].Text != "two" || turns[1].Text != "three" {
		t.Errorf("kept wrong turns (want newest): %+v", turns)
	}
}

// TestConversationProjectionEmpty: empty/whitespace input yields no turns and
// no title, and never panics.
func TestConversationProjectionEmpty(t *testing.T) {
	turns, title := projectConversation([]byte("\n  \n\n"), 30)
	if len(turns) != 0 {
		t.Errorf("expected no turns, got %+v", turns)
	}
	if title != "" {
		t.Errorf("expected no title, got %q", title)
	}
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

func TestClaudeProjectDirName(t *testing.T) {
	cases := map[string]string{
		"/home/user/wsl-projects/soluterminal": "-home-user-wsl-projects-soluterminal",
		"/home/u/proj":                         "-home-u-proj",
		"/home/u/.config/app":                  "-home-u--config-app",
	}
	for in, want := range cases {
		if got := claudeProjectDirName(in); got != want {
			t.Errorf("claudeProjectDirName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestIsMainTranscriptPath: only a top-level "<...>/projects/<enc>/<sid>.jsonl"
// may pin a pane. Subagent/Workflow transcripts (nested under "<sid>/...") must
// be rejected so an inherited-pane-header hook can't strand the pane on an
// orphan session id. Empty stays permissive for legacy clients.
func TestIsMainTranscriptPath(t *testing.T) {
	cases := map[string]bool{
		"": true, // legacy hook with no transcript_path → permissive
		"/home/u/.claude/projects/-home-u-proj/abc.jsonl":                                  true,
		"/home/u/.config/claude/projects/-home-u-proj/abc.jsonl":                           true,
		"/home/u/.claude/projects/-home-u-proj/abc/subagents/agent-x.jsonl":                false,
		"/home/u/.claude/projects/-home-u-proj/abc/subagents/workflows/wf_1/journal.jsonl": false,
		"/home/u/.claude/projects/-home-u-proj/abc.txt":                                    false, // not a transcript
		"/home/u/.claude/projects/abc.jsonl":                                               false, // no <enc> dir level
	}
	for in, want := range cases {
		if got := isMainTranscriptPath(in); got != want {
			t.Errorf("isMainTranscriptPath(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestTranscriptForPanePrefersTranscriptPath: a pane whose stored session id is
// a stale/orphan id (no top-level <sid>.jsonl) still resolves to the right file
// when ClaudeTranscriptPath points at a live main transcript — the subagent
// clobbering fix. A subagent/missing path is ignored and we fall through.
func TestTranscriptForPanePrefersTranscriptPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	cwd := "/home/u/wsl-projects/demo"
	projDir := filepath.Join(root, "projects", claudeProjectDirName(cwd))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(projDir, "55ce4d7c-aaaa-bbbb-cccc-000000000001.jsonl")
	if err := os.WriteFile(mainPath, []byte(`{"type":"mode","sessionId":"x"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A subagent path under a different session dir — must never be returned.
	subDir := filepath.Join(projDir, "deadbeef-0000-0000-0000-000000000002", "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	subPath := filepath.Join(subDir, "agent-zzz.jsonl")
	if err := os.WriteFile(subPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(t.TempDir())
	a := &apiServer{mgr: mgr}

	register := func(p *Pane) {
		mgr.mu.Lock()
		mgr.panes[p.ID] = p
		mgr.mu.Unlock()
	}

	// Pane with an orphan session id but a live transcript path → resolves to it.
	p := &Pane{
		ID:                   "p_main",
		Cwd:                  cwd,
		Status:               PaneRunning,
		ClaudeSessionID:      "orphan-id-with-no-file",
		ClaudeTranscriptPath: mainPath,
	}
	register(p)
	if got := a.transcriptForPane(p); got != mainPath {
		t.Errorf("transcript path pin: got %q, want %q", got, mainPath)
	}

	// A subagent transcript path must be rejected (defence-in-depth), falling
	// through to id/cwd resolution which here finds nothing → "".
	pSub := &Pane{
		ID:                   "p_sub",
		Cwd:                  cwd,
		Status:               PaneRunning,
		ClaudeSessionID:      "another-orphan",
		ClaudeTranscriptPath: subPath,
	}
	if got := a.transcriptForPane(pSub); got == subPath {
		t.Errorf("subagent path must not be returned, got %q", got)
	}

	// A stored path that no longer exists is ignored (file removed/rotated).
	pGone := &Pane{
		ID:                   "p_gone",
		Cwd:                  cwd,
		Status:               PaneRunning,
		ClaudeTranscriptPath: filepath.Join(projDir, "does-not-exist.jsonl"),
	}
	if got := a.transcriptForPane(pGone); got == pGone.ClaudeTranscriptPath {
		t.Errorf("missing path must not be returned, got %q", got)
	}
}
