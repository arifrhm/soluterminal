package main

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// native_transcript.go seeds a native pane's prior conversation from the
// on-disk Claude transcript. Phase-0 proved `claude -p --resume <sessionId>`
// restores conversation CONTEXT server-side but DOES NOT replay prior turns as
// stream-json events, so a resumed/flipped native pane renders BLANK until the
// next message. seedFromTranscript re-derives the past turns from the .jsonl
// transcript Claude Code writes to disk and returns them as NativeEvents marked
// FromReplay=true.
//
// This is a SEPARATE parser from ClaudeAdapter.Feed: the on-disk transcript
// vocabulary (promptSource / isMeta / parentUuid) differs from the live
// stream-json vocabulary (isReplay / session / init), so transcript lines must
// NOT be routed through Feed(). The R1b allow-list implemented here:
//
//   KEEP+MAP:
//     assistant  -> message.content[]: text->EvText, thinking (non-empty)->
//                   EvThinking, tool_use->EvToolCall
//     user       -> string content w/ promptSource in {typed,sdk,queued} and
//                   none of isMeta/isCompactSummary/isVisibleInTranscriptOnly ->
//                   EvUserText; list content tool_result blocks -> EvToolResult
//   DROP everything else: mode, permission-mode, ai-title, last-prompt,
//     file-history-snapshot, queue-operation, attachment subtypes, system
//     subtypes, and synthetic/meta user lines.

// transcriptSlugRe matches every char Claude Code's project-dir encoder maps to
// '-'. Empirically the slug is the cwd with every non-alphanumeric char replaced
// by '-' (a strict superset of the "'/' and '.' -> '-'" rule; they agree for any
// path without '_'/space/'~'). Use the non-alnum rule to match the real encoder.
var transcriptSlugRe = regexp.MustCompile(`[^A-Za-z0-9]`)

// transcriptSlug munges a pane cwd into the Claude Code project-dir slug.
func transcriptSlug(cwd string) string {
	return transcriptSlugRe.ReplaceAllString(cwd, "-")
}

