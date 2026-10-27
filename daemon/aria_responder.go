package main

// Aria agent responder — answer @mentions of the user's Aria comms-gateway
// agent keys from THIS machine, on the user's own Claude subscription.
//
// The hosted alternative (Aria's gateway answers mentions itself with an
// API-billed model) needs nothing local; this mode exists for users who'd
// rather spend their Claude plan and get their full local Claude — with
// whatever user-scoped MCP servers and context their sessions have — as the
// brain behind the agent.
//
// Multi-agent: the settings hold a LIST of agents (Aria allows up to ten
// keys per account, each its own @handle/persona/mode). A supervisor
// (AriaResponder) reconciles one ariaAgentRunner per enabled agent; each
// runner owns its poll loop, pane queue, and status. Per runner:
//
//   long-poll {gateway}/v1/wait (Bearer agent key; held ≤25s server-side,
//   answered the instant a readable chat gets a message)
//     → messages with mentioned=true?
//     → target pane set? inject into that LIVE session (full toolset); if
//       it's mid-task, QUEUE and deliver when it frees up — never a silent
//       downgrade to headless for a merely-busy pane.
//     → otherwise (or pane gone / mention waited 30 min): headless `claude
//       -p` run (task-schedule machinery: resolveClaudeBin, detached child
//       tree, bounded log, hard timeout), with the gateway wired in via a
//       0600 --mcp-config temp file so the run needs NO prior `claude mcp
//       add`, and --allowedTools scoped to ONLY those tools — the reply
//       goes out through the gateway's normal grant/cap/journal pipeline as
//       the agent. When headless is covering for a configured pane, its
//       reply discloses that it's the limited fallback.
//
// ⚠️ Don't enable this AND a key's hosted auto-reply together: both consume
// the key's exactly-once cursor, so replies would race (never duplicate, but
// which brain answers becomes a coin toss). The gateway 409s wait/poll while
// the Aria-side mode is 'hosted'; the runner stands by unless it's 'self'.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	ariaResponderRunTimeout  = 5 * time.Minute
	ariaResponderWaitTimeout = 40 * time.Second // server holds ≤25s
	ariaResponderBackoffMax  = 60 * time.Second
	ariaResponderAuthBackoff = 5 * time.Minute // bad key: don't hammer

	// Pane queue: a busy target pane QUEUES mentions instead of silently
	// falling back to the headless run — task-style mentions ("run /pr
	// triage") only work with the pane's full toolset, so a short wait beats
	// a fast-but-incapable answer. The cap bounds how long a sender can be
	// left hanging before the limited fallback answers (and says it's the
	// fallback).
	ariaPaneQueuePoll    = 3 * time.Second
	ariaPaneQueueMaxWait = 30 * time.Minute
	ariaPaneQueueMaxLen  = 200
)

// Disclosure reasons for headless runs that cover for the pane. Empty reason
// = headless was the CHOSEN mode, nothing to disclose.
const (
	ariaFallbackPaneGone  = "that session is gone (its pane was closed)"
	ariaFallbackPaneStuck = "that session has been busy for over 30 minutes"
)

var ariaAgentKeyRe = regexp.MustCompile(`^ariak_[0-9a-f]{48}$`)

// ariaAgentConfig is ONE agent identity: an Aria key plus how this machine
// answers for it. Immutable inside a runner — edits restart the runner via
// the supervisor's reconcile (the persisted queue survives the swap).
type ariaAgentConfig struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	// AgentKey is the Aria comms-gateway key (ariak_…). Stored plaintext in
	// dataDir like the daemon's other local credentials; GET masks it.
	AgentKey   string `json:"agent_key"`
	GatewayURL string `json:"gateway_url,omitempty"` // default hosted gateway
	Permission string `json:"permission,omitempty"`  // task preset: plan|edit (default plan)
	Model      string `json:"model,omitempty"`       // optional --model override
	// PaneID: deliver mentions INTO this live pane (its session context +
	// its own MCP config answer) instead of a fresh headless run. A busy
	// pane queues mentions until it's free; only a GONE pane (or a mention
	// waiting past ariaPaneQueueMaxWait) is covered by a headless run, which
	// then discloses it's the limited fallback. "" = always headless.
	PaneID string `json:"pane_id,omitempty"`
	// Handle is the key's @mention handle, cached from the save-time
	// /v1/info verify so the UI can name the agent without a live gateway.
	Handle string `json:"handle,omitempty"`
	// McpName is this agent's user-scope MCP server registration name. The
	// first agent keeps the historical "aria-comms" (running sessions
	// depend on it); later agents get "aria-comms-<handle>". Stable once
	// assigned.
	McpName string `json:"mcp_name,omitempty"`
}

func (s ariaAgentConfig) gateway() string {
	if v := strings.TrimRight(strings.TrimSpace(s.GatewayURL), "/"); v != "" {
		return v
	}
	return ariaResponderGatewayDefault
}

func (s ariaAgentConfig) permission() string {
	if validTaskPermission(s.Permission) {
		return s.Permission
	}
	return "plan"
}

func (s ariaAgentConfig) mcpName() string {
	if s.McpName != "" {
		return s.McpName
	}
	return "aria-comms"
}

// runnable = the supervisor should have a live runner for it.
func (s ariaAgentConfig) runnable() bool {
	return s.Enabled && ariaAgentKeyRe.MatchString(s.AgentKey)
}

// legacyAgentConfigured reports whether a v1 settings blob held any
// meaningful configuration worth migrating (not a bare/default file).
func legacyAgentConfigured(a ariaAgentConfig) bool {
	return a.Enabled || a.AgentKey != "" || a.GatewayURL != "" ||
		a.PaneID != "" || a.Permission != "" || a.Model != "" || a.Handle != ""
}

const ariaDefaultAgentID = "agent_default"

func newAriaAgentID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("agent_%d", time.Now().UnixNano())
	}
	return "agent_" + hex.EncodeToString(b)
}

type ariaResponderConfig struct {
	Agents []ariaAgentConfig `json:"agents"`
}

