package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Recent-conversation endpoint for a pane's Claude Code session.
//
// GET /api/panes/{id}/conversation?limit=N returns the recent *human-readable*
// conversation for the pane's Claude Code session: the prompts the user typed,
// the assistant's prose replies, and the tools it ran — so a mobile client can
// render "You asked X → assistant replied <prose> → ran Edit foo.go" instead of
// the raw, noisy tool-call log.
//
// Privacy posture (this MUST never leak more than the Activity feed does):
//   - We read message *content*, which the Activity feed deliberately does not.
//     To stay consistent we redact tool inputs through the EXACT same
//     redactHookHint path the feed uses (basename-only file paths, secret-
//     shaped Bash commands collapsed to "(redacted)", URLs reduced to host).
//   - assistant `thinking` blocks are dropped entirely — chain-of-thought is
//     never surfaced.
//   - We never include tool *outputs* (user lines that are only tool_result
//     blocks are skipped), command-wrapper/meta user lines, or subagent
//     (isSidechain) turns.
//   - Exposure matches the other /api/panes/{id}/* GETs: relay-authed, no extra
//     loopback gate (the mobile app reaches this over the relay).
//
// Boundedness: transcripts are median ~167KB but can reach tens of MB. We
// tail-read a bounded window from the END of the file (convTailWindowBytes) and
// only project the complete lines in it — never io.ReadAll the whole file.

const (
	// convTailWindowBytes bounds how many bytes we read from the tail of a
	// transcript. A few hundred KB comfortably holds dozens of recent turns
	// (each transcript line is typically well under a few KB of the content
	// we keep), while capping memory + IO regardless of total file size.
	convTailWindowBytes = 512 << 10 // 512 KiB

	convDefaultLimit = 30
	convMinLimit     = 1
	convMaxLimit     = 200
)

// convLine is the subset of a transcript line we decode for conversation
// projection. message.content is intentionally json.RawMessage because it is
// polymorphic: a STRING for real user prompts, or a LIST of blocks for
// assistant turns and tool_result user lines. We decode it on demand per role.
type convLine struct {
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	AITitle     string `json:"aiTitle"` // present only on type:"ai-title" lines
	Message     *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// convBlock is one element of an assistant (or tool_result) content list.
type convBlock struct {
	Type  string         `json:"type"` // "text" | "thinking" | "tool_use" | "tool_result" | ...
	Text  string         `json:"text"`
	Name  string         `json:"name"`  // tool name, for type:"tool_use"
	Input map[string]any `json:"input"` // tool input, for type:"tool_use"
}

// convTurn is the projected DTO a client renders. Kept deliberately small.
type convTurn struct {
	Role     string `json:"role"`               // "user" | "assistant"
	Kind     string `json:"kind"`               // "text" | "tool_use"
	Text     string `json:"text,omitempty"`     // prompt / prose (text kind)
	Tool     string `json:"tool,omitempty"`     // tool name (tool_use kind)
	ToolHint string `json:"toolHint,omitempty"` // redacted hint (tool_use kind)
	Ts       string `json:"ts,omitempty"`       // line timestamp (RFC3339)
}

// paneConversation handles GET /api/panes/{id}/conversation. See the file
// header for the privacy posture. It never panics on a malformed transcript:
// resolution failures and parse errors degrade to an empty/partial result.
func (a *apiServer) paneConversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pane, ok := a.mgr.Get(id)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	limit := convDefaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if parsed, err := parseIntClamped(v, convMinLimit, convMaxLimit); err == nil {
			limit = parsed
		}
	}

	// The user's per-pane title override wins over ai-title in the header.
	// Surfaced on EVERY response path below (including the no-transcript and
	// unreadable-file early returns) so a manually-set title still shows on
	// panes with no resolvable conversation — shell / degraded panes, which is
	// exactly where a manual title is most useful. Empty is omitted.
	ov := strings.TrimSpace(pane.Snapshot().TitleOverride)
	emptyResp := func() map[string]any {
		m := map[string]any{"turns": []convTurn{}}
		if ov != "" {
			m["titleOverride"] = ov
		}
		return m
	}

	// Resolve the pane's transcript: exact session-id match first, then a SAFE
	// cwd fallback for a stale/rotated id (after /compact, resume, or a daemon
	// restart). The fallback never guesses in a way that could show another
	// pane's conversation — see transcriptForPane.
	path := a.transcriptForPane(pane)
	if path == "" {
		writeJSON(w, 200, emptyResp())
		return
	}

	buf, err := tailReadBounded(path, convTailWindowBytes)
	if err != nil {
		// File vanished / unreadable — treat as no conversation rather than a
		// 500; the session may have been cleaned up underneath us.
		writeJSON(w, 200, emptyResp())
		return
	}

	turns, title := projectConversation(buf, limit)
	// Warm the pane's ai-title cache so the bulk /api/panes list can show this
	// pane's generated title without re-reading the transcript itself.
	pane.setAITitleCache(title)
	resp := map[string]any{"turns": turns}
	if title != "" {
		resp["aiTitle"] = title
	}
	if ov != "" {
		resp["titleOverride"] = ov
	}
	writeJSON(w, 200, resp)
}