// resolveTranscriptPath returns the first existing
// <configDir>/projects/<slug>/<sessionID>.jsonl across the resolved Claude
// config dirs, or "" if none exists. cwd or sessionID empty -> "".
func resolveTranscriptPath(cwd, sessionID string) string {
	if cwd == "" || sessionID == "" {
		return ""
	}
	slug := transcriptSlug(cwd)
	for _, dir := range claudeConfigDirs() {
		p := filepath.Join(dir, "projects", slug, sessionID+".jsonl")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// parsedSeed is the INTERNAL intermediate produced by parsing a transcript: the
// seeded events in file order (each FromReplay=true) plus, parallel to events,
// the originating transcript line's uuid for each EvUserText event (empty for
// non-user events or when a line carries no uuid). The uuid is the stable
// per-prompt identity used by mergeSeedWithRing to dedup the replay echo without
// collapsing legitimately repeated prompts. It is NOT placed on the wire
// NativeEvent (the schema is owned elsewhere this round); it lives only here.
type parsedSeed struct {
	events   []NativeEvent
	userUUID []string // len == len(events); meaningful only where events[i].Type == EvUserText
}

// events returns a fresh copy of the parsed events so a caller can stamp Seqs
// (mergeSeedWithRing) without mutating the cached backing array.
func (p parsedSeed) clonedEvents() []NativeEvent {
	if len(p.events) == 0 {
		return nil
	}
	out := make([]NativeEvent, len(p.events))
	copy(out, p.events)
	return out
}

// seedCacheEntry memoizes one transcript file's parse keyed by (mtime,size). A
// change to either invalidates the entry (Claude appends to the file, so size
// always grows on a real update; mtime guards the rare same-size rewrite). It
// holds the full parsedSeed intermediate (events + per-user uuids) so a cache
// hit still drives the uuid-precise replay-echo dedup, not just bare events.
// tick is a monotonic LRU clock stamped on every hit/insert (origin/main's perf
// round added the cache too; this entry is the superset that also carries the
// uuids the native-renderer dedup needs).
type seedCacheEntry struct {
	mtime time.Time
	size  int64
	seed  parsedSeed
	tick  uint64 // insertion/refresh order, for bounded LRU eviction
}

// seedCacheCap bounds the parse cache. Native sessions are bounded (one entry
// per resumed pane's transcript), but cap defensively: keep at most this many
// parsed transcripts; on overflow evict the least-recently-used entry. The cap
// is a soft memory bound — each entry holds one parsed conversation.
const seedCacheCap = 64

var (
	seedCacheMu   sync.Mutex
	seedCache     = map[string]seedCacheEntry{}
	seedCacheTick uint64 // monotonic LRU clock, bumped under seedCacheMu
	// seedParseCount counts how many times a transcript file was actually
	// re-parsed from disk (cache MISS). Tests assert the cache elides re-reads
	// of an unchanged file; production code ignores it.
	seedParseCount uint64
)

// seedFromTranscript reads the on-disk Claude transcript for (cwd, sessionID)
// and returns the prior conversation as NativeEvents in file order, each marked
// FromReplay=true. A missing transcript file is NOT an error -> ([], nil). The
// parser is robust: malformed or unknown lines are skipped, never panic.
//
// The result is memoized per transcript path keyed by the file's (mtime, size):
// historyBackfill calls this synchronously on every cold connect, so a hot pane
// re-resolving an unchanged multi-MB .jsonl would otherwise re-scan it every
// reconnect. On a cache hit the on-disk file is NOT re-read.
//
// The returned events carry Type + payload + FromReplay only; Seq/PaneID are the
// egress owner's concern (the history handler stamps them when prepending).
//
// Parse cache: this is called on every fresh mount (since==0) AND every truncated
// reconnect resync, so a long-running native pane re-reads and re-JSON-parses a
// multi-MB transcript repeatedly on the request goroutine. A package-level cache
// (keyed by resolved file path, validated by mtime+size) serves repeat seeds of
// an UNCHANGED transcript from memory. The cache is best-effort and NEVER fails a
// seed: a stat error simply falls through to the uncached parse, and only clean
// parses (sc.Err()==nil) are cached so a partial read is never memoized. Callers
// may mutate/Seq-stamp the result (mergeSeedWithRing does), so a cache hit returns
// a fresh COPY of the cached slice — the cached events are never aliased out.
func seedFromTranscript(cwd, sessionID string, explicitModel ...string) ([]NativeEvent, error) {
	seed, err := seedParsedFromTranscript(cwd, sessionID)
	events := seed.clonedEvents()
	if len(explicitModel) > 0 && explicitModel[0] != "" {
		for i := range events {
			if events[i].Type == EvSessionInit && events[i].FromReplay {
				events[i].Model = explicitModel[0]
				break
			}
		}
	}
	return events, err
}

// seedParsedFromTranscript is the cached core: it returns the full parsedSeed
// intermediate (events + per-user uuids) so callers that need prompt identity
// (mergeSeedWithRing) can dedup precisely. A cache hit on an unchanged file
// returns the memoized parse without re-reading disk.
func seedParsedFromTranscript(cwd, sessionID string) (parsedSeed, error) {
	path := resolveTranscriptPath(cwd, sessionID)
	if path == "" {
		return parsedSeed{}, nil
	}
	// Best-effort cache lookup keyed by (mtime,size). A stat failure falls
	// through to an uncached parse below — the cache never turns a parseable
	// seed into a failure.
	if fi, statErr := os.Stat(path); statErr == nil {
		seedCacheMu.Lock()
		if e, ok := seedCache[path]; ok && e.size == fi.Size() && e.mtime.Equal(fi.ModTime()) {
			// Refresh recency so a hot path isn't evicted under the cap.
			seedCacheTick++
			e.tick = seedCacheTick
			seedCache[path] = e
			seed := e.seed
			seedCacheMu.Unlock()
			return seed, nil
		}
		seedCacheMu.Unlock()
	}

	// Cache miss (or stale): parse the file. Parsing is done OUTSIDE the lock so
	// concurrent callers for different files don't serialize on a slow read; a
	// rare duplicate parse of the same file under concurrency is harmless (both
	// produce the same result and the last write wins).
	seed, parseErr := parseTranscriptFile(path)
	if parseErr != nil {
		// A read/scan error mid-file still returns what we parsed so far rather
		// than discarding it; the caller treats a partial seed as best-effort. A
		// partial parse is NOT cached (the stamp would memoize a truncated read).
		return seed, parseErr
	}

	// Re-stat AFTER the read and store under the post-read stamp. Caching the
	// post-read stat (rather than a pre-read one) keeps the validity stamp
	// consistent with the bytes we actually parsed: if the file grew during the
	// read, the next call's stat won't match this stamp and will re-parse.
	if fi, statErr := os.Stat(path); statErr == nil {
		seedCacheMu.Lock()
		seedCacheTick++
		seedCache[path] = seedCacheEntry{mtime: fi.ModTime(), size: fi.Size(), seed: seed, tick: seedCacheTick}
		evictSeedCacheLocked()
		seedCacheMu.Unlock()
	}
	return seed, nil
}

// evictSeedCacheLocked drops the least-recently-used entry once the cache
// exceeds seedCacheCap. Caller MUST hold seedCacheMu.
func evictSeedCacheLocked() {
	for len(seedCache) > seedCacheCap {
		var oldestKey string
		var oldestTick uint64 = ^uint64(0)
		for k, e := range seedCache {
			if e.tick < oldestTick {
				oldestTick, oldestKey = e.tick, k
			}
		}
		delete(seedCache, oldestKey)
	}
}

// parseTranscriptFile reads and maps a transcript .jsonl into a parsedSeed. It
// increments seedParseCount (a test-visible read counter) on entry. A missing
// file is ([], nil); a mid-file scan error returns the partial parse + the error.
func parseTranscriptFile(path string) (parsedSeed, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return parsedSeed{}, nil
		}
		return parsedSeed{}, err
	}
	defer f.Close()

	seedCacheMu.Lock()
	seedParseCount++
	seedCacheMu.Unlock()

	var seed parsedSeed
	// Running vitals captured from the transcript so the seed can synthesize a
	// session_init (model/cwd) + usage (context%) — neither of which Claude
	// replays on --resume. Without these the Focus bottom bar paints blank until
	// the first LIVE turn fires a real session_init/usage. We track the LAST
	// assistant model + usage (the most-recent context occupancy) and any
	// line-level cwd/sessionId. See synthesizeSeedVitals.
	var vit seedVitals
	sc := bufio.NewScanner(f)
	// Transcript lines can be large (big tool_result payloads); raise the cap
	// well above bufio's default 64KiB so long lines aren't silently truncated.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		vit.observe(line)
		evs, uuid := mapTranscriptLineWithMeta(line)
		for i := range evs {
			evs[i].FromReplay = true
			seed.events = append(seed.events, evs[i])
			// Carry the line uuid only for the user_text event(s); other event
			// types don't participate in the replay-echo dedup.
			if evs[i].Type == EvUserText {
				seed.userUUID = append(seed.userUUID, uuid)
			} else {
				seed.userUUID = append(seed.userUUID, "")
			}
		}
	}
	if err := sc.Err(); err != nil {
		// A read/scan error mid-file still returns what we parsed so far rather
		// than discarding it; the caller treats a partial seed as best-effort.
		return synthesizeSeedVitals(seed, vit), err
	}
	return synthesizeSeedVitals(seed, vit), nil
}

