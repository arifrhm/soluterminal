package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GET /api/sessions/recent?cwd=<dir>&limit=N[&automated=1]
//
// Past Claude Code and Codex conversations started in a folder, newest first,
// so the New Pane dialog can offer "resume that one" with its first prompt and
// title instead of the bare `claude --resume` picker. Interactive sessions
// only by default: workflow / headless runs (Claude entrypoint sdk-*, Codex
// `exec` and soluterminal-originated rollouts) vastly outnumber them and are
// noise here; automated=1 includes them.
//
// Bounded work: only the newest sessionsScanCap files per source are opened,
// and each read is a capped head (first prompt) plus tail (latest title).

const (
	sessionsDefaultLimit = 12
	sessionsMaxLimit     = 40
	sessionsScanCap      = 160
	sessionsHeadBytes    = 64 << 10
	sessionsTailBytes    = 128 << 10
	sessionsPromptChars  = 200
	codexDayDirsScanned  = 45
	// Transcript bytes one request may read PER PROVIDER (heads + tails), so
	// a huge history can't turn one dialog open into a multi-hundred-MB read,
	// and one provider's noise can't starve the other's listing.
	sessionsReadBudget = 12 << 20
)

// sessionsBudget is the per-request read allowance; nil = unlimited (tests).
// Callers ask for an allowance, read, then charge what they actually read.
type sessionsBudget struct{ left int64 }

func (b *sessionsBudget) allow(n int64) int64 {
	if b == nil {
		return n
	}
	return max(0, min(n, b.left))
}

func (b *sessionsBudget) charge(n int64) {
	if b != nil {
		b.left -= n
	}
}

func (b *sessionsBudget) exhausted() bool { return b != nil && b.left <= 0 }

// countingReader charges a budget for the bytes a scanner actually consumes.
type countingReader struct {
	r      io.Reader
	budget *sessionsBudget
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.budget.charge(int64(n))
	return n, err
}

type browsedSession struct {
	Agent       string `json:"agent"` // "claude" | "codex"
	ID          string `json:"id"`
	Title       string `json:"title,omitempty"`
	FirstPrompt string `json:"firstPrompt,omitempty"`
	UpdatedMs   int64  `json:"updatedMs"`
	Automated   bool   `json:"automated,omitempty"`
	Cwd         string `json:"cwd"`
}

func (a *apiServer) recentSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cwd, err := canonicalizeFilePath(q.Get("cwd"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	limit := sessionsDefaultLimit
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		limit = min(n, sessionsMaxLimit)
	}
	automated := q.Get("automated") == "1"
	out := listClaudeSessions(claudeConfigDirs(), cwd, limit, automated, &sessionsBudget{left: sessionsReadBudget})
	out = append(out, listCodexSessions(codexSessionsRoots(), cwd, limit, automated, &sessionsBudget{left: sessionsReadBudget})...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedMs > out[j].UpdatedMs })
	if len(out) > limit {
		out = out[:limit]
	}
	writeJSON(w, 200, map[string]any{"sessions": out})
}

type fileAge struct {
	path string
	mod  time.Time
}

func newestFiles(paths []string, capN int) []fileAge {
	files := make([]fileAge, 0, len(paths))
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			files = append(files, fileAge{p, info.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	if len(files) > capN {
		files = files[:capN]
	}
	return files
}

func listClaudeSessions(configDirs []string, cwd string, limit int, automated bool, budget *sessionsBudget) []browsedSession {
	var paths []string
	seenDir := map[string]bool{}
	for _, d := range configDirs {
		proj := filepath.Join(d, "projects", claudeProjectDirName(cwd))
		if seenDir[proj] {
			continue
		}
		seenDir[proj] = true
		entries, err := os.ReadDir(proj)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
				paths = append(paths, filepath.Join(proj, e.Name()))
			}
		}
	}
	var out []browsedSession
	for _, f := range newestFiles(paths, sessionsScanCap) {
		if budget.exhausted() {
			break
		}
		s, ok := readClaudeSessionSummary(f.path, cwd, automated, budget)
		if !ok || (s.Automated && !automated) {
			continue
		}
		s.UpdatedMs, s.Cwd = f.mod.UnixMilli(), cwd
		out = append(out, s)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func readHead(path string, n int64) []byte {
	if n <= 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf, _ := io.ReadAll(io.LimitReader(f, n))
	return buf
}

func clipPrompt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > sessionsPromptChars {
		return string(r[:sessionsPromptChars-1]) + "…"
	}
	return s
}

// readClaudeSessionSummary also verifies the transcript's own recorded cwd:
// Claude's project-dir encoding is lossy ("/a-b" and "/a/b" both encode to
// "-a-b"), so a directory match alone can list another folder's sessions.
// A transcript with no recorded cwd cannot be attributed and is skipped.
func readClaudeSessionSummary(path, cwd string, automated bool, budget *sessionsBudget) (browsedSession, bool) {
	s := browsedSession{Agent: "claude"}
	// The id goes on to `claude --resume <id>`: accept only a real session id.
	id, ok := canonicalCodexThreadID(strings.TrimSuffix(filepath.Base(path), ".jsonl"))
	if !ok {
		return s, false
	}
	s.ID = id
	head := readHead(path, budget.allow(sessionsHeadBytes))
	budget.charge(int64(len(head)))
	if len(head) == 0 {
		return s, false
	}
	sawEntry := false
	recordedCwd := ""
	for _, raw := range bytes.Split(head, []byte("\n")) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var ln struct {
			convLine
			Entrypoint string `json:"entrypoint"`
			SessionID  string `json:"sessionId"`
			Cwd        string `json:"cwd"`
		}
		if json.Unmarshal(raw, &ln) != nil {
			continue
		}
		if ln.Cwd != "" && recordedCwd == "" {
			recordedCwd = ln.Cwd
		}
		if ln.Entrypoint != "" && !sawEntry {
			sawEntry = true
			s.Automated = strings.HasPrefix(ln.Entrypoint, "sdk")
		}
		if ln.Type == "ai-title" && ln.AITitle != "" {
			s.Title = ln.AITitle
		}
		if s.FirstPrompt == "" && ln.Type == "user" && !ln.IsSidechain && ln.Message != nil {
			if text, ok := userPromptText(&ln.convLine); ok {
				s.FirstPrompt = clipPrompt(text)
			}
		}
		if s.FirstPrompt != "" && sawEntry && recordedCwd != "" {
			break
		}
	}
	// Decide from the head before paying for the tail read.
	if recordedCwd == "" || filepath.Clean(recordedCwd) != filepath.Clean(cwd) {
		return s, false
	}
	// A transcript with no human prompt (a crashed start, a pure tool run) has
	// nothing to resume into.
	if s.FirstPrompt == "" || (s.Automated && !automated) {
		return s, false
	}
	if n := budget.allow(sessionsTailBytes); n > 0 {
		if tail, err := tailReadBounded(path, n); err == nil {
			budget.charge(int64(len(tail)))
			if t := latestAITitle(tail); t != "" {
				s.Title = t
			}
		}
	}
	return s, true
}

