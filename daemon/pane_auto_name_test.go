package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stubTitleRunner replaces the `claude -p` run and records what it was shown.
func stubTitleRunner(t *testing.T, reply string, err error) *atomic.Value {
	t.Helper()
	seen := &atomic.Value{}
	prev := titleRunner
	titleRunner = func(_ context.Context, prompt string) (string, error) {
		seen.Store(prompt)
		return reply, err
	}
	t.Cleanup(func() { titleRunner = prev })
	return seen
}

// namingHome isolates the Claude and Codex conversation stores.
func namingHome(t *testing.T) (claudeHome, codexHome string) {
	t.Helper()
	claudeHome, codexHome = t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("HOME", t.TempDir())
	return
}

var codexThreadSeq atomic.Int32

// withCodexRollout gives p a Codex session whose rollout holds these turns.
func withCodexRollout(t *testing.T, codexHome string, p *Pane, turns ...[2]string) {
	t.Helper()
	id := fmt.Sprintf("0199a5b2-0000-7000-8000-%012d", codexThreadSeq.Add(1))
	dir := filepath.Join(codexHome, "sessions", "2026", "10", "26")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>cwd</environment_context>"}]}}` + "\n")
	for _, tr := range turns {
		fmt.Fprintf(&b, `{"type":"response_item","payload":{"type":"message","role":%q,"content":[{"type":"text","text":%q}]}}`+"\n", tr[0], tr[1])
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-10-26T00-00-00-"+id+".jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	p.CodexThreadID = id
}

func namingServer(t *testing.T, autoName bool) (*apiServer, *Pane) {
	t.Helper()
	a := &apiServer{mgr: NewManager(""), naming: newPaneNaming(t.TempDir())}
	if autoName {
		if err := a.naming.setAutoName(true); err != nil {
			t.Fatal(err)
		}
	}
	p := testPaneWithTail(t, "p1", "codex", "")
	p.Agent = agentCodex
	p.CreatedAt = time.Now().Add(time.Second) // opened after the setting
	a.mgr.panes[p.ID] = p
	return a, p
}

func waitNamed(t *testing.T, a *apiServer) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for a.naming.pending.Load() > 0 {
		if time.Now().After(deadline) {
			t.Fatal("auto-naming did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSuggestPaneNameReadsTheAgentsOwnConversation(t *testing.T) {
	_, codexHome := namingHome(t)
	seen := stubTitleRunner(t, `"Fix login redirect."`, nil)
	a, p := namingServer(t, false)
	p.LastPrompts = []string{"export TOKEN=s3cr3t", "fix the login redirect"}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/panes/p1/name/suggest", nil)
	req.SetPathValue("id", "p1")
	a.handleSuggestPaneName(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("typed lines alone are not a conversation: %d %s", rec.Code, rec.Body)
	}
	withCodexRollout(t, codexHome, p, [2]string{"user", "the login page loops back to itself, fix it"}, [2]string{"assistant", "Found the redirect loop."})
	p.Origin = &PaneOrigin{Source: paneOriginAgent, Purpose: "Fix the login bug"}
	rec = httptest.NewRecorder()
	a.handleSuggestPaneName(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"Fix login redirect"`) {
		t.Fatalf("suggest: %d %s", rec.Code, rec.Body)
	}
	prompt := seen.Load().(string)
	if !strings.Contains(prompt, "Started for: Fix the login bug\nUser: the login page loops back to itself, fix it\nAssistant: Found the redirect loop.") {
		t.Fatalf("model input:\n%s", prompt)
	}
	// Never the typed-line history, never Codex's injected context.
	if strings.Contains(prompt, "s3cr3t") || strings.Contains(prompt, "environment_context") {
		t.Fatalf("model saw typed lines or injected context:\n%s", prompt)
	}
	if p.Snapshot().Title != "codex" {
		t.Fatal("a suggestion must not rename the pane")
	}
}

func TestSuggestPaneNameWithoutClaudeCode(t *testing.T) {
	_, codexHome := namingHome(t)
	stubTitleRunner(t, "", errTitleCLIUnavailable)
	a, p := namingServer(t, false)
	withCodexRollout(t, codexHome, p, [2]string{"user", "hello"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetPathValue("id", "p1")
	a.handleSuggestPaneName(rec, req)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "Claude Code") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

// A pane that isn't running Claude must not be named from another session's
// transcript that happens to sit in the same folder.
func TestNamingNeverReadsAnotherSessionsTranscript(t *testing.T) {
	home, _ := namingHome(t)
	cwd := t.TempDir()
	dir := filepath.Join(home, "projects", claudeProjectDirName(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	other := `{"type":"user","message":{"role":"user","content":"publish release 0.0.1217?"},"sessionId":"s-other"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "s-other.jsonl"), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	a, p := namingServer(t, false)
	p.Cwd = cwd
	p.LastPrompts = []string{"fix the backup job"}
	if text := a.paneNamingText(p); text != "" {
		t.Fatalf("naming text:\n%s", text)
	}
	// Its own session id does select its transcript.
	p.ClaudeSessionID = "s-other"
	if path := ownTranscript(p); !strings.HasSuffix(path, "s-other.jsonl") {
		t.Fatalf("own transcript: %q", path)
	}
	if text := a.paneNamingText(p); !strings.Contains(text, "0.0.1217") {
		t.Fatalf("own transcript text:\n%s", text)
	}
}

func TestAutoNameIsOptInAndOncePerGenericAgentPane(t *testing.T) {
	_, codexHome := namingHome(t)
	var runs atomic.Int32
	prev := titleRunner
	titleRunner = func(context.Context, string) (string, error) { runs.Add(1); return "Login redirect fix", nil }
	t.Cleanup(func() { titleRunner = prev })

	// Off by default.
	a, p := namingServer(t, false)
	withCodexRollout(t, codexHome, p, [2]string{"user", "fix the login redirect"})
	p.LastPrompts = []string{"fix the login redirect"}
	a.maybeAutoNamePane(p)
	if runs.Load() != 0 || p.Snapshot().Title != "codex" || a.naming.pending.Load() != 0 {
		t.Fatal("auto-naming ran while off")
	}

	a, p = namingServer(t, true)
	withCodexRollout(t, codexHome, p, [2]string{"user", "fix the login redirect"})
	a.maybeAutoNamePane(p) // no prompt yet
	waitNamed(t, a)
	if runs.Load() != 0 {
		t.Fatal("named a pane with no prompt")
	}
	p.LastPrompts = []string{"fix the login redirect"}
	a.maybeAutoNamePane(p)
	waitNamed(t, a)
	if got := p.Snapshot().Title; got != "Login redirect fix" || runs.Load() != 1 {
		t.Fatalf("title %q after %d runs", got, runs.Load())
	}
	// Once only, and never over a real name.
	p.Rename("codex")
	a.maybeAutoNamePane(p)
	waitNamed(t, a)
	if runs.Load() != 1 {
		t.Fatal("named the same pane twice")
	}
	q := testPaneWithTail(t, "p2", "Release notes", "")
	q.Agent = agentCodex
	q.CreatedAt = p.CreatedAt
	q.LastPrompts = []string{"draft the notes"}
	a.mgr.panes[q.ID] = q
	a.maybeAutoNamePane(q)
	waitNamed(t, a)
	if runs.Load() != 1 || q.Snapshot().Title != "Release notes" {
		t.Fatal("replaced a name someone gave")
	}
	// A plain shell pane is never queued.
	sh := testPaneWithTail(t, "p3", "bash", "")
	sh.CreatedAt = p.CreatedAt
	sh.LastPrompts = []string{"export TOKEN=abc"}
	a.mgr.panes[sh.ID] = sh
	a.maybeAutoNamePane(sh)
	if a.naming.pending.Load() != 0 || runs.Load() != 1 {
		t.Fatal("queued a shell pane")
	}
}

// Turning the setting on (or restarting the daemon) does not rename the panes
// already open: the prompt hook also runs on a sweep over every pane.
func TestAutoNameOnlyNamesPanesOpenedAfterItWasTurnedOn(t *testing.T) {
	_, codexHome := namingHome(t)
	var runs atomic.Int32
	prev := titleRunner
	titleRunner = func(context.Context, string) (string, error) { runs.Add(1); return "Name", nil }
	t.Cleanup(func() { titleRunner = prev })
	a, p := namingServer(t, true)
	p.CreatedAt = time.Now().Add(-time.Hour)
	withCodexRollout(t, codexHome, p, [2]string{"user", "x"})
	p.LastPrompts = []string{"x"}
	a.maybeAutoNamePane(p)
	waitNamed(t, a)
	if runs.Load() != 0 || p.Snapshot().Title != "codex" {
		t.Fatal("renamed a pane that was open before auto-naming was turned on")
	}
	// Saving "on" again keeps the cut-off.
	at := a.naming.enabledAt()
	if err := a.naming.setAutoName(true); err != nil || a.naming.enabledAt() != at {
		t.Fatal("re-saving 'on' moved the cut-off")
	}
}

func TestAutoNameYieldsToARenameWhileItRuns(t *testing.T) {
	_, codexHome := namingHome(t)
	a, p := namingServer(t, true)
	withCodexRollout(t, codexHome, p, [2]string{"user", "x"})
	p.LastPrompts = []string{"x"}
	started, release := make(chan struct{}), make(chan struct{})
	prev := titleRunner
	titleRunner = func(context.Context, string) (string, error) { close(started); <-release; return "Model name", nil }
	t.Cleanup(func() { titleRunner = prev })
	a.maybeAutoNamePane(p)
	<-started // the model is running now
	p.Rename("My name")
	close(release)
	waitNamed(t, a)
	if got := p.Snapshot().Title; got != "My name" {
		t.Fatalf("auto-name overwrote the user's rename: %q", got)
	}
}

// Panes prompted together are all named: the second is queued, not dropped.
func TestAutoNameQueuesPanesPromptedTogether(t *testing.T) {
	_, codexHome := namingHome(t)
	a, p := namingServer(t, true)
	release := make(chan struct{})
	var n atomic.Int32
	prev := titleRunner
	titleRunner = func(context.Context, string) (string, error) {
		<-release
		return fmt.Sprintf("Name %d", n.Add(1)), nil
	}
	t.Cleanup(func() { titleRunner = prev })
	q := testPaneWithTail(t, "p2", "codex", "")
	q.Agent = agentCodex
	q.CreatedAt = p.CreatedAt
	a.mgr.panes[q.ID] = q
	withCodexRollout(t, codexHome, p, [2]string{"user", "one"})
	withCodexRollout(t, codexHome, q, [2]string{"user", "two"})
	p.LastPrompts = []string{"one"}
	q.LastPrompts = []string{"two"}
	a.maybeAutoNamePane(p)
	a.maybeAutoNamePane(q) // while the first is still running
	close(release)
	waitNamed(t, a)
	if paneTitleIsGeneric(p.Snapshot().Title) || paneTitleIsGeneric(q.Snapshot().Title) {
		t.Fatalf("titles %q, %q", p.Snapshot().Title, q.Snapshot().Title)
	}
}

// A pane closed while queued costs no model run.
func TestAutoNameSkipsAPaneClosedWhileQueued(t *testing.T) {
	_, codexHome := namingHome(t)
	var runs atomic.Int32
	prev := titleRunner
	titleRunner = func(context.Context, string) (string, error) { runs.Add(1); return "Name", nil }
	t.Cleanup(func() { titleRunner = prev })
	a, p := namingServer(t, true)
	withCodexRollout(t, codexHome, p, [2]string{"user", "x"})
	p.LastPrompts = []string{"x"}
	delete(a.mgr.panes, p.ID)
	a.autoNameOne(p)
	if runs.Load() != 0 {
		t.Fatal("ran the model for a closed pane")
	}
}

func TestRenameIfGenericIsConditional(t *testing.T) {
	p := testPaneWithTail(t, "p", "codex", "")
	if prev, ok := p.renameIfGeneric("Model name"); !ok || prev != "codex" || p.Snapshot().Title != "Model name" {
		t.Fatal("generic title not replaced")
	}
	if _, ok := p.renameIfGeneric("Other"); ok || p.Snapshot().Title != "Model name" {
		t.Fatal("replaced a real name")
	}
}

func TestNewestLinesWithinKeepsWholeLinesAndRunes(t *testing.T) {
	if got := newestLinesWithin([]string{"aaaa", "bbbb", "cccc"}, 10); got != "bbbb\ncccc" {
		t.Fatalf("got %q", got)
	}
	if got := newestLinesWithin([]string{strings.Repeat("é", 10)}, 7); got != "ééé" {
		t.Fatalf("rune cut: %q", got)
	}
	// A huge newest reply is clamped, so the prompt before it still fits.
	got := newestLinesWithin([]string{"User: fix the login", "Assistant: " + strings.Repeat("x", 100000)}, 4000)
	if !strings.HasPrefix(got, "User: fix the login\nAssistant: ") || len(got) > 4000 {
		t.Fatalf("long reply crowded out the prompt (%d bytes)", len(got))
	}
}

// At the first prompt the conversation is often not on disk yet: the pane is
// not given up on, and is named once its conversation appears.
func TestAutoNameRetriesUntilTheConversationExists(t *testing.T) {
	_, codexHome := namingHome(t)
	var runs atomic.Int32
	prev := titleRunner
	titleRunner = func(context.Context, string) (string, error) { runs.Add(1); return "Login fix", nil }
	t.Cleanup(func() { titleRunner = prev })
	a, p := namingServer(t, true)
	p.LastPrompts = []string{"fix the login"}
	a.maybeAutoNamePane(p) // no thread id yet
	waitNamed(t, a)
	if runs.Load() != 0 || a.naming.count != 0 {
		t.Fatal("ran the model, or spent an hourly slot, with nothing to read")
	}
	withCodexRollout(t, codexHome, p, [2]string{"user", "fix the login"})
	a.maybeAutoNamePane(p) // the sweep, once the thread is known
	waitNamed(t, a)
	if p.Snapshot().Title != "Login fix" || runs.Load() != 1 {
		t.Fatalf("title %q after %d runs", p.Snapshot().Title, runs.Load())
	}
}

// A failed run is not retried for that pane (Suggest still works).
func TestAutoNameDoesNotRetryAFailedRun(t *testing.T) {
	_, codexHome := namingHome(t)
	var runs atomic.Int32
	prev := titleRunner
	titleRunner = func(context.Context, string) (string, error) { runs.Add(1); return "", errTitleCLIUnavailable }
	t.Cleanup(func() { titleRunner = prev })
	a, p := namingServer(t, true)
	withCodexRollout(t, codexHome, p, [2]string{"user", "x"})
	p.LastPrompts = []string{"x"}
	a.maybeAutoNamePane(p)
	waitNamed(t, a)
	a.maybeAutoNamePane(p)
	waitNamed(t, a)
	if runs.Load() != 1 {
		t.Fatalf("%d runs", runs.Load())
	}
	// A closed pane is forgotten.
	delete(a.mgr.panes, p.ID)
	a.pruneAutoNameTried()
	if _, ok := a.naming.tried.Load(p.ID); ok {
		t.Fatal("closed pane still remembered")
	}
}

func TestPaneNamingSettingPersistsAndIsLocalOnly(t *testing.T) {
	dir := t.TempDir()
	a := &apiServer{naming: newPaneNaming(dir)}
	rec := httptest.NewRecorder()
	a.handleGetPaneNaming(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rec.Body.String(), `"autoName":false`) {
		t.Fatalf("default: %s", rec.Body)
	}
	rec = httptest.NewRecorder()
	a.handlePutPaneNaming(rec, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"autoName":true}`)))
	if rec.Code != 200 || !newPaneNaming(dir).autoName() || newPaneNaming(dir).enabledAt() == 0 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	relayed := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"autoName":false}`))
	relayed.Header.Set("X-Forwarded-Prefix", "/m/mach_x")
	rec = httptest.NewRecorder()
	a.handlePutPaneNaming(rec, relayed)
	if rec.Code != http.StatusForbidden || !a.naming.autoName() {
		t.Fatalf("relayed put: %d", rec.Code)
	}
}

func TestPaneNamingSettingFailedSaveLeavesItOff(t *testing.T) {
	dir := t.TempDir()
	n := newPaneNaming(dir)
	n.path = filepath.Join(dir, "missing-dir", paneNamingSettingsFile)
	if err := n.setAutoName(true); err == nil {
		t.Fatal("save into a missing directory succeeded")
	}
	if n.autoName() {
		t.Fatal("a failed save turned auto-naming on")
	}
}