// seedVitals accumulates the model + last-usage + cwd/sessionId scanned from a
// transcript, so the seed can rehydrate the Focus bottom bar (model / context% /
// folder) INSTANTLY on a resumed pane — `claude --resume` restores context but
// replays no session_init/usage, so without this the bar is blank until the user
// sends the first message (the bug this fixes). Only assistant lines carry a
// model + per-call usage; the LAST one wins (most-recent context occupancy).
type seedVitals struct {
	model     string
	cwd       string
	sessionID string
	hasUsage  bool
	usage     struct {
		input         int
		cacheRead     int
		cacheCreation int
		output        int
	}
	// compactAfterUsage — a /compact landed AFTER the last usage-bearing assistant
	// record, so `usage` above is the STALE pre-compact occupancy: a compaction
	// frees the window but older boundaries write no post-compaction usage.
	// Without this the seed replayed that pre-compact reading with no
	// marker, so a reload / Terminal↔Focus flip taken BEFORE the next turn
	// resurrected the old ctx% — the live path resets on the boundary, but the
	// re-seed re-derives from scratch and never saw one. Mirrors
	// nativeTranscript.ts's contextCompacted: a later context-bearing usage
	// supersedes it.
	compactAfterUsage bool
	// A current compact_boundary supplied postTokens. Its summary user record
	// follows immediately and must not turn the fresh boundary reading back into
	// an unknown/stale marker.
	sawBoundaryPost bool
}

