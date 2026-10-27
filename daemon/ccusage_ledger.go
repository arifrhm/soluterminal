package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// In-app vs out-app session ledger.
//
// The daemon's Claude Code hooks are registered globally in
// ~/.claude/settings.json, so EVERY session — whether the daemon spawned it
// inside a pane ("in-app") or the user ran a bare `claude` in a terminal
// ("out-app") — fires them. In-app sessions carry the SOLU_PANE_ID marker
// (forwarded as the X-Soluterminal-Pane header); out-app sessions don't.
//
// The hook handler (a separate, later-owned file) calls Record(sessionID,
// inApp) on every hook event. The transcript scanner later calls
// Lookup(sessionID) to tag each usage row's in_app field. Unknown sessions
// (ran before the hooks were registered, or without our hooks) resolve to
// out-app at the scanner.
//
// This ledger is the in-memory authority. It persists to
// <dataDir>/cc-inapp-sessions.json so the flag survives daemon restarts, but
// because Record is called very frequently we do NOT save on every call: a
// dirty flag tracks unsaved changes and the scanner calls Flush() once per
// tick to persist if anything changed.
//
// in_app is an UPGRADE-ONLY flag: once a session is proven in-app it stays
// in-app forever. A session that ever fires a hook carrying the pane header
// is genuinely in-app even if some other hook event for it arrives without
// the header (e.g. a different hook type that doesn't forward SOLU_PANE_ID).
// An out-app session can never gain a pane, so a later in_app=true legitimately
// upgrades an earlier in_app=false, but never the reverse.

// inAppEntry is the persisted per-session record.
type inAppEntry struct {
	// InApp is true once the session has ever been seen carrying the pane
	// marker. Upgrade-only.
	InApp bool `json:"inApp"`
	// FirstSeenUnix is the first time this session was recorded (seconds
	// since the Unix epoch). First-seen wins; used by Prune.
	FirstSeenUnix int64 `json:"firstSeenUnix"`
	// Account is the Claude account name that was active when this session
	// started (captured at SessionStart). Empty for sessions that predate
	// account capture, ran with no active cred, or were never seen by a
	// SessionStart hook. The scanner treats empty as "fall back to the machine
	// owner" — the cloud's existing behavior — so legacy rows stay attributed
	// exactly as today. omitempty keeps ledger files compact when unattributed.
	Account string `json:"account,omitempty"`
	// AccountAuthoritative marks Account as recorded by a source that knows
	// which credential paid for the session (a workflow invocation's lane),
	// rather than the hook-time guess of the machine's active login. A weak
	// RecordAccount never overwrites it; an authoritative record overwrites a
	// weak one.
	AccountAuthoritative bool   `json:"accountAuthoritative,omitempty"`
	WorkflowInvocationID string `json:"workflowInvocationId,omitempty"`
	WorkflowRole         string `json:"workflowRole,omitempty"`
	CacheTTLClass        string `json:"cacheTtlClass,omitempty"`
	CacheTTLControl      string `json:"cacheTtlControl,omitempty"`
}

// inAppLedger is the thread-safe, lazily-persisted session→in_app map. The
// hook handler (Record) and the scanner (Lookup/Flush) touch it
// concurrently, so every method takes the mutex.
type inAppLedger struct {
	path  string
	mu    sync.Mutex
	m     map[string]inAppEntry
	dirty bool
}

// newInAppLedger constructs an empty ledger backed by
// <dataDir>/cc-inapp-sessions.json. It does NOT read from disk — call Load
// once at startup.
func newInAppLedger(dataDir string) *inAppLedger {
	return &inAppLedger{
		path: filepath.Join(dataDir, "cc-inapp-sessions.json"),
		m:    make(map[string]inAppEntry),
	}
}

// Load reads the persisted ledger. A missing file is not an error (empty
// ledger). A corrupt file is quarantined aside and the ledger starts fresh —
// losing the in_app history degrades gracefully (unknown → out-app), it
// doesn't wedge anything.
func (l *inAppLedger) Load() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, err := os.ReadFile(l.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(b) == 0 {
		return nil
	}
	var m map[string]inAppEntry
	if err := json.Unmarshal(b, &m); err != nil {
		quarantineCorruptState(l.path, err)
		l.m = make(map[string]inAppEntry)
		return nil
	}
	if m == nil {
		m = make(map[string]inAppEntry)
	}
	l.m = m
	l.dirty = false
	return nil
}

// Record notes that sessionID was seen with the given in-app flag.
//
//   - FirstSeenUnix is set once (first-seen wins) and never moved.
//   - InApp is upgrade-only: a true sticks; a later false can never clear an
//     existing true, but a true can upgrade an earlier false.
//
// Marks the ledger dirty on any change (new entry or InApp upgrade) so the
// next Flush persists it. Cheap and lock-bounded — safe to call on every
// hook event. An empty sessionID is ignored.
func (l *inAppLedger) Record(sessionID string, inApp bool) {
	if sessionID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	cur, ok := l.m[sessionID]
	if !ok {
		l.m[sessionID] = inAppEntry{InApp: inApp, FirstSeenUnix: time.Now().Unix()}
		l.dirty = true
		return
	}
	// Upgrade-only on InApp; FirstSeen is immutable.
	if inApp && !cur.InApp {
		cur.InApp = true
		l.m[sessionID] = cur
		l.dirty = true
	}
}

