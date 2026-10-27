package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHookToolCallPersistsClaudeSessionID(t *testing.T) {
	dir := t.TempDir()
	store, err := NewPaneStore(dir)
	if err != nil {
		t.Fatalf("NewPaneStore: %v", err)
	}
	mgr := NewManager(dir)
	p := transactionTestPane("pane_hook")
	p.Cwd = "/repo"
	mgr.panes[p.ID] = p
	api := &apiServer{mgr: mgr, panes: store, uiBus: newUIBus()}

	body := `{"hook_event_name":"PreToolUse","tool_name":"Read","session_id":"sess-123456789","cwd":"/repo","tool_input":{"file_path":"README.md"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/tool-call", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Soluterminal-Pane", p.ID)
	rec := httptest.NewRecorder()

	api.hookToolCall(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if got := p.Snapshot().ClaudeSessionID; got != "sess-123456789" {
		t.Fatalf("pane ClaudeSessionID = %q, want session id", got)
	}
	list := store.List()
	if len(list) != 1 || list[0].ClaudeSessionID != "sess-123456789" {
		t.Fatalf("persisted panes = %#v, want stored claude session id", list)
	}
}

func TestHookPersistsRecipeRepairWithoutClaudeSessionRotation(t *testing.T) {
	dir := t.TempDir()
	store, err := NewPaneStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(dir)
	p := transactionTestPane("pane_recipe_repair")
	p.Cwd = "/repo"
	p.Cmd = []string{"codex"}
	p.ClaudeSessionID = "current-session"
	p.ClaudeTranscriptPath = "/home/u/.claude/projects/-repo/current-session.jsonl"
	mgr.panes[p.ID] = p
	if err := store.Put(p); err != nil {
		t.Fatal(err)
	}
	api := &apiServer{mgr: mgr, panes: store, uiBus: newUIBus()}
	body := `{"hook_event_name":"SessionStart","session_id":"current-session","transcript_path":"/home/u/.claude/projects/-repo/current-session.jsonl","cwd":"/repo"}`
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/tool-call", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Soluterminal-Pane", p.ID)
	rec := httptest.NewRecorder()
	api.hookToolCall(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%q", rec.Code, rec.Body.String())
	}
	// Reload from disk: the session/path were unchanged, so only the recipe
	// repair can trigger this persistence. An in-memory-only fix regresses on
	// the next daemon restart and revives the old provider's resume command.
	reloaded, err := NewPaneStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	list := reloaded.List()
	if len(list) != 1 {
		t.Fatalf("persisted panes = %d, want 1", len(list))
	}
	snap := list[0]
	if agentFromCmd(snap.Cmd) != agentClaude || snap.ClaudeSessionID != "current-session" || snap.ClaudeTranscriptPath != p.ClaudeTranscriptPath {
		t.Fatalf("recipe repair did not retain persisted Claude continuity: %+v", snap)
	}
}

// SessionStart must pin a freshly-started session's id IMMEDIATELY — before any
// tool call — overwriting a stale prior id the pane still carries from a
// Ctrl-C'd conversation. Without this, switching that pane to Focus resumes the
// old session. (Claude Code only fires SessionStart as a COMMAND hook, which
// curls this same endpoint; the handler treats it like PreToolUse.)
func TestHookToolCallSessionStartPinsFreshSession(t *testing.T) {
	dir := t.TempDir()
	store, err := NewPaneStore(dir)
	if err != nil {
		t.Fatalf("NewPaneStore: %v", err)
	}
	mgr := NewManager(dir)
	p := transactionTestPane("pane_ss")
	p.Cwd = "/repo"
	// Precondition: the pane still holds the PRIOR (Ctrl-C'd) session's id.
	p.ClaudeSessionID = "old-session-0001"
	p.ClaudeTranscriptPath = "/home/u/.claude/projects/-repo/old-session-0001.jsonl"
	p.Agent = agentCodex
	p.CodexThreadID = "019f543c-3f11-7701-bc5e-6a73a50a9083"
	mgr.panes[p.ID] = p
	api := &apiServer{mgr: mgr, panes: store, uiBus: newUIBus()}

	body := `{"hook_event_name":"SessionStart","source":"startup","session_id":"fresh-session-9999","transcript_path":"/home/u/.claude/projects/-repo/fresh-session-9999.jsonl","cwd":"/repo"}`
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/tool-call", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Soluterminal-Pane", p.ID)
	rec := httptest.NewRecorder()

	api.hookToolCall(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	snap := p.Snapshot()
	if snap.ClaudeSessionID != "fresh-session-9999" {
		t.Fatalf("ClaudeSessionID = %q, want fresh id pinned at SessionStart (stale id survived → Focus would resume the old session)", snap.ClaudeSessionID)
	}
	if snap.ClaudeTranscriptPath != "/home/u/.claude/projects/-repo/fresh-session-9999.jsonl" {
		t.Fatalf("ClaudeTranscriptPath = %q, want the fresh transcript pinned", snap.ClaudeTranscriptPath)
	}
	if snap.Agent != "" || snap.CodexThreadID != "" {
		t.Fatalf("provider handoff = {agent:%q codexThread:%q}, want Claude with no stale Codex identity", snap.Agent, snap.CodexThreadID)
	}
	list := store.List()
	if len(list) != 1 || list[0].ClaudeSessionID != "fresh-session-9999" {
		t.Fatalf("persisted = %#v, want fresh id persisted", list)
	}
}

// SessionStart must be registered as a COMMAND hook (http SessionStart hooks
// never fire in Claude Code) and a legacy http SessionStart entry from an older
// daemon must be MIGRATED in place, not duplicated. PreToolUse/PostToolUse stay
// http.
func TestEnsureHooksRegistersCommandSessionStart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SOLU_SKIP_HOOK_REGISTER", "")

	// Seed a legacy http-type SessionStart entry (what older builds wrote).
	settingsDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(settingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"http","url":"http://127.0.0.1:8787/api/hooks/tool-call","headers":{"X-Soluterminal-Pane":"$SOLU_PANE_ID"}}]}]}}`
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	ensureClaudeHooksRegistered("127.0.0.1:8787")

	data, err := os.ReadFile(filepath.Join(settingsDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks == nil {
		t.Fatal("no hooks written")
	}

	ss, _ := hooks["SessionStart"].([]any)
	if len(ss) != 1 {
		t.Fatalf("SessionStart entries = %d, want 1 (legacy http migrated in place, not duplicated)", len(ss))
	}
	hk := ss[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if hk["type"] != "command" {
		t.Fatalf("SessionStart hook type = %v, want command (http SessionStart never fires)", hk["type"])
	}
	cmd, _ := hk["command"].(string)
	if !strings.Contains(cmd, "/api/hooks/tool-call") || !strings.Contains(cmd, "SOLU_PANE_ID") {
		t.Fatalf("SessionStart command missing endpoint or pane header: %q", cmd)
	}

	for _, ev := range []string{"PreToolUse", "PostToolUse"} {
		entries, _ := hooks[ev].([]any)
		if len(entries) != 1 {
			t.Fatalf("%s entries = %d, want 1", ev, len(entries))
		}
		h := entries[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
		if h["type"] != "http" {
			t.Fatalf("%s hook type = %v, want http", ev, h["type"])
		}
	}
}

// A hook resolved by CWD (no X-Soluterminal-Pane header) must NOT overwrite a
// pane's existing session id — only a header-attributed hook is authoritative.
// Prevents a stray out-app `claude` in a shared cwd from clobbering an in-app
// pane's resume identity (the SessionStart hook now fires per launch, so an
// out-app claude could otherwise overwrite an id before running any tool).
func TestHookCwdFallbackDoesNotOverwriteExistingSession(t *testing.T) {
	dir := t.TempDir()
	store, err := NewPaneStore(dir)
	if err != nil {
		t.Fatalf("NewPaneStore: %v", err)
	}
	mgr := NewManager(dir)
	p := transactionTestPane("pane_cwd")
	p.Cwd = "/repo"
	p.Status = PaneRunning
	p.ClaudeSessionID = "in-app-owner-id"
	mgr.panes[p.ID] = p
	api := &apiServer{mgr: mgr, panes: store, uiBus: newUIBus()}

	// Intruder hook: empty header → cwd fallback resolves pane_cwd by /repo.
	body := `{"hook_event_name":"SessionStart","session_id":"intruder-id","transcript_path":"/home/u/.claude/projects/-repo/intruder-id.jsonl","cwd":"/repo"}`
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/tool-call", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	api.hookToolCall(httptest.NewRecorder(), req)
	if got := p.Snapshot().ClaudeSessionID; got != "in-app-owner-id" {
		t.Fatalf("cwd-fallback hook overwrote the pane's id: got %q, want in-app-owner-id", got)
	}

	// Same pane, but WITH the header → authoritative, may re-pin to a new id.
	body2 := `{"hook_event_name":"PreToolUse","tool_name":"Read","session_id":"owner-rotated-id","transcript_path":"/home/u/.claude/projects/-repo/owner-rotated-id.jsonl","cwd":"/repo","tool_input":{"file_path":"x"}}`
	req2 := httptest.NewRequest(http.MethodPost, "/api/hooks/tool-call", strings.NewReader(body2))
	req2.RemoteAddr = "127.0.0.1:12345"
	req2.Header.Set("X-Soluterminal-Pane", p.ID)
	api.hookToolCall(httptest.NewRecorder(), req2)
	if got := p.Snapshot().ClaudeSessionID; got != "owner-rotated-id" {
		t.Fatalf("header-attributed hook should re-pin: got %q, want owner-rotated-id", got)
	}
}

// A pane with NO id yet may still be SEEDED by a cwd-resolved hook — the
// robustness fallback for out-app sessions is preserved; it just can't OVERWRITE.
func TestHookCwdFallbackSeedsEmptySession(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewPaneStore(dir)
	mgr := NewManager(dir)
	p := transactionTestPane("pane_seed")
	p.Cwd = "/repo"
	p.Status = PaneRunning
	mgr.panes[p.ID] = p
	api := &apiServer{mgr: mgr, panes: store, uiBus: newUIBus()}

	body := `{"hook_event_name":"PreToolUse","tool_name":"Read","session_id":"seeded-id","transcript_path":"/home/u/.claude/projects/-repo/seeded-id.jsonl","cwd":"/repo","tool_input":{"file_path":"x"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/tool-call", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	api.hookToolCall(httptest.NewRecorder(), req)
	if got := p.Snapshot().ClaudeSessionID; got != "seeded-id" {
		t.Fatalf("cwd-resolved hook should seed an empty pane: got %q, want seeded-id", got)
	}
}

func TestClaudeHookCallbackAddrUsesLoopbackForWildcardBinds(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0:8787": "127.0.0.1:8787",
		":8787":        "127.0.0.1:8787",
		"[::]:8787":    "127.0.0.1:8787",
		"127.0.0.1:9":  "127.0.0.1:9",
		"localhost:9":  "localhost:9",
	}
	for in, want := range cases {
		if got := claudeHookCallbackAddr(in); got != want {
			t.Fatalf("claudeHookCallbackAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

// A side-port daemon (dev build, staging, sandbox) must never rewrite the
// machine-wide hook URLs: on 2026-07-04 and again in later testing a side
// daemon re-pointed every Claude session's hooks at itself.
func TestEnsureHooksSkipsNonCanonicalPort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SOLU_SKIP_HOOK_REGISTER", "")
	settingsDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(settingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	prod := `{"hooks":{"PostToolUse":[{"matcher":"","hooks":[{"type":"http","url":"http://127.0.0.1:8787/api/hooks/tool-call"}]}]}}`
	path := filepath.Join(settingsDir, "settings.json")
	if err := os.WriteFile(path, []byte(prod), 0o600); err != nil {
		t.Fatal(err)
	}

	ensureClaudeHooksRegistered("127.0.0.1:9791")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != prod {
		t.Fatalf("side-port daemon rewrote the hooks:\n%s", data)
	}
}