// observe folds one raw transcript line into the running vitals. Cheap minimal
// unmarshal (the full event mapping happens separately in the scan loop); a
// malformed line is silently skipped. Seed-time only — never on a render path.
func (v *seedVitals) observe(line []byte) {
	var probe struct {
		Type             string `json:"type"`
		Subtype          string `json:"subtype"`
		Cwd              string `json:"cwd"`
		SessionID        string `json:"sessionId"`
		IsCompactSummary bool   `json:"isCompactSummary"`
		CompactMetadata  struct {
			PreTokens  int `json:"preTokens"`
			PostTokens int `json:"postTokens"`
		} `json:"compactMetadata"`
		Message struct {
			Model string `json:"model"`
			Usage *struct {
				InputTokens              int `json:"input_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
				OutputTokens             int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return
	}
	if probe.Cwd != "" {
		v.cwd = probe.Cwd
	}
	if probe.SessionID != "" {
		v.sessionID = probe.SessionID
	}
	if probe.Type == "system" && probe.Subtype == "compact_boundary" {
		if probe.CompactMetadata.PostTokens > 0 {
			v.hasUsage = true
			v.usage.input = probe.CompactMetadata.PostTokens
			v.usage.cacheRead = 0
			v.usage.cacheCreation = 0
			v.usage.output = 0
			v.compactAfterUsage = false
			v.sawBoundaryPost = true
		} else {
			v.compactAfterUsage = true
			v.sawBoundaryPost = false
		}
		return
	}
	// The compaction summary the CLI writes in place of the compacted turns — the
	// only on-disk trace of a /compact (verified against real transcripts: a
	// `type:"user"` record with isCompactSummary + isVisibleInTranscriptOnly and no
	// usage). It is dropped from the rendered rows further down, so this probe is
	// the seed's ONLY chance to notice the window was freed.
	if probe.IsCompactSummary {
		if !v.sawBoundaryPost {
			v.compactAfterUsage = true
		}
		return
	}
	if probe.Type != "assistant" {
		return
	}
	if probe.Message.Model != "" {
		v.model = probe.Message.Model
	}
	if probe.Message.Usage != nil {
		v.sawBoundaryPost = false
		v.hasUsage = true
		v.usage.input = probe.Message.Usage.InputTokens
		v.usage.cacheRead = probe.Message.Usage.CacheReadInputTokens
		v.usage.cacheCreation = probe.Message.Usage.CacheCreationInputTokens
		v.usage.output = probe.Message.Usage.OutputTokens
		// A context-BEARING usage (this turn re-measured the window) supersedes an
		// earlier compaction: the reading is post-compact and real again. Gated on
		// occupancy>0 for the same reason the client gates — a reasoning-only ping
		// carries no window reading and must not clear the marker.
		if probe.Message.Usage.InputTokens+probe.Message.Usage.CacheReadInputTokens+
			probe.Message.Usage.CacheCreationInputTokens > 0 {
			v.compactAfterUsage = false
		}
	}
}

// synthesizeSeedVitals PREPENDS a FromReplay session_init (model/cwd/sessionId)
// and APPENDS a FromReplay usage (last assistant token occupancy) to a parsed
// seed, so the client's existing fold logic populates the Focus bottom bar on
// re-entry without waiting for a live turn. Both are marked FromReplay so they
// get high disjoint seqs in mergeParsedSeedWithRing and don't advance the live
// resync cursor. The userUUID slice is kept parallel (empty for both synthetic
// events — neither is a user_text). A seed with no model (empty/tool-only
// transcript) is returned unchanged, so a brand-new pane is unaffected. The live
// session_init/usage stream OVERRIDES these the moment the first turn runs (the
// client prefers a live init over a replayed one; usage is last-wins).
func synthesizeSeedVitals(seed parsedSeed, v seedVitals) parsedSeed {
	if v.model == "" {
		return seed // nothing authoritative to seed (no assistant turn yet)
	}
	init := NativeEvent{
		Type: EvSessionInit, Model: v.model, Cwd: v.cwd, SessionID: v.sessionID,
		FromReplay: true,
	}
	seed.events = append([]NativeEvent{init}, seed.events...)
	seed.userUUID = append([]string{""}, seed.userUUID...)
	if v.hasUsage {
		usage := NativeEvent{
			Type: EvUsage, FromReplay: true,
			InputTokens: v.usage.input, CachedTokens: v.usage.cacheRead,
			CacheCreationTokens: v.usage.cacheCreation, OutputTokens: v.usage.output,
		}
		seed.events = append(seed.events, usage)
		seed.userUUID = append(seed.userUUID, "")
		// The seeded usage predates a compaction: tag it with the SAME marker the
		// live parser emits for a compact_boundary so the client's ctx% falls back
		// to "measuring" instead of trusting a freed window's old reading. Must be
		// appended AFTER the usage — the client takes the LAST context-relevant
		// event. Text is deliberately EMPTY: the marker exists only to drive the
		// reset, and SlashResultView renders nothing for blank text, so this adds
		// no phantom "compacted" card to the replayed transcript.
		if v.compactAfterUsage {
			seed.events = append(seed.events, NativeEvent{
				Type: EvSlashResult, Subtype: "compact", FromReplay: true,
			})
			seed.userUUID = append(seed.userUUID, "")
		}
	}
	return seed
}

// seedSeqBase is the floor of the Seq block reserved for transcript-seeded
// events in the /native/history response. Live ring events use small monotonic
// seqs (1..N), so a high base keeps the two spaces disjoint — the client dedups
// by Seq, and a collision would silently drop a real live event. Seeded events
// are also placed FIRST in the response array, so they render ahead of the live
// events even though their seqs are numerically larger (the client folds in
// array order, not Seq order).
//
// MUST stay below 2^53: the web client parses Seq as a JS number (float64),
// which represents integers exactly only up to 2^53. A base of 2^62 made every
// seeded Seq round to the SAME float64 value in the browser, so the client's
// Seq-dedup collapsed the ENTIRE seeded history to a single event (only the
// first folded; the rest looked like duplicate seqs). 2^52 keeps every seeded
// Seq an exact, distinct integer (ulp is 1 throughout [2^52, 2^53)) while
// staying far above any realistic live ring seq.
const seedSeqBase uint64 = 1 << 52

// mergeSeedWithRing stamps the seeded (FromReplay) events with disjoint Seqs in
// the seedSeqBase block, drops the seeded user_text that the live ring already
// carries as a replay-echo (so the last prompt isn't rendered twice), and
// returns the seeded events FOLLOWED BY the ring events. The seeded events are
// returned in file order with FromReplay already set by seedFromTranscript.
//
// The dedup is CONSUMABLE by text, not set membership: each live-ring user_text
// drops AT MOST ONE matching seeded user_text. A legitimately repeated prompt
// ("continue", "y") that appears N times in the transcript but only once in the
// ring keeps N-1 seeded copies — the old set-membership dedup collapsed ALL of
// them. (The ring's user_text carries message.id, which is empty for human/
// replay turns, so it cannot be matched to a transcript line's top-level uuid;
// text is the only shared identity across the seed/ring boundary. uuid-precise
// dedup lives in mergeParsedSeedWithRing, which has the per-seed uuids.)
func mergeSeedWithRing(seeded, ring []NativeEvent) []NativeEvent {
	return mergeParsedSeedWithRing(parsedSeed{events: seeded}, ring)
}

// mergeParsedSeedWithRing is the uuid-aware merge. It behaves like
// mergeSeedWithRing but, when given the parse intermediate's per-user uuids,
// dedups by message identity instead of bare text where possible:
//   - It consumes each live-ring user_text against the seeded user_texts by
//     text (the seed/ring boundary shares no uuid — the ring echo has no
//     transcript uuid), dropping at most one seeded copy per ring echo.
//   - Among seeded user_texts of identical text, the uuid distinguishes them so
//     two same-text prompts with DIFFERENT uuids both survive when the ring
//     holds only one echo. With no uuid present it falls back to plain text.
//
// seed.userUUID, when non-nil, is parallel to seed.events (uuid per EvUserText,
// "" otherwise). A nil/short userUUID degrades gracefully to text-only dedup.
func mergeParsedSeedWithRing(seed parsedSeed, ring []NativeEvent) []NativeEvent {
	seeded := seed.events

	// Overlap cut: the live ring is a contiguous SUFFIX of the same conversation
	// the transcript seed reconstructs, so for any turn the ring still holds the
	// seed is a DUPLICATE of it. A hard refresh (Ctrl+Shift+R) of a long-alive pane
	// re-fetches since=0 while the ring still carries the WHOLE conversation, so the
	// seed and ring then overlap on every settled turn. The old merge only deduped
	// user_text, so the overlap's tool calls / answers RENDERED TWICE and — because
	// the surviving prompt was the ring's (live, non-replay) copy while its answer
	// was hoisted to the front as seeded history — the prompt rendered BELOW its own
	// answer. Trim the seed at the point the ring's coverage begins so each
	// overlapping turn is served exactly once, from the ring (its authoritative live
	// copy: real timing, streaming state, correct ordering). The seeded prompt of the
	// boundary turn, kept just before the cut, is dropped by the user_text dedup below
	// (the ring carries it), so the overlap turn comes wholly from the ring.
	if cut := seedRingOverlapCut(seeded, ring); cut < len(seeded) {
		seeded = seeded[:cut]
	}

	// Multiset of live-ring user_text payloads: value = how many seeded copies
	// this text may still consume (one per ring occurrence).
	ringUserTexts := make(map[string]int)
	for _, ev := range ring {
		if ev.Type == EvUserText && ev.Text != "" {
			ringUserTexts[ev.Text]++
		}
	}

	out := make([]NativeEvent, 0, len(seeded)+len(ring))
	seq := seedSeqBase
	for i, ev := range seeded {
		if ev.Type == EvUserText {
			if n := ringUserTexts[ev.Text]; n > 0 {
				// This seeded prompt is the ring's replay-echo; drop one copy.
				// The uuid (when present) just confirms identity; the decision is
				// the consumable text match since the echo carries no uuid.
				ringUserTexts[ev.Text] = n - 1
				_ = i // uuid is seed.userUUID[i]; retained for identity, not needed to decide here
				continue
			}
		}
		ev.Seq = seq
		seq++
		out = append(out, ev)
	}
	out = append(out, ring...)
	return out
}

// seedRingOverlapCut returns the index in `seeded` at which the live ring's
// coverage begins — i.e. the first seeded event whose UNIQUE identity (a tool
// callId or an assistant messageId) also appears in `ring`. The ring is always a
// contiguous suffix of the conversation, so every seeded event from that index on
// is a turn the ring already holds and must be dropped from the seed (served once,
// from the ring). Returns len(seeded) when there is no overlap — ring empty
// (post-resume: the seed is the only history) or ring entirely newer than the
// transcript (a just-typed turn not yet flushed to disk) — so the whole seed is
// kept. Pure + unit-tested.
func seedRingOverlapCut(seeded, ring []NativeEvent) int {
	ringKeys := make(map[string]struct{}, len(ring))
	for i := range ring {
		if k := overlapKey(&ring[i]); k != "" {
			ringKeys[k] = struct{}{}
		}
	}
	if len(ringKeys) == 0 {
		return len(seeded)
	}
	for i := range seeded {
		if k := overlapKey(&seeded[i]); k != "" {
			if _, ok := ringKeys[k]; ok {
				return i
			}
		}
	}
	return len(seeded)
}

// overlapKey returns a globally-unique identity for the event types that can
// anchor the seed↔ring overlap boundary: a tool call/result by its callId, or an
// assistant text/thinking block by its messageId. Both ids originate in the
// Anthropic message stream and are unique across a conversation, so a seed/ring
// match is a TRUE overlap (never a coincidence). Returns "" for events that can't
// anchor: user_text (a repeated prompt like "yes"/"continue" isn't unique — those
// are handled by the consumable text dedup instead) and the synthetic
// session_init/usage/status (no stable id). The live ring carries finalized
// text/thinking/tool_call blocks (mapAssistant stamps message.id / tool id), the
// same identities seedFromTranscript assigns, so the two join cleanly.
func overlapKey(ev *NativeEvent) string {
	switch ev.Type {
	case EvToolCall, EvToolResult:
		if ev.CallID != "" {
			return "c:" + ev.CallID
		}
	case EvText, EvThinking:
		if ev.MessageID != "" {
			return "m:" + ev.MessageID
		}
	}
	return ""
}

// claudeInterruptMarkerPrefix is the text Claude Code writes as a `user` record
// when the USER stops a turn: "[Request interrupted by user]" and "[Request
// interrupted by user for tool use]" (both observed on disk; the latter follows
// the tool_result of the tool that was cancelled). It is POSITIVE evidence that
// a stop was deliberate, which is the only thing separating "you interrupted
// me" from "a restart killed me" — see claudeTranscriptTail.awaitsReply. Match
// on the block text's PREFIX, never a substring of the whole record: a /compact
// summary quotes the marker inside a much longer text and must not read as an
// interrupt.
const claudeInterruptMarkerPrefix = "[Request interrupted by user"

// claudeTranscriptTail is how a transcript ENDS — everything a resume needs to
// know, gathered in ONE pass because transcripts reach tens of MB (58 MB in the
// wild) and a resume can't afford to read one twice.
type claudeTranscriptTail struct {
	// unmatched are tool_use ids from the last tool-bearing assistant message
	// that never got a tool_result: the sequence is invalid and `--resume` would
	// 400 forever until repaired.
	unmatched []string
	// lastAsstUUID parents the synthetic repair record.
	lastAsstUUID string

	// awaitsReply reports that the transcript ends OWING an assistant reply: a
	// real user turn (or a tool_result, which the API requires the assistant to
	// answer) with no assistant message and no user interrupt after it. Since a
	// deliberate stop ALWAYS writes an interrupt marker, a transcript left in
	// this state means the agent died before it could answer — a restart, not a
	// choice. This is the mid-TEXT twin of `unmatched`: an agent killed while
	// streaming prose writes no assistant record at all (verified against claude
	// 2.1.211), so there is no dangling tool_use to repair and nothing else on
	// disk marks the loss.
	awaitsReply bool

	// meta for the synthetic repair record.
	metaCwd, metaSession, metaGitBranch, metaVersion string
}

// claudeUserTurnKind classifies a transcript `user` record for the awaits-reply
// state machine. The `user` type is overloaded on disk: real human turns share
// it with tool results, interrupt markers, and synthetic stubs (compaction
// notices, command stdout, task notifications) that are NOT turns anyone is
// waiting on.
type claudeUserTurnKind int

const (
	// claudeUserIgnore is a synthetic/meta record: a `<local-command-stdout>
	// Compacted…` stub, a hook caveat, a promptSource=system task notification.
	// Critically, the compaction stub ends real, healthy, IDLE sessions — reading
	// one as a pending turn would show "a restart interrupted you" to a user who
	// simply ran /compact and walked away.
	claudeUserIgnore     claudeUserTurnKind = iota
	claudeUserPrompt                        // a real human turn — the assistant owes a reply
	claudeUserToolResult                    // a tool result — the API REQUIRES an assistant reply next
	claudeUserInterrupt                     // "[Request interrupted by user…]" — a deliberate stop
)

// isRealClaudeUserTurn applies mapTranscriptUser's real-human-turn gate: a turn
// someone is actually waiting on carries promptSource typed/sdk/queued and none
// of the meta flags. promptSource=system (task notifications) and absent
// (compaction stubs, command stdout) are machinery, not turns.
func isRealClaudeUserTurn(promptSource string, isMeta, isCompactSummary, isVisibleOnly bool) bool {
	if isMeta || isCompactSummary || isVisibleOnly {
		return false
	}
	switch promptSource {
	case "typed", "sdk", "queued":
		return true
	default:
		return false
	}
}

// classifyClaudeUserRecord sorts a transcript `user` record for the awaits-reply
// machine. Content comes in BOTH shapes on disk and a human turn can wear
// either: a typed turn records `content:"text"`, while a turn submitted as
// content blocks records `content:[{type:"text"}]` with promptSource=sdk
// (verified by killing a real headless agent). Blocks are checked for the
// interrupt marker FIRST — it is the one shape that must never be mistaken for
// a pending turn.
func classifyClaudeUserRecord(promptSource string, isMeta, isCompactSummary, isVisibleOnly bool, message json.RawMessage) claudeUserTurnKind {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(message, &m) != nil || len(m.Content) == 0 {
		return claudeUserIgnore
	}
	// String content => a human turn, or a synthetic stub wearing one's clothes.
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		if !isRealClaudeUserTurn(promptSource, isMeta, isCompactSummary, isVisibleOnly) || strings.TrimSpace(s) == "" {
			return claudeUserIgnore
		}
		return claudeUserPrompt
	}
	// List content => the interrupt marker, tool_result blocks, or a block-shaped
	// human turn.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		return claudeUserIgnore
	}
	for _, b := range blocks {
		if b.Type == "text" && strings.HasPrefix(strings.TrimSpace(b.Text), claudeInterruptMarkerPrefix) {
			return claudeUserInterrupt
		}
	}
	for _, b := range blocks {
		if b.Type == "tool_result" {
			return claudeUserToolResult
		}
	}
	if !isRealClaudeUserTurn(promptSource, isMeta, isCompactSummary, isVisibleOnly) {
		return claudeUserIgnore
	}
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			return claudeUserPrompt
		}
	}
	return claudeUserIgnore
}