func (c ariaResponderConfig) find(id string) *ariaAgentConfig {
	for i := range c.Agents {
		if c.Agents[i].ID == id {
			return &c.Agents[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// settings store
// ---------------------------------------------------------------------------

type ariaResponderStore struct {
	path string

	mu      sync.Mutex
	s       ariaResponderConfig
	changed chan struct{} // buffered(1); poked on every set()
}

func newAriaResponderStore(dataDir string) (*ariaResponderStore, error) {
	st := &ariaResponderStore{
		path:    filepath.Join(dataDir, "aria_responder.json"),
		changed: make(chan struct{}, 1),
	}
	b, err := os.ReadFile(st.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return st, nil
		}
		return nil, fmt.Errorf("read aria responder settings: %w", err)
	}
	var loaded ariaResponderConfig
	if err := json.Unmarshal(b, &loaded); err != nil {
		return nil, fmt.Errorf("parse aria responder settings: %w", err)
	}
	if len(loaded.Agents) == 0 {
		// v1 file: one agent's fields at the top level. Wrap it as the
		// default agent whenever ANY field was configured — not just key or
		// enabled — so a user who set a custom gateway/pane/permission but
		// hadn't pasted a key yet doesn't lose it on upgrade. Its queue file
		// moves to the per-agent name so a restart mid-queue still resumes.
		var legacy ariaAgentConfig
		if err := json.Unmarshal(b, &legacy); err == nil && legacyAgentConfigured(legacy) {
			legacy.ID = ariaDefaultAgentID
			legacy.McpName = "aria-comms"
			loaded.Agents = []ariaAgentConfig{legacy}
			oldQ := filepath.Join(dataDir, "aria_queue.json")
			if _, err := os.Stat(oldQ); err == nil {
				_ = os.Rename(oldQ, ariaQueuePath(dataDir, ariaDefaultAgentID))
			}
		}
	}
	st.s = loaded
	return st, nil
}

func (st *ariaResponderStore) get() ariaResponderConfig {
	st.mu.Lock()
	defer st.mu.Unlock()
	return ariaResponderConfig{Agents: append([]ariaAgentConfig(nil), st.s.Agents...)}
}

func (st *ariaResponderStore) set(s ariaResponderConfig) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(st.path, b, 0o600); err != nil {
		return err
	}
	st.s = s
	select {
	case st.changed <- struct{}{}:
	default:
	}
	return nil
}

// ---------------------------------------------------------------------------
// wire shapes from the gateway's /v1/wait (subset we consume)
// ---------------------------------------------------------------------------

type ariaWaitMessage struct {
	Sender    *string `json:"sender"`
	Text      string  `json:"text"`
	Timestamp string  `json:"timestamp"`
	Mentioned bool    `json:"mentioned"`
}

type ariaWaitUpdate struct {
	Source   string            `json:"source"`
	ChatID   string            `json:"chat_id"`
	ChatName *string           `json:"chat_name"`
	Messages []ariaWaitMessage `json:"messages"`
}

type ariaWaitResponse struct {
	OK            bool             `json:"ok"`
	MentionHandle string           `json:"mention_handle"`
	Updates       []ariaWaitUpdate `json:"updates"`
}

// ---------------------------------------------------------------------------
// supervisor — one runner per enabled agent
// ---------------------------------------------------------------------------

// AriaResponder reconciles the configured agents against live runners. It
// owns no gateway I/O itself; each ariaAgentRunner is single-flight by
// construction (its loop doesn't poll again until the current reply lands).
type AriaResponder struct {
	store   *ariaResponderStore
	uiBus   *uiBus
	mgr     *Manager
	dataDir string
	httpc   *http.Client
	// listenAddr is the daemon's own bind address; the MCP proxy URL panes
	// and headless runs point at is derived from its port (aria_mcp_proxy.go).
	// Set once at boot before Run(); "" falls back to direct-gateway configs.
	listenAddr string

	mu      sync.Mutex
	runners map[string]*ariaAgentRunner
}

func newAriaResponder(store *ariaResponderStore, bus *uiBus, mgr *Manager, dataDir string) *AriaResponder {
	return &AriaResponder{
		store:   store,
		uiBus:   bus,
		mgr:     mgr,
		dataDir: dataDir,
		httpc:   &http.Client{Timeout: ariaResponderWaitTimeout},
		runners: map[string]*ariaAgentRunner{},
	}
}

func (r *AriaResponder) Run() {
	// One-time: move existing installs' key-bearing MCP registrations onto
	// the keyless loopback proxy (aria_mcp_proxy.go).
	r.migrateMcpToProxy()
	for {
		r.reconcile()
		<-r.store.changed
	}
}

// reconcile diffs desired agents against live runners: unchanged configs are
// left alone, edits restart the runner (its persisted queue carries over),
// disables/removals stop it and tell any queued chats instead of ghosting
// them. In-flight replies are never killed — a stop only ends polling.
func (r *AriaResponder) reconcile() {
	cfg := r.store.get()
	desired := map[string]ariaAgentConfig{}
	for _, a := range cfg.Agents {
		if a.runnable() {
			desired[a.ID] = a
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, run := range r.runners {
		want, ok := desired[id]
		if ok && run.cfg == want {
			continue
		}
		// stop() (not bare cancel) waits for the flusher to release the queue
		// file, so the replacement started below — or the discard path — sees
		// a settled file and can't re-answer a mid-delivered batch.
		run.stop()
		delete(r.runners, id)
		if !ok {
			log.Printf("aria-responder: agent %s (@%s) stopped", id, run.cfg.Handle)
			go r.discardQueueWithNotice(run.cfg)
		}
	}
	for id, want := range desired {
		if _, ok := r.runners[id]; ok {
			continue
		}
		run := newAriaAgentRunner(want, r.uiBus, r.mgr, r.dataDir, r.httpc)
		run.listenAddr = r.listenAddr
		r.runners[id] = run
		log.Printf("aria-responder: agent %s (@%s) starting", id, want.Handle)
		go run.run()
	}
}

// discardQueueWithNotice drains a stopped agent's persisted queue: the
// gateway cursor is already past those mentions, so they can only drop —
// send the affected chats one honest line first.
func (r *AriaResponder) discardQueueWithNotice(cfg ariaAgentConfig) {
	path := ariaQueuePath(r.dataDir, cfg.ID)
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	_ = os.Remove(path)
	var f ariaQueueFile
	if err := json.Unmarshal(b, &f); err != nil || len(f.Mentions) == 0 {
		return
	}
	log.Printf("aria-responder: %d queued mention(s) dropped — agent %s disabled", len(f.Mentions), cfg.ID)
	if !ariaAgentKeyRe.MatchString(cfg.AgentKey) {
		return
	}
	for _, m := range distinctChats(mentionsOf(f.Mentions)) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = ariaGatewaySend(ctx, r.httpc, cfg.gateway(), cfg.AgentKey, m.Source, m.ChatID,
			"My owner switched off my responder on their computer before I could answer — sorry! Ping me again later.")
		cancel()
	}
}

// SendAsAgent posts text into a chat as one of the configured agents, via
// the gateway's REST /v1/send. agentID "" selects the first configured
// agent. This is the ONE door for other daemon subsystems (schedule report
// delivery) to speak as an agent — it keeps ariaAgentConfig's shape private
// so a change to how an agent authenticates never has to be chased through
// schedules code. Counts against the key's daily send cap; journaled
// server-side.
func (r *AriaResponder) SendAsAgent(ctx context.Context, agentID, source, chatID, text string) error {
	cfg := r.store.get()
	var agent *ariaAgentConfig
	if agentID == "" {
		if len(cfg.Agents) > 0 {
			agent = &cfg.Agents[0]
		}
	} else {
		agent = cfg.find(agentID)
	}
	if agent == nil || !ariaAgentKeyRe.MatchString(agent.AgentKey) {
		return errors.New("no Aria agent key on file — paste one in Settings → Aria agent")
	}
	return ariaGatewaySend(ctx, r.httpc, agent.gateway(), agent.AgentKey, source, chatID, text)
}

// ListAgentChats fans out /v1/info for every keyed agent (parallel; grants
// change gateway-side so there's no cache) and returns each agent's granted
// chats — the schedule dialog's chat-picker source.
func (r *AriaResponder) ListAgentChats(ctx context.Context) []ariaAgentChatsDTO {
	cfg := r.store.get()
	results := make([]ariaAgentChatsDTO, len(cfg.Agents))
	var wg sync.WaitGroup
	for i, ag := range cfg.Agents {
		if !ariaAgentKeyRe.MatchString(ag.AgentKey) {
			continue
		}
		wg.Add(1)
		go func(i int, ag ariaAgentConfig) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			info, _, err := ariaGatewayInfoCall(cctx, r.httpc, ag.gateway(), ag.AgentKey)
			d := ariaAgentChatsDTO{ID: ag.ID, Handle: ag.Handle, Chats: []ariaGatewayChat{}}
			if err != nil {
				d.Error = err.Error()
			} else {
				d.Handle = info.Handle
				d.Chats = info.Chats
			}
			results[i] = d
		}(i, ag)
	}
	wg.Wait()
	out := make([]ariaAgentChatsDTO, 0, len(results))
	for _, d := range results {
		if d.ID != "" {
			out = append(out, d)
		}
	}
	return out
}

type ariaAgentChatsDTO struct {
	ID     string            `json:"id"`
	Handle string            `json:"handle,omitempty"`
	Chats  []ariaGatewayChat `json:"chats"`
	Error  string            `json:"error,omitempty"`
}

// statusFor reports an agent's live status, or a static "disabled" for one
// with no runner.
func (r *AriaResponder) statusFor(id string) *ariaResponderStatusDTO {
	r.mu.Lock()
	run, ok := r.runners[id]
	r.mu.Unlock()
	if ok {
		return run.statusDTO()
	}
	return &ariaResponderStatusDTO{State: "disabled", Activity: []ariaActivity{}}
}

// ---------------------------------------------------------------------------
// per-agent runner
// ---------------------------------------------------------------------------

type ariaAgentRunner struct {
	cfg        ariaAgentConfig
	uiBus      *uiBus
	mgr        *Manager
	dataDir    string
	httpc      *http.Client
	listenAddr string // daemon bind addr → keyless MCP proxy URL ("" = direct)
	ctx        context.Context
	cancel     context.CancelFunc

	// Pane queue — mentions waiting for the busy target pane. The gateway
	// cursor has already advanced past these (wait() consumed them), so the
	// slice + its write-through file (aria_queue_<id>.json) are the only
	// copies; a restart (daemon or runner) resumes the queue from disk.
	// Guarded by queueMu; flusherOn is true while exactly one
	// flushQueueWhenPaneFree goroutine is alive.
	queueMu      sync.Mutex
	queue        []ariaQueuedMention
	queueHandle  string
	queuePersona string
	flusherOn    bool
	// flusherWG tracks the live flusher goroutine so stop() can wait for it
	// to RELEASE the persisted queue file before the supervisor hands that
	// file to a replacement runner (or the discard-notice path) — without
	// this barrier, a successor's resumeQueue could re-read a batch the old
	// flusher was mid-delivering and answer it twice.
	flusherWG sync.WaitGroup

	// Live status for the settings UI — the loop already knows all of this;
	// these fields just stop it from being log-only. statusMu is leaf-level:
	// never held while taking queueMu or doing I/O.
	statusMu    sync.Mutex
	stState     string // standby|listening|auth-failed|gateway-down
	stMode      string
	stHandle    string
	stLastErr   string
	stLastPoll  time.Time
	stLastReply time.Time
	activity    []ariaActivity // newest-first, capped
}

func newAriaAgentRunner(cfg ariaAgentConfig, bus *uiBus, mgr *Manager, dataDir string, httpc *http.Client) *ariaAgentRunner {
	ctx, cancel := context.WithCancel(context.Background())
	return &ariaAgentRunner{
		cfg:     cfg,
		uiBus:   bus,
		mgr:     mgr,
		dataDir: dataDir,
		httpc:   httpc,
		ctx:     ctx,
		cancel:  cancel,
		// Honest until the first poll returns — the UI shows "Starting…"
		// instead of a green "listening" that may actually be mid-dial.
		stState: "starting",
	}
}

// stop cancels the runner's polling and waits for its queue flusher to
// finish its current tick and release the persisted queue file. Bounded:
// the flusher honors ctx at each poll tick and its long headless fallbacks
// run detached, so this returns within one delivery tick — never a whole
// reply. In-flight replies themselves are never killed (a stop only ends
// polling + the flush loop).
func (r *ariaAgentRunner) stop() {
	r.cancel()
	r.flusherWG.Wait()
}

// startFlusher launches the single queue flusher, tracked so stop() can wait
// for it. Callers hold queueMu and have just set flusherOn.
func (r *ariaAgentRunner) startFlusher() {
	r.flusherWG.Add(1)
	go func() {
		defer r.flusherWG.Done()
		r.flushQueueWhenPaneFree()
	}()
}

// ariaQueuedMention stamps a queued mention with its arrival time so the
// 30-minute wait cap is per MENTION, not per flusher tenure — a mention
// that joined a long-lived queue at minute 29 still gets its full wait.
type ariaQueuedMention struct {
	ariaMention
	EnqueuedAt time.Time `json:"enqueued_at"`
}

const ariaActivityCap = 20

// ariaActivity is one owner-visible event in the responder's recent life —
// the mitigation for "typing-then-silence is invisible": every mention's
// outcome (pane, headless, queued, dropped, notice) lands here.
type ariaActivity struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"` // mention|pane|headless|fallback|queued|dropped|notice|error
	Chat   string    `json:"chat,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

func (r *ariaAgentRunner) noteState(state, errMsg string) {
	r.statusMu.Lock()
	r.stState, r.stLastErr = state, errMsg
	r.statusMu.Unlock()
}

func (r *ariaAgentRunner) noteMode(mode string) {
	r.statusMu.Lock()
	r.stMode = mode
	r.statusMu.Unlock()
}

func (r *ariaAgentRunner) notePoll(handle string) {
	r.statusMu.Lock()
	r.stState, r.stLastErr, r.stLastPoll = "listening", "", time.Now()
	if handle != "" {
		r.stHandle = handle
	}
	r.statusMu.Unlock()
}

func (r *ariaAgentRunner) noteReply() {
	r.statusMu.Lock()
	r.stLastReply = time.Now()
	r.statusMu.Unlock()
}

func (r *ariaAgentRunner) recordActivity(kind, chat, detail string) {
	r.statusMu.Lock()
	r.activity = append([]ariaActivity{{At: time.Now(), Kind: kind, Chat: chat, Detail: detail}}, r.activity...)
	if len(r.activity) > ariaActivityCap {
		r.activity = r.activity[:ariaActivityCap]
	}
	r.statusMu.Unlock()
}

// chatLabel names a chat for activity rows/logs: prefer the human name.
func chatLabel(source, chatID string, chatName *string) string {
	if chatName != nil && strings.TrimSpace(*chatName) != "" {
		return source + "/" + strings.TrimSpace(*chatName)
	}
	return source + "/" + chatID
}

func batchChats(mentions []ariaMention) string {
	seen := map[string]bool{}
	var out []string
	for _, m := range mentions {
		l := chatLabel(m.Source, m.ChatID, m.ChatName)
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return strings.Join(out, ", ")
}

// gatewayMode asks the gateway which answering mode the key is in (Aria →
// Settings → Agents owns this) plus the owner's persona instructions, so the
// agent behaves identically whoever answers. Only "self" means this machine
// answers. The HTTP status rides along so the caller's backoff can tell a
// rejected key from an unreachable gateway.
func (r *ariaAgentRunner) gatewayMode() (mode, persona string, status int, err error) {
	ctx, cancel := context.WithTimeout(r.ctx, 15*time.Second)
	defer cancel()
	info, status, err := ariaGatewayInfoCall(ctx, r.httpc, r.cfg.gateway(), r.cfg.AgentKey)
	if err != nil {
		return "", "", status, err
	}
	// Pre-mode gateways have no responder_mode field: treat as self (the old
	// contract — whoever polls, answers).
	if info.Mode == "" {
		return "self", info.Persona, status, nil
	}
	return info.Mode, info.Persona, status, nil
}

// ariaBackoff maps an error streak + HTTP status to the retry delay — ONE
// ladder for every gateway call, so a bad key can't dodge the 5-minute auth
// backoff by failing at the mode-check instead of the wait (the mode-check
// path used to retry a flat 30s forever).
func ariaBackoff(streak, status int) time.Duration {
	b := time.Duration(streak) * 5 * time.Second
	if b > ariaResponderBackoffMax {
		b = ariaResponderBackoffMax
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		b = ariaResponderAuthBackoff
	case http.StatusConflict:
		// mode flipped away from 'self' mid-call — re-read it promptly
		b = 2 * time.Second
	}
	return b
}

// ariaErrState buckets a gateway failure for the status surface: an explicit
// key rejection is the actionable one; everything else is the gateway's
// problem, not the owner's.
func ariaErrState(status int) string {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return "auth-failed"
	}
	return "gateway-down"
}

// sleepOrDone pauses the loop, waking early only on shutdown.
func (r *ariaAgentRunner) sleepOrDone(d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-r.ctx.Done():
		return false
	}
}

func (r *ariaAgentRunner) run() {
	// Mentions queued for a busy pane survive restarts (the gateway cursor
	// is already past them — the queue file is the only copy).
	r.resumeQueue()
	errStreak := 0
	lastModeCheck := time.Time{}
	mode := ""
	persona := ""
	for r.ctx.Err() == nil {
		// The key's answering mode lives in Aria (Settings → Agents). Re-check
		// every minute so a flip there takes effect without touching soluterminal.
		if time.Since(lastModeCheck) > time.Minute {
			m, p, status, err := r.gatewayMode()
			if err != nil {
				if r.ctx.Err() != nil {
					return
				}
				errStreak++
				backoff := ariaBackoff(errStreak, status)
				r.noteState(ariaErrState(status), err.Error())
				log.Printf("aria-responder: mode check failed (streak %d, next in %s): %v", errStreak, backoff, err)
				if !r.sleepOrDone(backoff) {
					return
				}
				continue
			}
			errStreak = 0
			mode, persona, lastModeCheck = m, p, time.Now()
			r.noteMode(mode)
			if mode != "self" {
				log.Printf("aria-responder: key mode is %q — standing by (flip to 'My computer' in Aria → Settings → Agents)", mode)
			}
		}
		if mode != "self" {
			r.noteState("standby", "")
			if !r.sleepOrDone(time.Minute) {
				return
			}
			continue
		}
		out, status, err := r.wait()
		if err != nil {
			if r.ctx.Err() != nil {
				return
			}
			errStreak++
			backoff := ariaBackoff(errStreak, status)
			if status == http.StatusConflict {
				// mode flipped away from 'self' mid-wait — re-read it promptly
				lastModeCheck = time.Time{}
			}
			r.noteState(ariaErrState(status), err.Error())
			log.Printf("aria-responder: wait failed (streak %d, next in %s): %v", errStreak, backoff, err)
			if !r.sleepOrDone(backoff) {
				return
			}
			continue
		}
		errStreak = 0
		r.notePoll(out.MentionHandle)
		mentions := collectAriaMentions(out)
		if len(mentions) == 0 {
			continue
		}
		log.Printf("aria-responder: %d mention(s) of @%s — spawning claude", len(mentions), out.MentionHandle)
		r.recordActivity("mention", batchChats(mentions), fmt.Sprintf("%d mention(s) of @%s", len(mentions), out.MentionHandle))
		r.reply(out.MentionHandle, persona, mentions)
	}
}

func (r *ariaAgentRunner) wait() (*ariaWaitResponse, int, error) {
	ctx, cancel := context.WithTimeout(r.ctx, ariaResponderWaitTimeout)
	defer cancel()
	body, status, err := ariaGatewayPost(ctx, r.httpc, r.cfg.gateway()+"/v1/wait", r.cfg.AgentKey, "{}")
	if err != nil {
		return nil, status, err
	}
	var out ariaWaitResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, status, fmt.Errorf("parse wait response: %w", err)
	}
	return &out, status, nil
}

type ariaMention struct {
	Source   string  `json:"source"`
	ChatID   string  `json:"chat_id"`
	ChatName *string `json:"chat_name"`
	Sender   string  `json:"sender"`
	Text     string  `json:"text"`
	At       string  `json:"at"`
}

// signalTyping flips the gateway's typing indicator (POST /v1/typing) for
// every distinct chat in the batch — senders see "◆ handle is typing…" in
// Aria (and the native WA/TG bubble) while the reply is being worked on.
// Best-effort by design: an indicator must never delay or fail a reply.
func (r *ariaAgentRunner) signalTyping(mentions []ariaMention, on bool) {
	for _, m := range distinctChats(mentions) {
		body, err := json.Marshal(map[string]any{"source": m.Source, "chat_id": m.ChatID, "active": on})
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, _, _ = ariaGatewayPost(ctx, r.httpc, r.cfg.gateway()+"/v1/typing", r.cfg.AgentKey, string(body))
		cancel()
	}
}

// distinctChats returns one mention per (source, chat_id) — the dedup every
// per-chat fan-out (typing, notices) shares.
func distinctChats(mentions []ariaMention) []ariaMention {
	seen := map[string]bool{}
	var out []ariaMention
	for _, m := range mentions {
		k := m.Source + "\x00" + m.ChatID
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, m)
	}
	return out
}

// keepTyping starts the indicator and refreshes it every 8s (the gateway
// stamp lives ~15s) until the returned stop func runs, which also clears it.
// Safe to call the stop func more than once.
func (r *ariaAgentRunner) keepTyping(mentions []ariaMention) func() {
	r.signalTyping(mentions, true)
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(8 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				r.signalTyping(mentions, true)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			r.signalTyping(mentions, false)
		})
	}
}

// typingWhilePaneWorks keeps the indicator alive after a pane injection: the
// pane answers asynchronously, so refresh while it's mid-task and stop once
// it idles again (its gateway send clears the indicator server-side too) or
// after a hard cap matching the queue's own patience. The grace window
// covers the moment right after injection when the pane hasn't flipped to
// busy yet. If the turn parks on a permission prompt and stays there — the
// auto-approver resolves most within seconds, so one sighting proves
// nothing — the sender gets one honest notice instead of typing-then-
// silence; the owner's push comes from the attention engine as usual.
func (r *ariaAgentRunner) typingWhilePaneWorks(paneID string, mentions []ariaMention) {
	deadline := time.Now().Add(ariaPaneQueueMaxWait)
	grace := time.Now().Add(20 * time.Second)
	choiceStreak := 0
	disclosed := false
	t := time.NewTicker(8 * time.Second)
	defer t.Stop()
	for range t.C {
		if time.Now().After(deadline) || r.mgr == nil {
			break
		}
		pane, ok := r.mgr.Get(paneID)
		if !ok {
			break
		}
		state := pane.computeState()
		if state == StateChoicePrompt {
			choiceStreak++
			if choiceStreak == 2 && !disclosed {
				disclosed = true
				r.notifyChats(mentions,
					"A step in my answer needs my owner's approval — they've been pinged; this may take a little while.")
			}
		} else {
			choiceStreak = 0
		}
		busy := !isFireReadyState(state, pane.Snapshot().Status)
		if !busy && time.Now().After(grace) {
			break
		}
		r.signalTyping(mentions, true)
	}
	r.signalTyping(mentions, false)
}

func collectAriaMentions(out *ariaWaitResponse) []ariaMention {
	var mentions []ariaMention
	for _, u := range out.Updates {
		for _, m := range u.Messages {
			if !m.Mentioned {
				continue
			}
			sender := "someone"
			if m.Sender != nil && *m.Sender != "" {
				sender = *m.Sender
			}
			mentions = append(mentions, ariaMention{
				Source: u.Source, ChatID: u.ChatID, ChatName: u.ChatName,
				Sender: sender, Text: m.Text, At: m.Timestamp,
			})
		}
	}
	return mentions
}

// reply answers the collected mentions. With a target pane configured it
// hands them to that LIVE session (its accumulated context — and its own MCP
// config, so the pane needs this agent's MCP registered user-scope — does
// the answering). A busy pane QUEUES the mentions for delivery when it's
// free; only a gone pane (or a mention past the wait cap) is covered by a
// headless run, which discloses in-chat that it's the limited fallback.
func (r *ariaAgentRunner) reply(handle, persona string, mentions []ariaMention) {
	if r.cfg.PaneID == "" {
		r.replyHeadless(handle, persona, mentions, "")
		return
	}
	// Order guard: while earlier mentions sit in the queue, later ones join
	// the line — jumping straight to the pane would answer out of order.
	if r.queueActive() {
		r.enqueueForPane(handle, persona, mentions)
		return
	}
	switch r.deliverToPane(r.cfg.PaneID, handle, persona, mentions) {
	case paneDelivered:
		// The pane answers asynchronously — keep the sender's typing
		// indicator alive while it works. Async: a slow /v1/typing must
		// never stall the poll loop.
		go r.signalTyping(mentions, true)
		go r.typingWhilePaneWorks(r.cfg.PaneID, mentions)
	case paneBusy:
		r.enqueueForPane(handle, persona, mentions)
	case paneGone:
		r.replyHeadless(handle, persona, mentions, ariaFallbackPaneGone)
	}
}

// paneDeliver is deliverToPane's outcome: delivered, worth-retrying-later
// (pane exists but is mid-task), or not-coming-back (pane missing/exited/
// write-broken — retrying can't help).
type paneDeliver int

const (
	paneDelivered paneDeliver = iota
	paneBusy
	paneGone
)

// deliverToPane injects a reply request into the target pane, mirroring the
// prompt-schedule injection path (schedules_runner.go).
func (r *ariaAgentRunner) deliverToPane(paneID, handle, persona string, mentions []ariaMention) paneDeliver {
	if r.mgr == nil {
		return paneGone
	}
	pane, ok := r.mgr.Get(paneID)
	if !ok {
		log.Printf("aria-responder: target pane %s is gone", paneID)
		return paneGone
	}
	snap := pane.Snapshot()
	state := pane.computeState()
	if snap.Status == PaneExited || state == StateExited {
		log.Printf("aria-responder: target pane %s has exited", paneID)
		return paneGone
	}
	if !isFireReadyState(state, snap.Status) {
		log.Printf("aria-responder: pane %s is mid-task (state=%s)", paneID, state)
		return paneBusy
	}
	compact, err := json.Marshal(mentions)
	if err != nil {
		return paneGone
	}
	// Single line on purpose: raw multi-line PTY writes can submit early in
	// terminal TUIs (see the schedule-injection comments).
	personaLine := ""
	if persona != "" {
		personaLine = " Owner's instructions for you: " + strings.Join(strings.Fields(persona), " ") + "."
	}
	body := fmt.Sprintf(
		"Aria: you are the chat agent @%s and these messages just mentioned you: %s — "+
			"reply to each via the %s send_message tool (matching source + chat_id). "+
			"Reply directly when the message itself is enough; call read_messages only if you truly "+
			"need more of the conversation. Short, conversational; never write @%s yourself.%s",
		handle, string(compact), r.cfg.mcpName(), handle, personaLine)

	if snap.Kind == "native" {
		if _, err := pane.submitNativeInput(body); err != nil {
			log.Printf("aria-responder: native submit failed (%v)", err)
			return paneGone
		}
	} else if _, err := submitLineToPane(pane, body, pane.HasClaudeDescendant()); errors.Is(err, errDeliveryLeftInComposer) {
		// Delivered to the pane but not submitted: replying headless as well
		// would answer twice once someone presses Enter.
		log.Printf("aria-responder: mention left in pane %s's input box, not submitted", paneID)
	} else if err != nil {
		log.Printf("aria-responder: pane delivery failed (%v)", err)
		return paneGone
	}
	log.Printf("aria-responder: %d mention(s) delivered to pane %s", len(mentions), displayTitle(pane))
	r.noteReply()
	r.recordActivity("pane", batchChats(mentions), fmt.Sprintf("%d mention(s) → %s", len(mentions), displayTitle(pane)))
	if r.uiBus != nil {
		r.uiBus.publish(uiEvent{
			Type:       "toast",
			Text:       fmt.Sprintf("🤖 @%s mention → %s", handle, displayTitle(pane)),
			SkipRecent: true,
		})
	}
	return paneDelivered
}

// ---------------------------------------------------------------------------
// pane queue — busy pane holds the line instead of silently downgrading
// ---------------------------------------------------------------------------

func (r *ariaAgentRunner) queueActive() bool {
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	return r.flusherOn
}

func (r *ariaAgentRunner) enqueueForPane(handle, persona string, mentions []ariaMention) {
	now := time.Now()
	r.queueMu.Lock()
	for _, m := range mentions {
		r.queue = append(r.queue, ariaQueuedMention{ariaMention: m, EnqueuedAt: now})
	}
	if over := len(r.queue) - ariaPaneQueueMaxLen; over > 0 {
		// Cap overflow drops the OLDEST — log which chats lose messages so
		// the silence is at least diagnosable.
		dropped := map[string]int{}
		for _, q := range r.queue[:over] {
			dropped[q.Source+"/"+q.ChatID]++
		}
		log.Printf("aria-responder: queue over %d — dropped %d oldest mention(s): %v", ariaPaneQueueMaxLen, over, dropped)
		r.recordActivity("dropped", batchChats(mentionsOf(r.queue[:over])), fmt.Sprintf("queue over %d — %d oldest dropped", ariaPaneQueueMaxLen, over))
		r.queue = append([]ariaQueuedMention(nil), r.queue[over:]...)
	}
	r.queueHandle, r.queuePersona = handle, persona
	r.persistQueueLocked()
	start := !r.flusherOn
	r.flusherOn = true
	total := len(r.queue)
	r.queueMu.Unlock()
	log.Printf("aria-responder: pane mid-task — queued %d mention(s), %d waiting", len(mentions), total)
	r.recordActivity("queued", batchChats(mentions), fmt.Sprintf("%d queued — pane mid-task (%d waiting)", len(mentions), total))
	if r.uiBus != nil {
		r.uiBus.publish(uiEvent{
			Type:       "toast",
			Text:       fmt.Sprintf("🤖 @%s mention queued — pane is mid-task, answering when it's free", handle),
			SkipRecent: true,
		})
	}
	if start {
		r.startFlusher()
	}
}

// peekQueue snapshots the queue without consuming it, so a delivery attempt
// that fails leaves the line intact and concurrent enqueues are never lost.
func (r *ariaAgentRunner) peekQueue() (batch []ariaQueuedMention, handle, persona string) {
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	return append([]ariaQueuedMention(nil), r.queue...), r.queueHandle, r.queuePersona
}

func (r *ariaAgentRunner) dropDelivered(n int) {
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	if n >= len(r.queue) {
		r.queue = nil
	} else {
		r.queue = append([]ariaQueuedMention(nil), r.queue[n:]...)
	}
	r.persistQueueLocked()
}

// stopFlusherIfEmpty ends the flusher's tenure iff nothing is waiting; the
// shared lock makes this atomic with enqueueForPane, so a mention arriving
// at the same instant either lands in the queue (flusher keeps going) or
// sees flusherOn=false and delivers directly.
func (r *ariaAgentRunner) stopFlusherIfEmpty() bool {
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	if len(r.queue) == 0 {
		r.flusherOn = false
		return true
	}
	return false
}

func (r *ariaAgentRunner) clearQueueAndStop() {
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	r.queue = nil
	r.flusherOn = false
	r.persistQueueLocked()
}

// ---------------------------------------------------------------------------
// queue persistence — the only copy of consumed-but-unanswered mentions
// ---------------------------------------------------------------------------

type ariaQueueFile struct {
	Handle   string              `json:"handle"`
	Persona  string              `json:"persona,omitempty"`
	Mentions []ariaQueuedMention `json:"mentions"`
}

func ariaQueuePath(dataDir, agentID string) string {
	return filepath.Join(dataDir, "aria_queue_"+agentID+".json")
}

func (r *ariaAgentRunner) queuePath() string {
	return ariaQueuePath(r.dataDir, r.cfg.ID)
}

// persistQueueLocked mirrors the queue to disk (caller holds queueMu).
// Best-effort: a full disk must never take the responder down with it.
func (r *ariaAgentRunner) persistQueueLocked() {
	if len(r.queue) == 0 {
		_ = os.Remove(r.queuePath())
		return
	}
	b, err := json.Marshal(ariaQueueFile{Handle: r.queueHandle, Persona: r.queuePersona, Mentions: r.queue})
	if err == nil {
		err = os.WriteFile(r.queuePath(), b, 0o600)
	}
	if err != nil {
		log.Printf("aria-responder: persist queue failed: %v", err)
	}
}

// resumeQueue reloads mentions a previous runner left waiting and restarts
// the flusher over them. Called once from run(); the flusher itself handles
// the re-pointed/expired cases exactly as it would live entries.
func (r *ariaAgentRunner) resumeQueue() {
	b, err := os.ReadFile(r.queuePath())
	if err != nil {
		return
	}
	var f ariaQueueFile
	if err := json.Unmarshal(b, &f); err != nil || len(f.Mentions) == 0 {
		return
	}
	r.queueMu.Lock()
	r.queue = f.Mentions
	r.queueHandle, r.queuePersona = f.Handle, f.Persona
	start := !r.flusherOn
	r.flusherOn = true
	r.queueMu.Unlock()
	log.Printf("aria-responder: resumed %d queued mention(s) from before restart", len(f.Mentions))
	if start {
		r.startFlusher()
	}
}

// mentionsOf strips the queue stamps back to the wire shape.
func mentionsOf(qs []ariaQueuedMention) []ariaMention {
	out := make([]ariaMention, len(qs))
	for i, q := range qs {
		out[i] = q.ariaMention
	}
	return out
}

// expiredPrefix returns how many leading queue entries have waited past the
// cap. A prefix is enough: EnqueuedAt is append-ordered, so the oldest are
// always in front.
func expiredPrefix(qs []ariaQueuedMention, cutoff time.Time) int {
	n := 0
	for _, q := range qs {
		if q.EnqueuedAt.After(cutoff) {
			break
		}
		n++
	}
	return n
}

// flushQueueWhenPaneFree delivers queued mentions the moment the target pane
// goes idle. Exits when the queue drains, the runner shuts down (the file
// keeps the line for the successor), or a terminal condition hands the batch
// to the (disclosed) headless fallback. Mentions that individually wait past
// the cap dump to the fallback in waves while fresher ones keep waiting.
func (r *ariaAgentRunner) flushQueueWhenPaneFree() {
	for {
		if !r.sleepOrDone(ariaPaneQueuePoll) {
			return // shutdown: queue stays persisted for the successor
		}
		batch, handle, persona := r.peekQueue()
		if len(batch) == 0 {
			if r.stopFlusherIfEmpty() {
				return
			}
			continue
		}
		if r.cfg.PaneID == "" {
			// A resumed queue whose agent now answers headless: that's the
			// chosen mode, not a fallback — answer there, no disclosure.
			// Detached so the flusher returns promptly (bounds stop()).
			r.clearQueueAndStop()
			go r.replyHeadless(handle, persona, mentionsOf(batch), "")
			return
		}
		if n := expiredPrefix(batch, time.Now().Add(-ariaPaneQueueMaxWait)); n > 0 {
			// Detached: the multi-minute fallback run must not block the
			// FRESHER mentions still in the queue from reaching the pane.
			log.Printf("aria-responder: %d mention(s) waited past %s — disclosed fallback answers them, %d keep waiting", n, ariaPaneQueueMaxWait, len(batch)-n)
			r.dropDelivered(n)
			go r.replyHeadless(handle, persona, mentionsOf(batch[:n]), ariaFallbackPaneStuck)
			continue
		}
		switch r.deliverToPane(r.cfg.PaneID, handle, persona, mentionsOf(batch)) {
		case paneDelivered:
			r.dropDelivered(len(batch))
			go r.signalTyping(mentionsOf(batch), true)
			go r.typingWhilePaneWorks(r.cfg.PaneID, mentionsOf(batch))
			if r.stopFlusherIfEmpty() {
				return
			}
		case paneBusy:
			// hold the line; next tick re-checks
		case paneGone:
			r.clearQueueAndStop()
			go r.replyHeadless(handle, persona, mentionsOf(batch), ariaFallbackPaneGone)
			return
		}
	}
}

// notifyChats posts a short deterministic notice to each distinct chat in
// the batch via the gateway's REST send — the daemon's own voice for states
// where no model is running (approval stalls, shutdown regrets). Best-effort:
// a notice must never block or fail the loop that sends it.
func (r *ariaAgentRunner) notifyChats(mentions []ariaMention, text string) {
	for _, m := range distinctChats(mentions) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := ariaGatewaySend(ctx, r.httpc, r.cfg.gateway(), r.cfg.AgentKey, m.Source, m.ChatID, text); err != nil {
			log.Printf("aria-responder: notice to %s/%s failed: %v", m.Source, m.ChatID, err)
		} else {
			r.recordActivity("notice", chatLabel(m.Source, m.ChatID, m.ChatName), text)
		}
		cancel()
	}
}

// replyHeadless runs one fresh headless claude session over the mentions.
// The gateway rides in via a 0600 --mcp-config temp file, so the run works
// even on a machine that never ran `claude mcp add`; --allowedTools pins the
// surface to exactly the gateway's tools (everything else follows the
// permission preset, default plan = read-only). A non-empty fallbackReason
// means this run is COVERING for the owner's chosen pane — the prompt makes
// the reply say so, so a downgraded answer is never mistaken for the real
// session.
func (r *ariaAgentRunner) replyHeadless(handle, persona string, mentions []ariaMention, fallbackReason string) {
	bin, err := resolveClaudeBin()
	if err != nil {
		log.Printf("aria-responder: %v", err)
		return
	}
	// Point the run at the keyless loopback proxy when the daemon addr is
	// known: no secret in the config, so it can ride inline argv with no
	// /proc leak and no temp file. Only when the proxy URL is unavailable
	// (no listen addr) do we fall back to the key-bearing 0600 temp file.
	var mcpArg string
	if proxyURL := ariaMcpProxyURL(r.listenAddr, r.cfg.mcpName()); proxyURL != "" {
		cfg, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
			r.cfg.mcpName(): map[string]any{"type": "http", "url": proxyURL},
		}})
		if err != nil {
			log.Printf("aria-responder: marshal mcp config: %v", err)
			return
		}
		mcpArg = string(cfg)
	} else {
		mcpPath, err := writeAriaMcpConfig(r.dataDir, r.cfg.gateway(), r.cfg.AgentKey, r.cfg.mcpName())
		if err != nil {
			log.Printf("aria-responder: write mcp config: %v", err)
			return
		}
		defer os.Remove(mcpPath)
		mcpArg = mcpPath
	}
	mentionsJSON, err := json.MarshalIndent(mentions, "", "  ")
	if err != nil {
		log.Printf("aria-responder: marshal mentions: %v", err)
		return
	}
	prompt := fmt.Sprintf(
		"You are %q, the user's Aria chat agent. These chat messages just @mentioned you:\n\n%s\n\n"+
			"Reply to each via the %s send_message tool with the matching source and chat_id. "+
			"Reply directly when the message itself is enough; use the %s read_messages tool "+
			"only if you truly need more of the conversation. "+
			"Keep replies short, helpful, conversational. Never write \"@%s\" in a reply. "+
			"When every mention is answered, stop — output only a one-line summary of what you sent.",
		handle, mentionsJSON, r.cfg.mcpName(), r.cfg.mcpName(), handle)
	if persona != "" {
		// Same framing the hosted responder uses, so the owner's instructions
		// steer the agent identically in both answering modes.
		prompt += "\n\nOwner's instructions for you:\n" + persona
	}
	if fallbackReason != "" {
		prompt += "\n\nIMPORTANT: The owner normally routes your replies through a live work session " +
			"with full tools, but " + fallbackReason + " — you are a limited fallback with ONLY the " +
			r.cfg.mcpName() + " tools (no repo, files, or commands). Open your reply with one short clause " +
			"saying so, answer what you can, and if a request needs that session's tools, say it'll " +
			"have to wait for the session instead of guessing."
	}

	args := []string{
		"-p",
		"--permission-mode", taskPermissionModes[r.cfg.permission()],
		"--output-format", "text",
		"--mcp-config", mcpArg,
		"--allowedTools", "mcp__" + r.cfg.mcpName() + "__*",
	}
	if r.cfg.Model != "" {
		args = append(args, "--model", r.cfg.Model)
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = r.dataDir
	cmd.Env = providerChildEnvForBin(bin, os.Environ())
	cmd.Stdin = strings.NewReader(prompt)
	applyDetached(cmd)
	out := &boundedBuf{limit: taskLogLimit}
	cmd.Stdout = out
	cmd.Stderr = out

	// Typing indicator for the whole run — the send clears it server-side
	// too, but the explicit stop covers failed/timed-out runs.
	stopTyping := r.keepTyping(mentions)
	defer stopTyping()

	start := time.Now()
	if err := startWithoutCoreDumps(cmd); err != nil {
		log.Printf("aria-responder: spawn failed: %v", err)
		return
	}
	kill := time.AfterFunc(ariaResponderRunTimeout, func() { _ = killChildTree(cmd.Process) })
	werr := waitOwnedCommand(cmd)
	timedOut := !kill.Stop()

	header := fmt.Sprintf("# aria-responder run · handle=@%s · mentions=%d · started=%s\n\n",
		handle, len(mentions), start.Format(time.RFC3339))
	writeRunLog(filepath.Join(r.dataDir, "aria-responder-runs"), start, append([]byte(header), out.buf...))

	kind := "headless"
	if fallbackReason != "" {
		kind = "fallback"
	}
	dur := time.Since(start).Round(time.Second)
	switch {
	case timedOut:
		log.Printf("aria-responder: run timed out after %s — killed", ariaResponderRunTimeout)
		r.recordActivity("error", batchChats(mentions), fmt.Sprintf("%s run timed out after %s", kind, ariaResponderRunTimeout))
	case werr != nil:
		log.Printf("aria-responder: run failed in %s: %v · %s", dur, werr, ariaTruncate(out.buf, 300))
		r.recordActivity("error", batchChats(mentions), fmt.Sprintf("%s run failed: %v", kind, werr))
	default:
		log.Printf("aria-responder: replied in %s: %s", dur, ariaTruncate(out.buf, 300))
		r.noteReply()
		r.recordActivity(kind, batchChats(mentions), fmt.Sprintf("replied in %s", dur))
		if r.uiBus != nil {
			r.uiBus.publish(uiEvent{
				Type: "toast",
				Text: fmt.Sprintf("🤖 @%s replied: %s", handle, taskOutputPreview(out.buf)),
				// run-results already land in the run log; keep Recent clean
				SkipRecent: true,
			})
		}
	}
}

// writeAriaMcpConfig writes the gateway MCP config (with its Bearer key) to
// a 0600 temp file in dataDir and returns the path; the caller removes it
// when the run ends. os.CreateTemp creates 0600 — same trust boundary as
// aria_responder.json beside it.
func writeAriaMcpConfig(dir, gateway, key, serverName string) (string, error) {
	cfg, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			serverName: map[string]any{
				"type": "http",
				"url":  gateway + "/mcp",
				"headers": map[string]string{
					"Authorization": "Bearer " + key,
				},
			},
		},
	})
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "aria-mcp-*.json")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(cfg); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func ariaTruncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// registerClaudeMcp registers a user-scope MCP server so pane sessions can
// send as the agent without the owner ever running `claude mcp add` by hand.
// mcpURL is the daemon's KEYLESS loopback proxy (aria_mcp_proxy.go) — the
// ariak_ key never enters ~/.claude.json, and because the URL is stable, a
// later key rotation needs no re-registration and doesn't break running
// sessions. Best-effort: a missing claude binary just logs. Panes with Claude
// ALREADY running keep their old connection until that session restarts —
// the API note tells the owner (a ONE-time restart to move onto the proxy).
func registerClaudeMcp(mcpURL, name string) {
	if mcpURL == "" {
		return
	}
	bin, err := resolveClaudeBin()
	if err != nil {
		log.Printf("aria-responder: mcp register skipped: %v", err)
		return
	}
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = providerChildEnvForBin(bin, os.Environ())
		applyDetached(cmd)
		return guardedCombinedOutput(cmd)
	}
	_, _ = run("mcp", "remove", "-s", "user", name) // absent = fine
	out, err := run("mcp", "add", "-s", "user", name, "--transport", "http", mcpURL)
	if err != nil {
		log.Printf("aria-responder: mcp register failed: %v · %s", err, ariaTruncate(out, 200))
		return
	}
	log.Printf("aria-responder: user-scope MCP %q registered (existing Claude sessions pick it up on restart)", name)
}

// unregisterClaudeMcp removes a deleted agent's user-scope registration so
// stale keys don't linger in ~/.claude.json.
func unregisterClaudeMcp(name string) {
	bin, err := resolveClaudeBin()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "mcp", "remove", "-s", "user", name)
	cmd.Env = providerChildEnvForBin(bin, os.Environ())
	applyDetached(cmd)
	_, _ = guardedCombinedOutput(cmd)
}

// ---------------------------------------------------------------------------
// API — /api/aria/responder (auth-gated with the rest of /api/*)
// ---------------------------------------------------------------------------

type ariaAgentDTO struct {
	ID         string `json:"id"`
	Enabled    bool   `json:"enabled"`
	HasKey     bool   `json:"hasKey"`
	KeyPrefix  string `json:"keyPrefix,omitempty"`
	Handle     string `json:"handle,omitempty"`
	GatewayURL string `json:"gatewayUrl"`
	Permission string `json:"permission"`
	Model      string `json:"model,omitempty"`
	PaneID     string `json:"paneId,omitempty"`
	McpName    string `json:"mcpName"`
	// Status is the runner's live health — what used to be log-only.
	Status *ariaResponderStatusDTO `json:"status,omitempty"`
	// Verify reports the save-time /v1/info round-trip, only on a PUT that
	// changed this agent's key. ok:false = saved anyway but unverified
	// (gateway unreachable); a key the gateway REJECTS never saves at all.
	Verify *ariaVerifyDTO `json:"verify,omitempty"`
}

// ariaResponderDTO carries the agent list plus a legacy mirror of the FIRST
// agent at the top level — Aria's pairing client and older web bundles read
// those fields (hasKey/runningPanes/PUT-without-id) and must keep working.
type ariaResponderDTO struct {
	Enabled    bool   `json:"enabled"`
	HasKey     bool   `json:"hasKey"`
	KeyPrefix  string `json:"keyPrefix,omitempty"`
	Handle     string `json:"handle,omitempty"`
	GatewayURL string `json:"gatewayUrl"`
	Permission string `json:"permission"`
	Model      string `json:"model,omitempty"`
	PaneID     string `json:"paneId,omitempty"`
	// RunningPanes lets a pairing client (Aria) tell WHICH daemon owns the
	// user's sessions in dual native+WSL setups — pairing with the paneless
	// daemon is the classic wrong pick.
	RunningPanes int                     `json:"runningPanes"`
	Note         string                  `json:"note"`
	Verify       *ariaVerifyDTO          `json:"verify,omitempty"`
	Status       *ariaResponderStatusDTO `json:"status,omitempty"`
	Agents       []ariaAgentDTO          `json:"agents"`
}

type ariaVerifyDTO struct {
	Ok           bool   `json:"ok"`
	Handle       string `json:"handle,omitempty"`
	Mode         string `json:"mode,omitempty"`
	GrantedChats int    `json:"grantedChats"`
	Error        string `json:"error,omitempty"`
}

type ariaResponderStatusDTO struct {
	State       string         `json:"state"` // disabled|standby|listening|auth-failed|gateway-down
	Mode        string         `json:"mode,omitempty"`
	Handle      string         `json:"handle,omitempty"`
	LastError   string         `json:"lastError,omitempty"`
	LastPollAt  *time.Time     `json:"lastPollAt,omitempty"`
	LastReplyAt *time.Time     `json:"lastReplyAt,omitempty"`
	QueueLen    int            `json:"queueLen"`
	Activity    []ariaActivity `json:"activity"`
}

func (r *ariaAgentRunner) statusDTO() *ariaResponderStatusDTO {
	r.queueMu.Lock()
	qlen := len(r.queue)
	r.queueMu.Unlock()
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	dto := &ariaResponderStatusDTO{
		State:     r.stState,
		Mode:      r.stMode,
		Handle:    r.stHandle,
		LastError: r.stLastErr,
		QueueLen:  qlen,
		// Never nil — the web reads .activity.length straight off the DTO.
		Activity: append([]ariaActivity{}, r.activity...),
	}
	if dto.State == "" {
		dto.State = "starting" // constructed but not yet past the first poll
	}
	if dto.Handle == "" {
		dto.Handle = r.cfg.Handle
	}
	if !r.stLastPoll.IsZero() {
		t := r.stLastPoll
		dto.LastPollAt = &t
	}
	if !r.stLastReply.IsZero() {
		t := r.stLastReply
		dto.LastReplyAt = &t
	}
	return dto
}

const ariaResponderNote = "Answers @mentions of your Aria agent keys on this machine via your " +
	"Claude subscription. Each key's answering mode lives in Aria (Settings → " +
	"Agents). Saving a key auto-registers its MCP server for your Claude " +
	"sessions — panes with Claude ALREADY running keep their old connection " +
	"until you restart Claude there (/exit, then claude)."

func ariaAgentToDTO(s ariaAgentConfig) ariaAgentDTO {
	dto := ariaAgentDTO{
		ID:         s.ID,
		Enabled:    s.Enabled,
		HasKey:     s.AgentKey != "",
		Handle:     s.Handle,
		GatewayURL: s.gateway(),
		Permission: s.permission(),
		Model:      s.Model,
		PaneID:     s.PaneID,
		McpName:    s.mcpName(),
	}
	if len(s.AgentKey) >= 12 {
		dto.KeyPrefix = s.AgentKey[:12]
	}
	return dto
}

// responderDTO assembles the full response: per-agent DTOs with live status,
// plus the legacy first-agent mirror.
func (a *apiServer) responderDTO(cfg ariaResponderConfig) ariaResponderDTO {
	dto := ariaResponderDTO{
		Note:         ariaResponderNote,
		RunningPanes: a.runningPaneCount(),
		Agents:       []ariaAgentDTO{},
	}
	for _, ag := range cfg.Agents {
		ad := ariaAgentToDTO(ag)
		if a.ariaResponder != nil {
			ad.Status = a.ariaResponder.statusFor(ag.ID)
			if !ag.Enabled && ad.Status != nil {
				ad.Status.State = "disabled"
			}
		}
		dto.Agents = append(dto.Agents, ad)
	}
	if len(dto.Agents) > 0 {
		first := dto.Agents[0]
		dto.Enabled = first.Enabled
		dto.HasKey = first.HasKey
		dto.KeyPrefix = first.KeyPrefix
		dto.Handle = first.Handle
		dto.GatewayURL = first.GatewayURL
		dto.Permission = first.Permission
		dto.Model = first.Model
		dto.PaneID = first.PaneID
		dto.Status = first.Status
	} else {
		dto.GatewayURL = ariaResponderGatewayDefault
		dto.Permission = "plan"
		dto.Status = &ariaResponderStatusDTO{State: "disabled", Activity: []ariaActivity{}}
	}
	return dto
}

// runningPaneCount is the pairing hint for dual-daemon setups (see DTO).
func (a *apiServer) runningPaneCount() int {
	if a.ariaResponder == nil || a.ariaResponder.mgr == nil {
		return 0
	}
	n := 0
	for _, p := range a.ariaResponder.mgr.List() {
		if p.Snapshot().Status == "running" {
			n++
		}
	}
	return n
}

func (a *apiServer) getAriaResponder(w http.ResponseWriter, _ *http.Request) {
	if a.ariaResponder == nil {
		http.Error(w, "aria responder unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, a.responderDTO(a.ariaResponder.store.get()))
}

// deriveMcpName picks a stable, unique user-scope MCP server name for a new
// agent: the historical "aria-comms" if free (running sessions depend on
// it), else "aria-comms-<handle-or-id>".
func deriveMcpName(cfg ariaResponderConfig, agent *ariaAgentConfig) string {
	taken := map[string]bool{}
	for i := range cfg.Agents {
		if cfg.Agents[i].ID != agent.ID && cfg.Agents[i].McpName != "" {
			taken[cfg.Agents[i].McpName] = true
		}
	}
	if !taken["aria-comms"] {
		return "aria-comms"
	}
	base := strings.ToLower(agent.Handle)
	base = strings.Map(func(c rune) rune {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			return c
		}
		return -1
	}, base)
	if base == "" {
		base = strings.TrimPrefix(agent.ID, "agent_")
	}
	name := "aria-comms-" + base
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("aria-comms-%s%d", base, i)
	}
	return name
}

// putAriaResponder edits one agent. Body field `id` selects it: "" = the
// first/default agent (the legacy contract — Aria's pairing client PUTs a
// bare {agentKey}), "new" = append a fresh agent, anything else must match
// an existing agent's id.
func (a *apiServer) putAriaResponder(w http.ResponseWriter, r *http.Request) {
	if a.ariaResponder == nil {
		http.Error(w, "aria responder unavailable", http.StatusServiceUnavailable)
		return
	}
	var in struct {
		ID         *string `json:"id"`
		Enabled    *bool   `json:"enabled"`
		AgentKey   *string `json:"agentKey"`
		GatewayURL *string `json:"gatewayUrl"`
		Permission *string `json:"permission"`
		Model      *string `json:"model"`
		PaneID     *string `json:"paneId"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Aria's responder acts on messages from outside; its permission level,
	// gateway, key and switching it on are the person's choices.
	if (in.Permission != nil || in.GatewayURL != nil || in.AgentKey != nil || (in.Enabled != nil && *in.Enabled)) &&
		!a.requireHuman(w, r, "change Aria's permissions or connection") {
		return
	}
	cfg := a.ariaResponder.store.get()
	var agent *ariaAgentConfig
	explicitNew := false
	id := ""
	if in.ID != nil {
		id = strings.TrimSpace(*in.ID)
	}
	switch id {
	case "":
		// Legacy/default agent. Auto-created keyless if absent so the old
		// two-step pairing flow (set gateway, THEN paste key) still works —
		// only an EXPLICIT "new" add is required to carry a key.
		if len(cfg.Agents) == 0 {
			cfg.Agents = append(cfg.Agents, ariaAgentConfig{ID: ariaDefaultAgentID, McpName: "aria-comms"})
		}
		agent = &cfg.Agents[0]
	case "new":
		cfg.Agents = append(cfg.Agents, ariaAgentConfig{ID: newAriaAgentID()})
		agent = &cfg.Agents[len(cfg.Agents)-1]
		explicitNew = true
	default:
		agent = cfg.find(id)
		if agent == nil {
			http.Error(w, "no such agent", http.StatusNotFound)
			return
		}
	}
	if in.Enabled != nil {
		agent.Enabled = *in.Enabled
	}
	keyChanged := false
	if in.AgentKey != nil && *in.AgentKey != "" {
		if !ariaAgentKeyRe.MatchString(*in.AgentKey) {
			http.Error(w, "agentKey must look like ariak_<48 hex>", http.StatusBadRequest)
			return
		}
		agent.AgentKey = *in.AgentKey
		keyChanged = true
	}
	if in.GatewayURL != nil {
		agent.GatewayURL = strings.TrimSpace(*in.GatewayURL)
	}
	if in.Permission != nil {
		if *in.Permission != "" && !validTaskPermission(*in.Permission) {
			http.Error(w, "permission must be plan or edit", http.StatusBadRequest)
			return
		}
		agent.Permission = *in.Permission
	}
	if in.Model != nil {
		agent.Model = strings.TrimSpace(*in.Model)
	}
	if in.PaneID != nil {
		agent.PaneID = strings.TrimSpace(*in.PaneID)
	}
	if explicitNew && !keyChanged {
		http.Error(w, "a new agent needs an agentKey", http.StatusBadRequest)
		return
	}
	if agent.Enabled && agent.AgentKey == "" {
		http.Error(w, "set agentKey before enabling", http.StatusBadRequest)
		return
	}
	// Verify a changed key with the gateway NOW, while the user is looking —
	// a rotated/revoked key otherwise saves fine and fails silently at poll
	// time. Runs AFTER every field lands so a same-PUT gatewayUrl is the one
	// verified against. Gateway explicitly rejects it → don't save; gateway
	// unreachable → save anyway, flagged unverified (its outage is not the
	// key's fault).
	var verify *ariaVerifyDTO
	if keyChanged {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		info, status, err := ariaGatewayInfoCall(ctx, a.ariaResponder.httpc, agent.gateway(), agent.AgentKey)
		cancel()
		switch {
		case err == nil:
			agent.Handle = info.Handle
			verify = &ariaVerifyDTO{Ok: true, Handle: info.Handle, Mode: info.Mode, GrantedChats: len(info.Chats)}
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			http.Error(w, "the gateway rejected this key — it may have been rotated or disabled in Aria → Settings → Agents; paste a current one", http.StatusBadRequest)
			return
		default:
			agent.Handle = ""
			verify = &ariaVerifyDTO{Ok: false, Error: "saved, but could not verify with the gateway: " + err.Error()}
		}
	}
	if agent.McpName == "" {
		agent.McpName = deriveMcpName(cfg, agent)
	}
	agentID := agent.ID
	if err := a.ariaResponder.store.set(cfg); err != nil {
		http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// New key ⇒ register the KEYLESS loopback MCP proxy so panes can send as
	// this agent without a manual `claude mcp add` (async — never blocks the
	// save). No-op if the daemon addr is unknown; the URL carries no secret.
	if keyChanged {
		go registerClaudeMcp(ariaMcpProxyURL(a.ariaResponder.listenAddr, agent.mcpName()), agent.mcpName())
	}
	// Build the response from the config we just wrote — no second store.get().
	dto := a.responderDTO(cfg)
	for i := range dto.Agents {
		if dto.Agents[i].ID == agentID {
			dto.Agents[i].Verify = verify
			if i == 0 {
				dto.Verify = verify
			}
		}
	}
	writeJSON(w, http.StatusOK, dto)
}

// deleteAriaResponderAgent removes an agent outright: its runner stops, its
// queued chats get the shutdown notice (reconcile), and its user-scope MCP
// registration is cleaned up so the key doesn't linger in ~/.claude.json.
func (a *apiServer) deleteAriaResponderAgent(w http.ResponseWriter, r *http.Request) {
	if a.ariaResponder == nil {
		http.Error(w, "aria responder unavailable", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	cfg := a.ariaResponder.store.get()
	idx := -1
	for i := range cfg.Agents {
		if cfg.Agents[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		http.Error(w, "no such agent", http.StatusNotFound)
		return
	}
	removed := cfg.Agents[idx]
	cfg.Agents = append(cfg.Agents[:idx], cfg.Agents[idx+1:]...)
	if err := a.ariaResponder.store.set(cfg); err != nil {
		http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	go unregisterClaudeMcp(removed.mcpName())
	writeJSON(w, http.StatusOK, a.responderDTO(a.ariaResponder.store.get()))
}