// Lookup returns the in-app flag for sessionID. known is false if the
// session was never recorded — the scanner treats unknown as out-app.
func (l *inAppLedger) Lookup(sessionID string) (inApp bool, known bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.m[sessionID]
	if !ok {
		return false, false
	}
	return e.InApp, true
}

// RecordAccount notes the Claude account name that was active when sessionID
// started. Called once at SessionStart. First-non-empty wins: a real account
// name fills an empty slot and then sticks; an empty value never overwrites a
// recorded name; a later different name never clobbers an earlier one (the
// start-of-session account is authoritative). An empty sessionID is ignored.
// Marks dirty on a change so the next Flush persists it.
func (l *inAppLedger) RecordAccount(sessionID, account string) {
	if sessionID == "" || account == "" {
		// No-op on an empty account: we must not create a ledger entry for a
		// session we have nothing to attribute (it would shadow the in_app
		// semantics by making Lookup report the session known/out-app).
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	cur, ok := l.m[sessionID]
	if !ok {
		l.m[sessionID] = inAppEntry{Account: account, FirstSeenUnix: time.Now().Unix()}
		l.dirty = true
		return
	}
	if cur.Account == "" {
		cur.Account = account
		l.m[sessionID] = cur
		l.dirty = true
	}
}

// RecordAccountAuthoritative records the account that is known to have paid
// for sessionID (e.g. a workflow invocation's credential lane). Unlike
// RecordAccount it replaces an earlier hook-time guess; the first
// authoritative value then sticks. Empty sessionID/account are ignored.
func (l *inAppLedger) RecordAccountAuthoritative(sessionID, account string) {
	if sessionID == "" || account == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	cur, ok := l.m[sessionID]
	if !ok {
		l.m[sessionID] = inAppEntry{Account: account, AccountAuthoritative: true, FirstSeenUnix: time.Now().Unix()}
		l.dirty = true
		return
	}
	if cur.AccountAuthoritative {
		return
	}
	cur.Account = account
	cur.AccountAuthoritative = true
	l.m[sessionID] = cur
	l.dirty = true
}

// LookupAccount returns the recorded Claude account name for sessionID, or ""
// for an unknown or unattributed session. The scanner stamps each row's Account
// from this; "" means "fall back to the machine owner" (today's behavior), so
// unattributable turns are handled identically to before.
func (l *inAppLedger) LookupAccount(sessionID string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.m[sessionID]
	if !ok {
		return ""
	}
	return e.Account
}

// RecordWorkflow attributes a Claude session to the workflow launch that
// created it. Each field is first-nonempty-wins so hook and runtime observers
// may arrive in either order without clobbering stronger earlier evidence.
func (l *inAppLedger) RecordWorkflow(sessionID, invocationID, role, ttlClass, control string) {
	if sessionID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	cur, ok := l.m[sessionID]
	if !ok {
		cur.FirstSeenUnix = time.Now().Unix()
	}
	changed := !ok
	fill := func(dst *string, value string) {
		if *dst == "" && value != "" {
			*dst = value
			changed = true
		}
	}
	fill(&cur.WorkflowInvocationID, invocationID)
	fill(&cur.WorkflowRole, role)
	fill(&cur.CacheTTLClass, ttlClass)
	fill(&cur.CacheTTLControl, control)
	if changed {
		l.m[sessionID] = cur
		l.dirty = true
	}
}

func (l *inAppLedger) LookupWorkflow(sessionID string) (invocationID, role, ttlClass, control string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.m[sessionID]
	return e.WorkflowInvocationID, e.WorkflowRole, e.CacheTTLClass, e.CacheTTLControl
}

// Flush persists the ledger atomically IF it has unsaved changes, then
// clears the dirty flag. A no-op when nothing changed. The scanner calls
// this once per tick so the frequent Record calls don't each hit disk.
func (l *inAppLedger) Flush() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.dirty {
		return nil
	}
	b, err := json.MarshalIndent(l.m, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomicSynced(l.path, b, 0o600); err != nil {
		return err
	}
	l.dirty = false
	return nil
}

// Prune drops entries first seen strictly before the cutoff. Intended for
// trimming sessions older than Claude's transcript-retention window so the
// ledger doesn't grow unbounded. Marks dirty if anything was removed.
func (l *inAppLedger) Prune(before time.Time) {
	cutoff := before.Unix()
	l.mu.Lock()
	defer l.mu.Unlock()
	for id, e := range l.m {
		if e.FirstSeenUnix < cutoff {
			delete(l.m, id)
			l.dirty = true
		}
	}
}