func listCodexSessions(roots []string, cwd string, limit int, automated bool, budget *sessionsBudget) []browsedSession {
	var paths []string
	for _, root := range roots {
		days := codexRecentDayDirs(root, codexDayDirsScanned)
		for _, d := range days {
			entries, err := os.ReadDir(d)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if !e.IsDir() && strings.HasPrefix(e.Name(), "rollout-") && strings.HasSuffix(e.Name(), ".jsonl") {
					paths = append(paths, filepath.Join(d, e.Name()))
				}
			}
		}
	}
	var out []browsedSession
	for _, f := range newestFiles(paths, sessionsScanCap*3) {
		if budget.exhausted() {
			break
		}
		s, ok := readCodexSessionSummary(f.path, cwd, budget)
		if !ok || (s.Automated && !automated) {
			continue
		}
		s.UpdatedMs, s.Cwd = f.mod.UnixMilli(), cwd
		out = append(out, s)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// codexRecentDayDirs returns up to n sessions/YYYY/MM/DD dirs, newest first.
func codexRecentDayDirs(root string, n int) []string {
	var out []string
	years := sortedSubdirsDesc(root)
	for _, y := range years {
		for _, m := range sortedSubdirsDesc(y) {
			for _, d := range sortedSubdirsDesc(m) {
				out = append(out, d)
				if len(out) >= n {
					return out
				}
			}
		}
	}
	return out
}

func sortedSubdirsDesc(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

func readCodexSessionSummary(path, cwd string, budget *sessionsBudget) (browsedSession, bool) {
	s := browsedSession{Agent: "codex"}
	f, err := os.Open(path)
	if err != nil {
		return s, false
	}
	defer f.Close()
	sc := bufio.NewScanner(countingReader{io.LimitReader(f, budget.allow(sessionsHeadBytes*4)), budget})
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	first := true
	for sc.Scan() {
		var ln struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &ln) != nil {
			continue
		}
		if first {
			first = false
			if ln.Type != "session_meta" {
				return s, false
			}
			var meta struct {
				ID         string `json:"id"`
				Cwd        string `json:"cwd"`
				Originator string `json:"originator"`
				Source     string `json:"source"`
			}
			if json.Unmarshal(ln.Payload, &meta) != nil || filepath.Clean(meta.Cwd) != filepath.Clean(cwd) {
				return s, false
			}
			id, ok := canonicalCodexThreadID(meta.ID)
			if !ok {
				return s, false
			}
			s.ID = id
			s.Automated = meta.Source == "exec" || strings.HasPrefix(meta.Originator, "soluterminal") || meta.Originator == "codex_exec"
			continue
		}
		switch ln.Type {
		case "event_msg":
			var ev struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			}
			if json.Unmarshal(ln.Payload, &ev) == nil && ev.Type == "user_message" && !codexInjectedText(ev.Message) {
				s.FirstPrompt = clipPrompt(ev.Message)
			}
		case "response_item":
			var msg struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			}
			if json.Unmarshal(ln.Payload, &msg) != nil || msg.Type != "message" || msg.Role != "user" {
				continue
			}
			for _, c := range msg.Content {
				if c.Type == "input_text" && !codexInjectedText(c.Text) {
					s.FirstPrompt = clipPrompt(c.Text)
					break
				}
			}
		}
		if s.FirstPrompt != "" {
			break
		}
	}
	return s, s.ID != "" && s.FirstPrompt != ""
}

// codexInjectedText recognises the user-role items Codex writes on the
// user's behalf (AGENTS.md, environment context, skill bodies) so the first
// REAL prompt is what the list shows.
func codexInjectedText(t string) bool {
	t = strings.TrimSpace(t)
	return t == "" || strings.HasPrefix(t, "<") || strings.HasPrefix(t, "# AGENTS.md") || strings.HasPrefix(t, "---\n")
}