// scanClaudeTranscriptTail reads the transcript once and reports how it ends.
// Pure: it never writes.
func scanClaudeTranscriptTail(path string) (claudeTranscriptTail, error) {
	var tail claudeTranscriptTail
	f, err := os.Open(path)
	if err != nil {
		return tail, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	toolResultIDs := map[string]struct{}{}
	var lastToolUseIDs []string

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rl struct {
			Type                      string          `json:"type"`
			UUID                      string          `json:"uuid"`
			SessionID                 string          `json:"sessionId"`
			Cwd                       string          `json:"cwd"`
			GitBranch                 string          `json:"gitBranch"`
			Version                   string          `json:"version"`
			IsSidechain               bool            `json:"isSidechain"`
			PromptSource              string          `json:"promptSource"`
			IsMeta                    bool            `json:"isMeta"`
			IsCompactSummary          bool            `json:"isCompactSummary"`
			IsVisibleInTranscriptOnly bool            `json:"isVisibleInTranscriptOnly"`
			Message                   json.RawMessage `json:"message"`
		}
		if json.Unmarshal(line, &rl) != nil {
			continue
		}
		if rl.Cwd != "" {
			tail.metaCwd = rl.Cwd
		}
		if rl.SessionID != "" {
			tail.metaSession = rl.SessionID
		}
		if rl.GitBranch != "" {
			tail.metaGitBranch = rl.GitBranch
		}
		if rl.Version != "" {
			tail.metaVersion = rl.Version
		}
		switch rl.Type {
		case "assistant":
			if ids := assistantToolUseIDs(rl.Message); len(ids) > 0 {
				lastToolUseIDs = ids
				tail.lastAsstUUID = rl.UUID
			}
			// The agent produced a COMPLETE message, so it no longer owes one.
			// A partial stream is never written (a killed agent leaves no
			// assistant record), so this can't clear on a half-finished reply.
			// Sidechain (subagent) records are skipped for the awaits-reply
			// machine only — a subagent's internals must not settle the main
			// turn — while the repair inputs above stay byte-identical to the
			// long-proven behavior.
			if !rl.IsSidechain {
				tail.awaitsReply = false
			}
		case "user":
			for _, id := range userToolResultIDs(rl.Message) {
				toolResultIDs[id] = struct{}{}
			}
			if rl.IsSidechain {
				break
			}
			switch classifyClaudeUserRecord(rl.PromptSource, rl.IsMeta, rl.IsCompactSummary, rl.IsVisibleInTranscriptOnly, rl.Message) {
			case claudeUserPrompt, claudeUserToolResult:
				tail.awaitsReply = true
			case claudeUserInterrupt:
				tail.awaitsReply = false
			}
		}
	}
	if err := sc.Err(); err != nil {
		return claudeTranscriptTail{}, err
	}
	for _, id := range lastToolUseIDs {
		if _, ok := toolResultIDs[id]; !ok {
			tail.unmatched = append(tail.unmatched, id)
		}
	}
	return tail, nil
}