// transcriptPathForSession returns the .jsonl path for a Claude Code session
// id, or "" if not found. It reuses the daemon's existing transcript discovery
// (claudeConfigDirs + listTranscripts) and the existing path→sessionId mapping
// (sessionIDForTranscript) rather than re-deriving the
// ~/.claude/projects/<enc>/<sessionId>.jsonl layout. Subagent files map to the
// PARENT session id, so they are skipped here (we only want the main thread's
// transcript, whose base name IS the session id).
func transcriptPathForSession(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	for _, path := range listTranscripts(claudeConfigDirs()) {
		if isSubagentTranscript(path) {
			continue // subagent thread, not the main session file
		}
		if sessionIDForTranscript(path) == sessionID {
			return path
		}
	}
	return ""
}

// claudeProjectDirName encodes a cwd the way Claude Code names its transcript
// project dir: "/home/u/proj" → "-home-u-proj" (path separators and dots → "-").
//
// On Windows the separator is "\" and the drive letter carries a ":", so
// "C:\Users\u\proj" has to encode as "C--Users-u-proj". Replacing those two on
// POSIX as well would corrupt paths that legitimately contain them, so the
// extra pair is Windows-only rather than unconditional.
//
// This is a FAST PATH, not a guarantee: it reproduces Claude Code's naming by
// imitation, so a mismatch is always possible. Callers that would do something
// destructive on a miss (claudeTranscriptOnDisk, which drops a conversation)
// must not trust a negative from this alone — see the id scan there.
func claudeProjectDirName(cwd string) string {
	return claudeProjectDirNameFor(runtime.GOOS, cwd)
}

// Split from claudeProjectDirName so the Windows encoding is reachable from a
// test on any host — the POSIX-only encoding shipped broken precisely because
// nothing could exercise the other branch.
func claudeProjectDirNameFor(goos, cwd string) string {
	if goos == "windows" {
		return strings.NewReplacer("/", "-", `\`, "-", ":", "-", ".", "-").Replace(cwd)
	}
	return strings.NewReplacer("/", "-", ".", "-").Replace(cwd)
}

// transcriptForPane resolves a pane to its transcript file.
//
//  0. Stored ClaudeTranscriptPath — the authoritative anchor. The hook records
//     the MAIN transcript_path on every main-session tool call, so it follows
//     id rotation and is never a subagent/workflow orphan. Used whenever the
//     file still exists.
//  1. Exact stored ClaudeSessionID match — for legacy clients/panes that have
//     no transcript_path yet.
//  2. SAFE cwd fallback for a STALE id: a session id rotates after /compact,
//     --resume, or a daemon restart, leaving the pane pointed at a transcript
//     that no longer exists. We then use the newest transcript in the pane's
//     cwd project dir — but ONLY when it's unambiguous: we never take a file
//     that is another running pane's LIVE session, and we bail (empty → feed
//     fallback on the client) if TWO+ running Claude panes in the same cwd are
//     unresolved, since we can't tell which transcript is whose. This trades
//     coverage for zero mis-attribution (showing pane B's chat on pane A).
func (a *apiServer) transcriptForPane(p *Pane) string {
	// One scan of the transcript set; index by session id (skip subagents).
	byID := map[string]string{}
	all := listTranscripts(claudeConfigDirs())
	for _, path := range all {
		if isSubagentTranscript(path) {
			continue
		}
		byID[sessionIDForTranscript(path)] = path
	}

	snap := p.Snapshot()

	// (0) Authoritative: the MAIN transcript path the hook pinned. Survives
	// session-id rotation and is never a subagent/workflow orphan. Guard the
	// shape (defence-in-depth) and confirm the file still exists.
	if tp := strings.TrimSpace(snap.ClaudeTranscriptPath); tp != "" && !isSubagentTranscript(tp) {
		if fi, err := os.Stat(tp); err == nil && !fi.IsDir() {
			return tp
		}
	}

	if snap.ClaudeSessionID != "" {
		if path, ok := byID[snap.ClaudeSessionID]; ok {
			return path
		}
	}

	cwd := strings.TrimSpace(snap.Cwd)
	if cwd == "" {
		return ""
	}
	encDir := claudeProjectDirName(cwd)

	// Sessions already CLAIMED by a running pane whose id resolves (never steal
	// a live session), and how many running Claude panes in THIS cwd are also
	// unresolved (≥2 → ambiguous → bail).
	claimed := map[string]bool{}
	unresolvedInCwd := 0
	for _, q := range a.mgr.List() {
		if q.Status != PaneRunning {
			continue
		}
		qs := q.Snapshot()
		if qs.ClaudeSessionID == "" && !q.HasClaudeDescendant() {
			continue // not a Claude pane
		}
		if qs.ClaudeSessionID != "" {
			if _, ok := byID[qs.ClaudeSessionID]; ok {
				claimed[qs.ClaudeSessionID] = true
				continue
			}
		}
		if claudeProjectDirName(strings.TrimSpace(qs.Cwd)) == encDir {
			unresolvedInCwd++
		}
	}
	if unresolvedInCwd > 1 {
		return ""
	}

	// Newest unclaimed transcript in the pane's cwd project dir.
	best := ""
	var bestMod time.Time
	for _, path := range all {
		if isSubagentTranscript(path) {
			continue
		}
		if filepath.Base(filepath.Dir(path)) != encDir {
			continue
		}
		if claimed[sessionIDForTranscript(path)] {
			continue
		}
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		if best == "" || fi.ModTime().After(bestMod) {
			best = path
			bestMod = fi.ModTime()
		}
	}
	return best
}

// tailReadBounded reads at most maxBytes from the END of the file at path. If
// the file is larger than maxBytes it seeks to file_size-maxBytes (so we keep
// the newest turns) and drops the first (now-partial) line so every line we
// hand back is complete. Never io.ReadAll's the whole file. Returns the bytes
// to project over.
func tailReadBounded(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := fi.Size()

	start := int64(0)
	trimPartial := false
	if size > maxBytes {
		start = size - maxBytes
		trimPartial = true // we likely landed mid-line
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return nil, err
		}
	}

	// LimitReader is belt-and-suspenders: even though we only seek when the
	// file is large, this guarantees we never read more than maxBytes.
	buf, err := io.ReadAll(io.LimitReader(f, maxBytes))
	if err != nil {
		return nil, err
	}

	if trimPartial {
		if nl := bytes.IndexByte(buf, '\n'); nl >= 0 {
			buf = buf[nl+1:]
		} else {
			// One line filled the whole window — no complete line to keep.
			buf = nil
		}
	}
	return buf, nil
}

// projectConversation parses the complete JSONL lines in buf, projects each
// relevant one to a convTurn, and returns the LAST `limit` turns (newest kept)
// plus the conversation's ai-title (if any was seen in this window).
//
// This is a pure function (no IO, no daemon state) so it is unit-testable over
// synthetic fixtures. It is also the defensive core: a malformed line is
// skipped, never fatal.
//
// Kept / skipped per line:
//   - ai-title line                     → captured as the returned title (not a turn)
//   - user, content STRING (real prompt)→ {user, text}   (meta/command-wrapper skipped)
//   - user, content LIST (tool_result)  → SKIP (tool output, not the human)
//   - assistant text block              → {assistant, text}
//   - assistant thinking block          → SKIP (never surface chain-of-thought)
//   - assistant tool_use block          → {assistant, tool_use, redacted hint}
//   - any isSidechain line              → SKIP (subagent noise off the main thread)
func projectConversation(buf []byte, limit int) (turns []convTurn, aiTitle string) {
	turns = []convTurn{}
	for _, raw := range bytes.Split(buf, []byte("\n")) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var ln convLine
		if err := json.Unmarshal(raw, &ln); err != nil {
			continue // malformed / partial line — skip, never fatal
		}

		// Claude's generated conversation title. Last one in the window wins
		// (the title is rewritten as the conversation evolves).
		if ln.Type == "ai-title" {
			if ln.AITitle != "" {
				aiTitle = ln.AITitle
			}
			continue
		}

		// Subagent / sidechain turns pollute the main thread — drop them.
		if ln.IsSidechain {
			continue
		}
		if ln.Message == nil {
			continue
		}

		switch ln.Type {
		case "user":
			text, ok := userPromptText(&ln)
			if !ok {
				continue // tool_result-only, meta, or command-wrapper line
			}
			turns = append(turns, convTurn{
				Role: "user", Kind: "text", Text: text, Ts: ln.Timestamp,
			})
		case "assistant":
			turns = append(turns, assistantTurns(&ln)...)
		}
	}

	// Keep only the newest `limit` turns.
	if limit > 0 && len(turns) > limit {
		turns = turns[len(turns)-limit:]
	}
	return turns, aiTitle
}

// latestAITitle scans a transcript tail for the most recent ai-title line and
// returns it ("" if none). Lighter than projectConversation — it builds no
// turns, decoding only what the title scan needs. Last one in the window wins,
// mirroring projectConversation (the title is rewritten as the convo evolves).
func latestAITitle(buf []byte) string {
	title := ""
	for _, raw := range bytes.Split(buf, []byte("\n")) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var ln convLine
		if err := json.Unmarshal(raw, &ln); err != nil {
			continue
		}
		if ln.Type == "ai-title" && ln.AITitle != "" {
			title = ln.AITitle
		}
	}
	return title
}

// refreshPaneAITitleCache re-reads the pane's transcript tail and warms the
// in-memory ai-title cache the bulk /api/panes DTO reads. Called strictly OFF
// the request path (the activity watcher, on turn completion) so the pane list
// never pays transcript I/O. Best-effort: a missing/unreadable transcript or an
// absent ai-title leaves the existing cache untouched.
func (a *apiServer) refreshPaneAITitleCache(p *Pane) {
	if p == nil {
		return
	}
	path := a.transcriptForPane(p)
	if path == "" {
		return
	}
	buf, err := tailReadBounded(path, convTailWindowBytes)
	if err != nil {
		return
	}
	p.setAITitleCache(latestAITitle(buf))
}

// userPromptText extracts the real human prompt from a user line. It returns
// ok=false (skip) when the line is not a genuine typed prompt:
//   - isMeta lines (system-injected user turns),
//   - content that is a LIST (tool_result blocks — tool output, not the human),
//   - command-wrapper stubs the CLI writes as user content
//     (<command-name>, <command-message>, <local-command-...>).
func userPromptText(ln *convLine) (string, bool) {
	if ln.IsMeta {
		return "", false
	}
	if len(ln.Message.Content) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(ln.Message.Content, &s); err != nil {
		// Not a string → it's a content LIST (tool_result). Skip.
		return "", false
	}
	if s == "" {
		return "", false
	}
	// Command-wrapper / caveat stubs the CLI injects as user content — not a
	// human prompt. Cheap prefix check after trimming leading whitespace.
	trimmed := s
	for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\n' || trimmed[0] == '\t' || trimmed[0] == '\r') {
		trimmed = trimmed[1:]
	}
	if hasAnyPrefix(trimmed, "<command-name>", "<command-message>", "<command-args>", "<local-command-", "<command-stdout>", "<command-stderr>") {
		return "", false
	}
	return s, true
}

// assistantTurns projects an assistant line's content list into zero or more
// turns: text blocks become text turns, tool_use blocks become redacted
// tool_use turns, thinking blocks are dropped.
func assistantTurns(ln *convLine) []convTurn {
	var blocks []convBlock
	if err := json.Unmarshal(ln.Message.Content, &blocks); err != nil {
		return nil // non-list assistant content (unexpected) — skip safely
	}
	var out []convTurn
	for i := range blocks {
		b := &blocks[i]
		switch b.Type {
		case "text":
			if b.Text == "" {
				continue
			}
			out = append(out, convTurn{
				Role: "assistant", Kind: "text", Text: b.Text, Ts: ln.Timestamp,
			})
		case "tool_use":
			// Redact through the SAME path the Activity feed uses so the
			// conversation never leaks more than the feed (basename-only
			// paths, secret-shaped Bash → "(redacted)", URL → host).
			out = append(out, convTurn{
				Role: "assistant", Kind: "tool_use",
				Tool:     b.Name,
				ToolHint: redactHookHint(b.Name, b.Input),
				Ts:       ln.Timestamp,
			})
		case "thinking":
			// Dropped entirely — never surface chain-of-thought.
		}
	}
	return out
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if len(s) >= len(p) && s[:len(p)] == p {
			return true
		}
	}
	return false
}