// appendDanglingToolUseRepair writes the synthetic deny tool_result(s) that make
// a transcript ending in an unanswered tool_use loadable again.
func appendDanglingToolUseRepair(path string, tail claudeTranscriptTail) error {
	const interruptedMsg = "The previous request was interrupted before it could be answered. The session was resumed; ask again if you still need an answer."
	blocks := make([]map[string]any, 0, len(tail.unmatched))
	for _, id := range tail.unmatched {
		blocks = append(blocks, map[string]any{
			"tool_use_id": id,
			"type":        "tool_result",
			"content":     interruptedMsg,
			"is_error":    true,
		})
	}
	rec := map[string]any{
		"parentUuid":    tail.lastAsstUUID,
		"isSidechain":   false,
		"type":          "user",
		"message":       map[string]any{"role": "user", "content": blocks},
		"uuid":          newTranscriptUUID(),
		"timestamp":     time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"toolUseResult": "Error: " + interruptedMsg,
		"userType":      "external",
		"entrypoint":    "sdk-cli",
		"cwd":           tail.metaCwd,
		"sessionId":     tail.metaSession,
		"version":       tail.metaVersion,
		"gitBranch":     tail.metaGitBranch,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := out.Write(append(data, '\n')); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// claudeResumeState is what one transcript pass tells a resume about the
// previous session's last turn.
type claudeResumeState struct {
	// repairedToolUse: the transcript ended with an unanswered tool_use and we
	// appended a synthetic deny so `--resume` loads a VALID sequence.
	repairedToolUse bool
	// awaitsReply: the transcript ends owing an assistant reply — the agent died
	// before answering (see claudeTranscriptTail.awaitsReply).
	awaitsReply bool
}

// prepareClaudeResume self-heals a native session before it is resumed and
// reports what to tell the user — in ONE transcript pass.
//
// REPAIR: if the transcript ends with an assistant tool_use that was NEVER
// answered — the previous agent died mid-approval, e.g. an AskUserQuestion whose
// answer was lost when the connection dropped — the message sequence is invalid
// (the Anthropic API requires every tool_use to be followed by a tool_result),
// so the next `claude --resume` user turn 400s and POISONS the session forever,
// and the unanswered turn never settles (the UI spins on "thinking" with no
// recovery). Appending a synthetic deny tool_result per unanswered tool_use lets
// the resumed agent load a VALID transcript and settle the turn as "interrupted".
//
// Correctness: a session cannot progress past an unanswered tool_use, so only
// the most-recent tool-bearing assistant turn can be dangling; parallel tool
// calls in that turn are all completed in one appended user message. Safe to run
// with no agent attached (the prior agent is dead at resume time); the append is
// a single small line, atomic under O_APPEND.
//
// NOTICE: the repair only fires when a TOOL was pending. An agent killed while
// streaming plain text or thinking writes no assistant record at all, leaving a
// transcript that simply stops after the user's prompt — nothing to repair, and
// (before awaitsReply) nothing to tell the user, so the response just vanished.
// Both cases now report the same "a restart interrupted the previous response"
// notice. A missing transcript, or one that ends healthily, is a no-op.
func prepareClaudeResume(cwd, sessionID string) (claudeResumeState, error) {
	var st claudeResumeState
	path := resolveTranscriptPath(cwd, sessionID)
	if path == "" {
		return st, nil
	}
	tail, err := scanClaudeTranscriptTail(path)
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return st, err
	}
	if len(tail.unmatched) > 0 {
		if err := appendDanglingToolUseRepair(path, tail); err != nil {
			return st, err
		}
		st.repairedToolUse = true
		return st, nil
	}
	st.awaitsReply = tail.awaitsReply
	return st, nil
}

// assistantToolUseIDs returns the tool_use block ids in an assistant message.
func assistantToolUseIDs(raw json.RawMessage) []string {
	var m struct {
		Content []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	var ids []string
	for _, b := range m.Content {
		if b.Type == "tool_use" && b.ID != "" {
			ids = append(ids, b.ID)
		}
	}
	return ids
}

// userToolResultIDs returns the tool_use_ids a user message answers. A string
// content (a human turn) yields none.
func userToolResultIDs(raw json.RawMessage) []string {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	var blocks []struct {
		Type      string `json:"type"`
		ToolUseID string `json:"tool_use_id"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		return nil
	}
	var ids []string
	for _, b := range blocks {
		if b.Type == "tool_result" && b.ToolUseID != "" {
			ids = append(ids, b.ToolUseID)
		}
	}
	return ids
}

// newTranscriptUUID returns a random RFC-4122 v4 UUID for a synthetic transcript
// line (the on-disk format the Claude CLI uses for its uuid/parentUuid fields).
func newTranscriptUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// transcriptLine is the minimal top-level shape of a Claude on-disk transcript
// line needed for the R1b allow-list.
type transcriptLine struct {
	Type    string          `json:"type"`
	Message json.RawMessage `json:"message"`

	// UUID is the per-line message identity (top-level "uuid" on every transcript
	// line; user turns have NO message.id, so this is their only stable id). It is
	// carried INTERNALLY by the seeding path (parsedSeed) for replay-echo dedup and
	// is never placed on the wire NativeEvent.
	UUID string `json:"uuid"`

	// user-line gating (transcript vocab; differs from live stream-json)
	PromptSource              string `json:"promptSource"`
	IsMeta                    bool   `json:"isMeta"`
	IsCompactSummary          bool   `json:"isCompactSummary"`
	IsVisibleInTranscriptOnly bool   `json:"isVisibleInTranscriptOnly"`

	// assistant sub-agent threading
	ParentToolUseID string `json:"parentToolUseId"`
}

// mapTranscriptLine applies the R1b allow-list to ONE transcript JSONL line and
// returns 0..n NativeEvents. Malformed JSON or any unrecognized/dropped line
// returns nil — never panics.
func mapTranscriptLine(line []byte) []NativeEvent {
	evs, _ := mapTranscriptLineWithMeta(line)
	return evs
}

// mapTranscriptLineWithMeta is mapTranscriptLine plus the line's top-level uuid
// (the per-prompt identity the seeding path carries internally for dedup). The
// uuid is "" for a malformed/unknown line or one without a uuid field.
func mapTranscriptLineWithMeta(line []byte) ([]NativeEvent, string) {
	var t transcriptLine
	if err := json.Unmarshal(line, &t); err != nil {
		return nil, ""
	}
	switch t.Type {
	case "assistant":
		return mapTranscriptAssistant(t.Message, t.ParentToolUseID), t.UUID
	case "user":
		return mapTranscriptUser(&t), t.UUID
	default:
		// mode / permission-mode / ai-title / last-prompt /
		// file-history-snapshot / queue-operation / attachment / system / ...
		return nil, ""
	}
}

// mapTranscriptAssistant fans out an assistant message's content blocks. Mirrors
// ClaudeAdapter.mapAssistant but does NOT emit a usage event (transcript usage
// is cumulative and would mismatch the live delta-based usage stream).
func mapTranscriptAssistant(raw json.RawMessage, parentToolID string) []NativeEvent {
	var m struct {
		ID      string `json:"id"`
		Content []struct {
			Type      string          `json:"type"` // text|thinking|tool_use
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			Signature string          `json:"signature"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	var out []NativeEvent
	for _, b := range m.Content {
		switch b.Type {
		case "text":
			if b.Text == "" {
				continue
			}
			out = append(out, NativeEvent{Type: EvText, Text: b.Text, MessageID: m.ID})
		case "thinking":
			// Emit only if there is real thinking text (redacted blocks are empty).
			if b.Thinking == "" {
				continue
			}
			out = append(out, NativeEvent{Type: EvThinking, Text: b.Thinking, Signature: b.Signature, MessageID: m.ID})
		case "tool_use":
			out = append(out, NativeEvent{
				Type: EvToolCall, CallID: b.ID, Name: b.Name, Args: b.Input,
				ToolKind: classifyTool(b.Name), ParentCallID: parentToolID, MessageID: m.ID,
			})
		}
	}
	return out
}

// mapTranscriptUser maps a transcript user line. message.content is EITHER a
// string (a human turn -> EvUserText, gated by the promptSource/meta allow-list)
// OR a list of tool_result blocks (-> EvToolResult, paired to its tool_call by
// tool_use_id).
func mapTranscriptUser(t *transcriptLine) []NativeEvent {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(t.Message, &m); err != nil || len(m.Content) == 0 {
		return nil
	}

	// String content => a human turn.
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		// DROP synthetic/meta user lines: compact summaries, hook caveats,
		// XML-tag command stdout. Real human turns have promptSource typed/sdk/
		// queued and none of the meta flags set.
		if t.IsMeta || t.IsCompactSummary || t.IsVisibleInTranscriptOnly {
			return nil
		}
		switch t.PromptSource {
		case "typed", "sdk", "queued":
		default:
			return nil
		}
		if s == "" {
			return nil
		}
		if ctx, user, ok := splitAmbientDelegation(s); ok {
			return []NativeEvent{{Type: EvUserText, Text: ctx}, {Type: EvUserText, Text: user}}
		}
		return []NativeEvent{{Type: EvUserText, Text: s}}
	}

	// List content => tool_result blocks.
	var blocks []struct {
		Type      string          `json:"type"`
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"` // string OR list of {type:text|image}
		IsError   bool            `json:"is_error"`
	}
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return nil
	}
	var out []NativeEvent
	for _, b := range blocks {
		if b.Type != "tool_result" {
			continue
		}
		out = append(out, NativeEvent{
			Type: EvToolResult, CallID: b.ToolUseID, IsError: b.IsError, Content: unquoteContent(b.Content),
		})
	}
	return out
}
