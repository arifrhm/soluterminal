package main

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// sampleTranscript is a small hand-built Claude on-disk transcript exercising
// the R1b allow-list: a real user turn, an assistant text + tool_use, a
// tool_result, and a spread of noise lines that MUST be dropped (mode,
// permission-mode, ai-title, file-history-snapshot, queue-operation, an
// attachment, a system line, a meta user line, a compact-summary user line, an
// empty-thinking assistant block, and a malformed/unknown line).
const sampleTranscript = `{"type":"mode","mode":"default"}
{"type":"permission-mode","permissionMode":"acceptEdits"}
{"type":"ai-title","title":"Some title"}
{"type":"last-prompt","prompt":"x"}
{"type":"file-history-snapshot","files":[]}
{"type":"queue-operation","op":"enqueue"}
{"type":"attachment","subtype":"hook_success"}
{"type":"system","subtype":"init"}
{"type":"user","promptSource":"typed","message":{"role":"user","content":"hello there"}}
{"type":"user","promptSource":null,"isMeta":true,"message":{"role":"user","content":"<local-command-caveat>noise</local-command-caveat>"}}
{"type":"user","isCompactSummary":true,"message":{"role":"user","content":"summary of prior conversation"}}
{"type":"assistant","message":{"id":"msg_1","content":[{"type":"thinking","thinking":""},{"type":"text","text":"Hi! Running a command."},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}],"usage":{"input_tokens":10,"output_tokens":5}}}
{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_1","type":"tool_result","content":"file.txt","is_error":false}]}}
this is not json at all
{"type":"some_future_type_2099","foo":"bar"}
`

func TestSeedFromTranscriptMapsAllowList(t *testing.T) {
	// Build an isolated config dir + project transcript and point
	// CLAUDE_CONFIG_DIR at it so claudeConfigDirs() resolves to it.
	cfg := t.TempDir()
	cwd := "/home/user/wsl-projects/some-proj"
	slug := transcriptSlug(cwd)
	sessionID := "abc-123-session"
	projDir := filepath.Join(cfg, "projects", slug)
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, sessionID+".jsonl"), []byte(sampleTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)

	got, err := seedFromTranscript(cwd, sessionID)
	if err != nil {
		t.Fatalf("seedFromTranscript err = %v", err)
	}

	want := []NativeEvent{
		{Type: EvUserText, Text: "hello there", FromReplay: true},
		{Type: EvText, Text: "Hi! Running a command.", MessageID: "msg_1", FromReplay: true},
		{Type: EvToolCall, CallID: "toolu_1", Name: "Bash", ToolKind: "exec", MessageID: "msg_1", Args: raw(`{"command":"ls"}`), FromReplay: true},
		{Type: EvToolResult, CallID: "toolu_1", Content: "file.txt", FromReplay: true},
	}
	assertEventsEqual(t, got, want)
}

// TestSeedSynthesizesVitalsFromTranscript proves the Focus bottom-bar fix:
// a resumed pane's seed PREPENDS a FromReplay session_init (model/cwd) and
// APPENDS a FromReplay usage (last assistant token occupancy), so the bar shows
// model + context% INSTANTLY without waiting for the first live turn. The LAST
// assistant model/usage wins (most-recent context). A model-less transcript adds
// neither (covered by TestSeedFromTranscriptMapsAllowList, whose fixture has no
// model on its assistant line).
func TestSeedSynthesizesVitalsFromTranscript(t *testing.T) {
	cwd := "/home/user/wsl-projects/vitals-proj"
	sessionID := "vitals-session"
	// Two assistant turns; the LAST model + usage must win (5000 input, etc.).
	body := `{"type":"user","promptSource":"typed","message":{"role":"user","content":"hi"}}
{"type":"assistant","cwd":"/home/user/wsl-projects/vitals-proj","sessionId":"vitals-session","message":{"id":"m1","model":"claude-sonnet-4-6","content":[{"type":"text","text":"first"}],"usage":{"input_tokens":100,"cache_read_input_tokens":200,"cache_creation_input_tokens":50,"output_tokens":10}}}
{"type":"assistant","cwd":"/home/user/wsl-projects/vitals-proj","sessionId":"vitals-session","message":{"id":"m2","model":"claude-opus-4-8","content":[{"type":"text","text":"second"}],"usage":{"input_tokens":5000,"cache_read_input_tokens":12000,"cache_creation_input_tokens":3000,"output_tokens":42}}}
`
	writeSessionTranscript(t, t.TempDir(), cwd, sessionID, body)

	got, err := seedFromTranscript(cwd, sessionID)
	if err != nil {
		t.Fatalf("seedFromTranscript err = %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("want >=2 seeded events, got %d", len(got))
	}

	// First event: synthesized session_init carrying the LAST model + cwd.
	init := got[0]
	if init.Type != EvSessionInit || !init.FromReplay {
		t.Fatalf("first event = %+v; want FromReplay session_init", init)
	}
	if init.Model != "claude-opus-4-8" {
		t.Errorf("init.Model = %q; want claude-opus-4-8 (last assistant wins)", init.Model)
	}
	if init.Cwd != cwd {
		t.Errorf("init.Cwd = %q; want %q", init.Cwd, cwd)
	}
	if init.SessionID != sessionID {
		t.Errorf("init.SessionID = %q; want %q", init.SessionID, sessionID)
	}

	// Last event: synthesized usage carrying the LAST assistant occupancy.
	usage := got[len(got)-1]
	if usage.Type != EvUsage || !usage.FromReplay {
		t.Fatalf("last event = %+v; want FromReplay usage", usage)
	}
	if usage.InputTokens != 5000 || usage.CachedTokens != 12000 ||
		usage.CacheCreationTokens != 3000 || usage.OutputTokens != 42 {
		t.Errorf("usage tokens = in%d cache%d create%d out%d; want 5000/12000/3000/42",
			usage.InputTokens, usage.CachedTokens, usage.CacheCreationTokens, usage.OutputTokens)
	}
}

// TestSeedTagsCompactAfterLastUsage proves a /compact that lands AFTER the last
// assistant turn makes the seed emit the same compact marker the live parser
// emits for a compact_boundary. Without it, a reload taken before the next turn
// replays the PRE-compact usage as if it were current and the ctx% chip
// resurrects the stale (freed) reading. The record shape mirrors a real
// transcript: type:"user" + isCompactSummary, carrying no usage of its own.
func TestSeedTagsCompactAfterLastUsage(t *testing.T) {
	cwd := "/home/user/wsl-projects/compact-proj"
	sessionID := "compact-session"
	body := `{"type":"user","promptSource":"typed","message":{"role":"user","content":"hi"}}
{"type":"assistant","cwd":"/home/user/wsl-projects/compact-proj","sessionId":"compact-session","message":{"id":"m1","model":"claude-opus-4-8","content":[{"type":"text","text":"first"}],"usage":{"input_tokens":5000,"cache_read_input_tokens":12000,"cache_creation_input_tokens":3000,"output_tokens":42}}}
{"type":"user","isCompactSummary":true,"isVisibleInTranscriptOnly":true,"message":{"role":"user","content":"This session is being continued from a previous conversation…"}}
`
	writeSessionTranscript(t, t.TempDir(), cwd, sessionID, body)

	got, err := seedFromTranscript(cwd, sessionID)
	if err != nil {
		t.Fatalf("seedFromTranscript err = %v", err)
	}
	last := got[len(got)-1]
	if last.Type != EvSlashResult || last.Subtype != "compact" || !last.FromReplay {
		t.Fatalf("last seeded event = %+v; want a FromReplay slash_result subtype=compact", last)
	}
	// Blank on purpose: it drives the client's ctx% reset, and SlashResultView
	// renders nothing for empty text, so no phantom card joins the transcript.
	if last.Text != "" {
		t.Errorf("marker Text = %q; want empty (reset-only, renders nothing)", last.Text)
	}
	// It must trail the usage — the client takes the LAST context-relevant event,
	// so a marker emitted before the usage would be immediately superseded.
	usage := got[len(got)-2]
	if usage.Type != EvUsage {
		t.Fatalf("second-to-last = %+v; want the seeded usage before the marker", usage)
	}
}

func TestSeedUsesCompactBoundaryPostTokens(t *testing.T) {
	cwd := "/home/user/wsl-projects/compact-boundary-proj"
	sessionID := "compact-boundary-session"
	body := `{"type":"assistant","cwd":"/home/user/wsl-projects/compact-boundary-proj","sessionId":"compact-boundary-session","message":{"id":"m1","model":"claude-opus-4-8","content":[{"type":"text","text":"pre"}],"usage":{"input_tokens":5000,"cache_read_input_tokens":12000,"cache_creation_input_tokens":3000,"output_tokens":42}}}
{"type":"system","subtype":"compact_boundary","compactMetadata":{"preTokens":20000,"postTokens":9514}}
{"type":"user","isCompactSummary":true,"isVisibleInTranscriptOnly":true,"message":{"role":"user","content":"summary"}}
`
	writeSessionTranscript(t, t.TempDir(), cwd, sessionID, body)

	got, err := seedFromTranscript(cwd, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		if e.Type == EvSlashResult && e.Subtype == "compact" {
			t.Fatalf("postTokens boundary emitted stale compact marker: %+v", got)
		}
	}
	last := got[len(got)-1]
	if last.Type != EvUsage || last.InputTokens != 9514 || last.CachedTokens != 0 || last.CacheCreationTokens != 0 {
		t.Fatalf("last=%+v, want clean post-compact usage inputTokens=9514", last)
	}
}

func TestSeedCompactBoundaryWithoutPostTokensKeepsMarker(t *testing.T) {
	cwd := "/home/user/wsl-projects/compact-boundary-old-proj"
	sessionID := "compact-boundary-old-session"
	body := `{"type":"assistant","cwd":"/home/user/wsl-projects/compact-boundary-old-proj","sessionId":"compact-boundary-old-session","message":{"id":"m1","model":"claude-opus-4-8","content":[{"type":"text","text":"pre"}],"usage":{"input_tokens":5000,"output_tokens":42}}}
{"type":"system","subtype":"compact_boundary"}
{"type":"user","isCompactSummary":true,"isVisibleInTranscriptOnly":true,"message":{"role":"user","content":"summary"}}
`
	writeSessionTranscript(t, t.TempDir(), cwd, sessionID, body)

	got, err := seedFromTranscript(cwd, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	last := got[len(got)-1]
	if last.Type != EvSlashResult || last.Subtype != "compact" {
		t.Fatalf("last=%+v, want compact fallback marker", last)
	}
}

func TestSeedReplayInitPrefersExplicitPaneModel(t *testing.T) {
	cwd := "/home/user/wsl-projects/model-override-proj"
	sessionID := "model-override-session"
	body := `{"type":"assistant","cwd":"/home/user/wsl-projects/model-override-proj","sessionId":"model-override-session","message":{"id":"m1","model":"claude-sonnet-4-6","content":[{"type":"text","text":"old model"}],"usage":{"input_tokens":100}}}
`
	writeSessionTranscript(t, t.TempDir(), cwd, sessionID, body)

	overridden, err := seedFromTranscript(cwd, sessionID, "claude-opus-4-8")
	if err != nil {
		t.Fatal(err)
	}
	if overridden[0].Type != EvSessionInit || overridden[0].Model != "claude-opus-4-8" {
		t.Fatalf("overridden init=%+v, want explicit pane model", overridden[0])
	}
	plain, err := seedFromTranscript(cwd, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if plain[0].Model != "claude-sonnet-4-6" {
		t.Fatalf("plain init=%+v, want transcript model fallback", plain[0])
	}
}

// TestSeedCompactTagSurvivesReasoningOnlyUsage guards the occ>0 gate: only a
// usage that actually RE-MEASURED the window may clear the compaction marker.
// A reasoning-only record (no input/cache tokens — notably GLM's streaming
// thinking pings, which arrive by the hundreds) carries no window reading, so
// treating it as a re-measure would clear the marker and let the stale
// pre-compact % back onto the bar. The client shipped exactly this bug once
// (occ→0 zeroed its ctx reading); don't reintroduce it on the seed side.
func TestSeedCompactTagSurvivesReasoningOnlyUsage(t *testing.T) {
	cwd := "/home/user/wsl-projects/compact-proj3"
	sessionID := "compact-session3"
	body := `{"type":"assistant","cwd":"/home/user/wsl-projects/compact-proj3","sessionId":"compact-session3","message":{"id":"m1","model":"claude-opus-4-8","content":[{"type":"text","text":"pre"}],"usage":{"input_tokens":5000,"cache_read_input_tokens":12000,"cache_creation_input_tokens":3000,"output_tokens":42}}}
{"type":"user","isCompactSummary":true,"isVisibleInTranscriptOnly":true,"message":{"role":"user","content":"summary"}}
{"type":"assistant","cwd":"/home/user/wsl-projects/compact-proj3","sessionId":"compact-session3","message":{"id":"m2","model":"claude-opus-4-8","content":[{"type":"text","text":"thinking"}],"usage":{"input_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":7}}}
`
	writeSessionTranscript(t, t.TempDir(), cwd, sessionID, body)

	got, err := seedFromTranscript(cwd, sessionID)
	if err != nil {
		t.Fatalf("seedFromTranscript err = %v", err)
	}
	last := got[len(got)-1]
	if last.Type != EvSlashResult || last.Subtype != "compact" {
		t.Fatalf("last seeded event = %+v; want the compact marker to SURVIVE a reasoning-only usage", last)
	}
}

// TestSeedNoCompactTagWhenTurnFollows is the twin: a real turn AFTER the
// compaction re-measured the window, so the seeded usage is trustworthy again
// and NO marker may be emitted (else the chip would sit at "—" forever).
func TestSeedNoCompactTagWhenTurnFollows(t *testing.T) {
	cwd := "/home/user/wsl-projects/compact-proj2"
	sessionID := "compact-session2"
	body := `{"type":"assistant","cwd":"/home/user/wsl-projects/compact-proj2","sessionId":"compact-session2","message":{"id":"m1","model":"claude-opus-4-8","content":[{"type":"text","text":"pre"}],"usage":{"input_tokens":5000,"cache_read_input_tokens":12000,"cache_creation_input_tokens":3000,"output_tokens":42}}}
{"type":"user","isCompactSummary":true,"isVisibleInTranscriptOnly":true,"message":{"role":"user","content":"summary"}}
{"type":"assistant","cwd":"/home/user/wsl-projects/compact-proj2","sessionId":"compact-session2","message":{"id":"m2","model":"claude-opus-4-8","content":[{"type":"text","text":"post"}],"usage":{"input_tokens":800,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":5}}}
`
	writeSessionTranscript(t, t.TempDir(), cwd, sessionID, body)

	got, err := seedFromTranscript(cwd, sessionID)
	if err != nil {
		t.Fatalf("seedFromTranscript err = %v", err)
	}
	for _, e := range got {
		if e.Type == EvSlashResult && e.Subtype == "compact" {
			t.Fatalf("emitted a compact marker even though a turn re-measured after it: %+v", e)
		}
	}
	last := got[len(got)-1]
	if last.Type != EvUsage || last.InputTokens != 800 {
		t.Fatalf("last = %+v; want the POST-compact usage (input 800)", last)
	}
}

// TestSeedSynthesizesNoVitalsWithoutModel proves a tool-only / model-less
// transcript adds NO synthetic session_init or usage (a brand-new pane that has
// no assistant turn yet must be untouched).
func TestSeedSynthesizesNoVitalsWithoutModel(t *testing.T) {
	cwd := "/home/user/wsl-projects/novitals-proj"
	sessionID := "novitals-session"
	body := `{"type":"user","promptSource":"typed","message":{"role":"user","content":"hi"}}
`
	writeSessionTranscript(t, t.TempDir(), cwd, sessionID, body)
	got, err := seedFromTranscript(cwd, sessionID)
	if err != nil {
		t.Fatalf("seedFromTranscript err = %v", err)
	}
	for _, e := range got {
		if e.Type == EvSessionInit || e.Type == EvUsage {
			t.Fatalf("unexpected synthesized %s in a model-less seed: %+v", e.Type, e)
		}
	}
}

// TestSeedFromTranscriptSlugMunging verifies the project-dir slug munging the
// path resolver depends on (every non-alnum char -> '-').
func TestSeedFromTranscriptSlugMunging(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"/home/user/wsl-projects/soluterminal": "-home-user-wsl-projects-soluterminal",
		"/tmp/tmp.0YlvFA0qgA":                  "-tmp-tmp-0YlvFA0qgA",
	}
	for in, want := range cases {
		if got := transcriptSlug(in); got != want {
			t.Errorf("transcriptSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSeedFromTranscriptMissingFile asserts a missing transcript is NOT an error.
func TestSeedFromTranscriptMissingFile(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	got, err := seedFromTranscript("/no/such/cwd", "no-session")
	if err != nil {
		t.Fatalf("missing file: err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("missing file: got %d events, want 0", len(got))
	}
	// Empty inputs short-circuit too.
	if got, err := seedFromTranscript("", ""); err != nil || got != nil {
		t.Fatalf("empty inputs: got (%v,%v), want (nil,nil)", got, err)
	}
}

// TestMapTranscriptLineRobustToGarbage proves a malformed or unknown line never
// crashes and yields no events.
func TestMapTranscriptLineRobustToGarbage(t *testing.T) {
	t.Parallel()
	for _, line := range []string{
		`not json`,
		`{"type":"some_future_type_2099"}`,
		`{"type":"assistant"}`,                    // no message
		`{"type":"user"}`,                         // no message
		`{"type":"user","message":{"content":1}}`, // content is a number
		``,
	} {
		if got := mapTranscriptLine([]byte(line)); got != nil {
			t.Errorf("mapTranscriptLine(%q) = %+v, want nil", line, got)
		}
	}
}

// TestMergeSeedWithRingDedupsUserEcho verifies seeded events get disjoint high
// seqs, precede the ring events, and a seeded user_text matching a live ring
// user_text (the replay echo) is dropped to avoid showing the prompt twice.
func TestMergeSeedWithRingDedupsUserEcho(t *testing.T) {
	t.Parallel()
	seeded := []NativeEvent{
		{Type: EvUserText, Text: "old prompt", FromReplay: true},
		{Type: EvText, Text: "old answer", FromReplay: true},
		{Type: EvUserText, Text: "latest prompt", FromReplay: true}, // dup of ring echo
	}
	ring := []NativeEvent{
		{Seq: 1, Type: EvUserText, Text: "latest prompt"}, // live replay echo
		{Seq: 2, Type: EvText, Text: "live answer"},
	}
	out := mergeSeedWithRing(seeded, ring)

	// 2 kept seeds + 2 ring = 4.
	if len(out) != 4 {
		t.Fatalf("len(out) = %d, want 4 (%+v)", len(out), out)
	}
	// Seeds come first, in the seedSeqBase block, ascending and disjoint.
	if out[0].Text != "old prompt" || out[0].Seq != seedSeqBase {
		t.Fatalf("out[0] = %+v, want old prompt @ seedSeqBase", out[0])
	}
	if out[1].Text != "old answer" || out[1].Seq != seedSeqBase+1 {
		t.Fatalf("out[1] = %+v, want old answer @ seedSeqBase+1", out[1])
	}
	// The duplicate "latest prompt" seed was dropped; ring follows.
	if out[2].Seq != 1 || out[2].Text != "latest prompt" {
		t.Fatalf("out[2] = %+v, want ring latest prompt @ seq 1", out[2])
	}
	if out[3].Seq != 2 || out[3].Text != "live answer" {
		t.Fatalf("out[3] = %+v, want ring live answer @ seq 2", out[3])
	}
}

// TestHistoryBackfillSeedsBelowFloor proves the /native/history assembler serves
// the transcript seed in BOTH the fresh-mount (since==0) AND the
// ring-floor-passed (truncated) cases, and serves a pure-ring response when the
// requested `since` is contiguous with the ring. This is the fix for the prior
// stub where a below-floor request returned only the ring tail and the early
// conversation vanished once a long turn rolled the ring.
func TestHistoryBackfillSeedsBelowFloor(t *testing.T) {
	cfg := t.TempDir()
	cwd := "/home/user/wsl-projects/some-proj"
	slug := transcriptSlug(cwd)
	sessionID := "abc-123-session"
	projDir := filepath.Join(cfg, "projects", slug)
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, sessionID+".jsonl"), []byte(sampleTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)

	// A native pane whose ring floor is well above 1 (older events evicted), so a
	// low `since` is "below the floor".
	p := newTestNativePane()
	p.Cwd = cwd
	p.ClaudeSessionID = sessionID
	p.nativeRing = []NativeEvent{
		{Seq: 100, Type: EvText, Text: "live a"},
		{Seq: 101, Type: EvText, Text: "live b"},
	}

	countReplay := func(evs []NativeEvent) int {
		n := 0
		for _, e := range evs {
			if e.FromReplay {
				n++
			}
		}
		return n
	}

	// since==0: fresh seed → transcript prepended.
	evs, trunc := historyBackfill(p, 0)
	if !trunc {
		t.Fatalf("since=0 with ring floor 100: want truncated=true")
	}
	if got := countReplay(evs); got == 0 {
		t.Fatalf("since=0: want seeded (FromReplay) events prepended, got %d", got)
	}
	if evs[len(evs)-1].Seq != 101 {
		t.Fatalf("since=0: ring tail must follow the seed; last seq=%d want 101", evs[len(evs)-1].Seq)
	}

	// since below the floor (5 < 100): floor passed → seed prepended too.
	evs, trunc = historyBackfill(p, 5)
	if !trunc {
		t.Fatalf("since=5 below floor 100: want truncated=true")
	}
	if got := countReplay(evs); got == 0 {
		t.Fatalf("since=5 below floor: want transcript seed prepended, got %d replay events", got)
	}

	// since contiguous with the ring (100): not truncated, pure ring, no seed.
	evs, trunc = historyBackfill(p, 100)
	if trunc {
		t.Fatalf("since=100 (== a live seq): want truncated=false")
	}
	if got := countReplay(evs); got != 0 {
		t.Fatalf("since=100 contiguous: want NO seed, got %d replay events", got)
	}
	if len(evs) != 1 || evs[0].Seq != 101 {
		t.Fatalf("since=100: want only seq 101, got %+v", evs)
	}
}

// A pane id may be reused across foreground providers, leaving historical
// Claude identity on the pane while Codex owns it. That stale identity must
// never seed Claude model vitals (for example Opus) into Codex Focus history.
func TestHistoryBackfillDoesNotSeedClaudeTranscriptForCodex(t *testing.T) {
	cwd := "/home/user/wsl-projects/provider-isolation"
	sessionID := "old-claude-session"
	body := `{"type":"assistant","cwd":"/home/user/wsl-projects/provider-isolation","sessionId":"old-claude-session","message":{"id":"m1","model":"claude-opus-4-8","content":[{"type":"text","text":"old Claude reply"}],"usage":{"input_tokens":5000}}}
`
	writeSessionTranscript(t, t.TempDir(), cwd, sessionID, body)

	p := newTestNativePane()
	p.Agent = agentCodex
	p.Cwd = cwd
	p.ClaudeSessionID = sessionID // stale historical identity: must be ignored
	p.CodexThreadID = "codex-thread-current"
	p.nativeRing = []NativeEvent{{Seq: 7, Type: EvText, Text: "live Codex reply"}}

	events, truncated := historyBackfill(p, 0)
	if !truncated {
		t.Fatal("ring floor 7 should report truncated history")
	}
	if len(events) != 1 || events[0].Model != "" || events[0].Text != "live Codex reply" {
		t.Fatalf("Codex history received stale Claude seed: %+v", events)
	}
}

// TestSeedFromTranscriptCacheHit proves a repeat seed of an UNCHANGED transcript
// is served from the cache (identical events, no re-read needed) and that the
// returned slice is a fresh copy callers may stamp without corrupting the cache.
func TestSeedFromTranscriptCacheHit(t *testing.T) {
	resetSeedCache(t)
	cwd := "/home/user/wsl-projects/cache-proj"
	sessionID := "cache-session"
	path := writeSessionTranscript(t, t.TempDir(), cwd, sessionID, sampleTranscript)

	first, err := seedFromTranscript(cwd, sessionID)
	if err != nil {
		t.Fatalf("first seed err = %v", err)
	}
	if len(first) == 0 {
		t.Fatalf("first seed returned no events")
	}

	// The path must now be cached under a matching mtime+size stamp.
	seedCacheMu.Lock()
	e, ok := seedCache[path]
	seedCacheMu.Unlock()
	if !ok {
		t.Fatalf("expected a cache entry for %q after first seed", path)
	}

	second, err := seedFromTranscript(cwd, sessionID)
	if err != nil {
		t.Fatalf("second seed err = %v", err)
	}
	// Same events on a hit.
	assertEventsEqual(t, second, first)

	// The hit must return a FRESH copy, not the cached backing array — stamping
	// the returned slice must not mutate the cache (mirrors mergeSeedWithRing).
	if len(second) > 0 {
		second[0].Seq = 999999
	}
	seedCacheMu.Lock()
	cachedSeq := uint64(0)
	if len(e.seed.events) > 0 {
		cachedSeq = e.seed.events[0].Seq
	}
	seedCacheMu.Unlock()
	if cachedSeq != 0 {
		t.Fatalf("mutating the returned slice corrupted the cache: cached Seq=%d, want 0", cachedSeq)
	}
}

// TestSeedFromTranscriptCacheBustsOnGrowth proves transcript growth (a size/mtime
// change) busts the cache and the re-parse reflects the appended turn.
func TestSeedFromTranscriptCacheBustsOnGrowth(t *testing.T) {
	resetSeedCache(t)
	cwd := "/home/user/wsl-projects/grow-proj"
	sessionID := "grow-session"
	cfg := t.TempDir()
	path := writeSessionTranscript(t, cfg, cwd, sessionID, sampleTranscript)

	first, err := seedFromTranscript(cwd, sessionID)
	if err != nil {
		t.Fatalf("first seed err = %v", err)
	}

	// Append another real user turn. Open O_APPEND so the file grows.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	extra := `{"type":"user","promptSource":"typed","message":{"role":"user","content":"a brand new turn"}}` + "\n"
	if _, err := f.WriteString(extra); err != nil {
		t.Fatal(err)
	}
	f.Close()
	// Force a distinct mtime in case the append lands in the same coarse tick as
	// the original write (some filesystems have second-granularity mtimes).
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}

	second, err := seedFromTranscript(cwd, sessionID)
	if err != nil {
		t.Fatalf("second seed err = %v", err)
	}
	if len(second) != len(first)+1 {
		t.Fatalf("growth not reflected: first=%d second=%d, want second=first+1", len(first), len(second))
	}
	last := second[len(second)-1]
	if last.Type != EvUserText || last.Text != "a brand new turn" {
		t.Fatalf("last event after growth = %+v, want the appended user turn", last)
	}
}

// TestSeedFromTranscriptCacheConcurrent exercises the cache under concurrent
// callers across several sessions (run with -race to catch data races on the
// shared map). Every result must equal the single-threaded parse.
func TestSeedFromTranscriptCacheConcurrent(t *testing.T) {
	resetSeedCache(t)
	cfg := t.TempDir()
	const sessions = 8
	type fixture struct {
		cwd, sessionID string
		want           []NativeEvent
	}
	fixtures := make([]fixture, sessions)
	for i := 0; i < sessions; i++ {
		cwd := filepath.Join("/home/user/wsl-projects", "conc-proj-"+strconv.Itoa(i))
		sessionID := "conc-session-" + strconv.Itoa(i)
		writeSessionTranscript(t, cfg, cwd, sessionID, sampleTranscript)
		parsed, err := parseTranscriptFile(resolveTranscriptPath(cwd, sessionID))
		if err != nil {
			t.Fatal(err)
		}
		fixtures[i] = fixture{cwd: cwd, sessionID: sessionID, want: parsed.clonedEvents()}
	}

	var wg sync.WaitGroup
	for g := 0; g < 32; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			fx := fixtures[g%sessions]
			got, err := seedFromTranscript(fx.cwd, fx.sessionID)
			if err != nil {
				t.Errorf("concurrent seed err = %v", err)
				return
			}
			assertEventsEqualf(t, got, fx.want, "concurrent goroutine "+strconv.Itoa(g))
		}(g)
	}
	wg.Wait()
}

// writeSessionTranscript writes lines to <cfg>/projects/<slug>/<session>.jsonl
// and points CLAUDE_CONFIG_DIR at cfg. Returns the transcript path.
func writeSessionTranscript(t *testing.T, cfg, cwd, sessionID, body string) string {
	t.Helper()
	projDir := filepath.Join(cfg, "projects", transcriptSlug(cwd))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projDir, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	return path
}

// TestRepairDanglingToolUse proves the native-resume self-heal: a transcript
// ending in an UNANSWERED tool_use gets a synthetic tool_result appended (so the
// resumed session isn't poisoned), the repair is idempotent (a second call is a
// no-op once answered), and a complete transcript is never touched.
func TestRepairDanglingToolUse(t *testing.T) {
	cwd := "/home/user/wsl-projects/dangle-proj"
	sessionID := "dangle-session"

	// Transcript ending with an unanswered AskUserQuestion tool_use.
	dangling := `{"type":"user","promptSource":"typed","uuid":"u1","message":{"role":"user","content":"ask me to choose"}}
{"type":"assistant","uuid":"a1","sessionId":"dangle-session","cwd":"/home/user/wsl-projects/dangle-proj","version":"2.1.185","gitBranch":"main","message":{"id":"msg1","content":[{"type":"tool_use","id":"toolu_DANGLE","name":"AskUserQuestion","input":{"questions":[]}}]}}
`
	path := writeSessionTranscript(t, t.TempDir(), cwd, sessionID, dangling)

	st, err := prepareClaudeResume(cwd, sessionID)
	if err != nil {
		t.Fatalf("repair err = %v", err)
	}
	if !st.repairedToolUse {
		t.Fatalf("want repairedToolUse=true for a dangling tool_use")
	}

	// The appended line must be a user tool_result matching the dangling id, and
	// the transcript must now PARSE as a matched tool_use→tool_result pair.
	got, err := seedFromTranscript(cwd, sessionID)
	if err != nil {
		t.Fatalf("seed after repair err = %v", err)
	}
	var sawCall, sawResult bool
	for _, e := range got {
		if e.Type == EvToolCall && e.CallID == "toolu_DANGLE" {
			sawCall = true
		}
		if e.Type == EvToolResult && e.CallID == "toolu_DANGLE" && e.IsError {
			sawResult = true
		}
	}
	if !sawCall || !sawResult {
		t.Fatalf("after repair want matched call+result for toolu_DANGLE; call=%v result=%v", sawCall, sawResult)
	}

	// Idempotent: now that it's answered, a second repair is a no-op. It still
	// reports awaitsReply — the synthetic tool_result leaves the assistant owing
	// a reply, which is the honest state until the user takes the turn again.
	if st, err := prepareClaudeResume(cwd, sessionID); err != nil || st.repairedToolUse {
		t.Fatalf("second repair: want no repair, got (%+v,%v)", st, err)
	}
	_ = path

	// A complete transcript (tool_use already answered) is never touched.
	cwd2, sess2 := "/home/user/wsl-projects/complete-proj", "complete-session"
	complete := `{"type":"assistant","uuid":"a1","message":{"id":"m1","content":[{"type":"tool_use","id":"toolu_OK","name":"Bash","input":{"command":"ls"}}]}}
{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_OK","type":"tool_result","content":"file.txt","is_error":false}]}}
`
	writeSessionTranscript(t, t.TempDir(), cwd2, sess2, complete)
	if st, err := prepareClaudeResume(cwd2, sess2); err != nil || st.repairedToolUse {
		t.Fatalf("complete transcript: want no repair, got (%+v,%v)", st, err)
	}

	// A missing transcript is a clean no-op.
	if st, err := prepareClaudeResume("/no/such", "nope"); err != nil || st.repairedToolUse || st.awaitsReply {
		t.Fatalf("missing transcript: want zero state, got (%+v,%v)", st, err)
	}
}

// resetSeedCache clears the package-level transcript parse cache + counter so a
// test sees a deterministic, isolated cache. Other tests in this file seed and
// re-seed transcripts, so without a reset the parse counter would carry their
// reads.
func resetSeedCache(t *testing.T) {
	t.Helper()
	seedCacheMu.Lock()
	seedCache = map[string]seedCacheEntry{}
	seedCacheTick = 0
	seedParseCount = 0
	seedCacheMu.Unlock()
}

func currentSeedParseCount() uint64 {
	seedCacheMu.Lock()
	defer seedCacheMu.Unlock()
	return seedParseCount
}

// TestSeedCacheHitOnUnchangedFile proves the transcript parse is memoized per
// file: parsing the SAME unchanged transcript twice reads/parses the file once,
// and a subsequent mtime/size change re-parses (invalidation).
func TestSeedCacheHitOnUnchangedFile(t *testing.T) {
	resetSeedCache(t)
	cfg := t.TempDir()
	cwd := "/home/user/wsl-projects/cache-proj"
	sessionID := "cache-session"
	path := writeSessionTranscript(t, cfg, cwd, sessionID, sampleTranscript)

	// First parse: cache miss -> exactly one disk read.
	got1, err := seedFromTranscript(cwd, sessionID)
	if err != nil {
		t.Fatalf("first seed err = %v", err)
	}
	if n := currentSeedParseCount(); n != 1 {
		t.Fatalf("after first seed: parse count = %d, want 1", n)
	}
	if len(got1) == 0 {
		t.Fatalf("first seed: want events, got 0")
	}

	// Second parse of the UNCHANGED file: cache hit -> NO additional disk read,
	// identical result, and a cache entry exists for the path.
	got2, err := seedFromTranscript(cwd, sessionID)
	if err != nil {
		t.Fatalf("second seed err = %v", err)
	}
	if n := currentSeedParseCount(); n != 1 {
		t.Fatalf("after cache-hit seed: parse count = %d, want 1 (re-read the file)", n)
	}
	assertEventsEqual(t, got2, got1)

	seedCacheMu.Lock()
	_, cached := seedCache[path]
	seedCacheMu.Unlock()
	if !cached {
		t.Fatalf("want a cache entry for %s after parsing", path)
	}

	// Mutating the file (append a line -> new size + mtime) must invalidate the
	// entry and force a re-parse on the next read.
	// Bump mtime explicitly too in case the append lands within the same coarse
	// filesystem timestamp tick.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"user","promptSource":"typed","message":{"role":"user","content":"another"}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}

	got3, err := seedFromTranscript(cwd, sessionID)
	if err != nil {
		t.Fatalf("third seed err = %v", err)
	}
	if n := currentSeedParseCount(); n != 2 {
		t.Fatalf("after file change: parse count = %d, want 2 (re-parse on invalidation)", n)
	}
	if len(got3) != len(got1)+1 {
		t.Fatalf("after append: want %d events, got %d", len(got1)+1, len(got3))
	}
}

// TestSeedDedupByUuidKeepsDuplicateText proves a transcript with two
// identical-text user prompts (DIFFERENT uuids) yields TWO seeded user_text
// events, and that the consumable replay-echo dedup against the live ring drops
// only ONE matching seeded copy — never collapsing the legitimate repeat.
func TestSeedDedupByUuidKeepsDuplicateText(t *testing.T) {
	resetSeedCache(t)
	cfg := t.TempDir()
	cwd := "/home/user/wsl-projects/dup-proj"
	sessionID := "dup-session"

	// Two human turns with identical text "continue" but different uuids, plus an
	// assistant turn in between so they're genuinely separate prompts.
	body := `{"type":"user","promptSource":"typed","uuid":"u-aaa","message":{"role":"user","content":"continue"}}
{"type":"assistant","uuid":"a-1","message":{"id":"msg_1","content":[{"type":"text","text":"ok"}]}}
{"type":"user","promptSource":"typed","uuid":"u-bbb","message":{"role":"user","content":"continue"}}
`
	writeSessionTranscript(t, cfg, cwd, sessionID, body)

	// Parse intermediate carries both uuids, parallel to the user_text events.
	seed, err := seedParsedFromTranscript(cwd, sessionID)
	if err != nil {
		t.Fatalf("seedParsed err = %v", err)
	}
	var userTexts []string
	var userUUIDs []string
	for i, ev := range seed.events {
		if ev.Type == EvUserText {
			userTexts = append(userTexts, ev.Text)
			userUUIDs = append(userUUIDs, seed.userUUID[i])
		}
	}
	if len(userTexts) != 2 || userTexts[0] != "continue" || userTexts[1] != "continue" {
		t.Fatalf("want two 'continue' user_text events, got %v", userTexts)
	}
	if userUUIDs[0] != "u-aaa" || userUUIDs[1] != "u-bbb" {
		t.Fatalf("want uuids [u-aaa u-bbb] carried internally, got %v", userUUIDs)
	}

	// The plain seedFromTranscript (wire events) also yields both.
	evs, err := seedFromTranscript(cwd, sessionID)
	if err != nil {
		t.Fatalf("seed err = %v", err)
	}
	n := 0
	for _, e := range evs {
		if e.Type == EvUserText && e.Text == "continue" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("seedFromTranscript: want 2 'continue' user_text events, got %d", n)
	}

	// Replay-echo dedup: a live ring carrying ONE "continue" echo must drop only
	// ONE seeded copy, leaving the other intact (the old set-membership dedup
	// dropped BOTH).
	ring := []NativeEvent{
		{Seq: 1, Type: EvUserText, Text: "continue"}, // the live replay-echo of the last turn
		{Seq: 2, Type: EvText, Text: "live answer"},
	}
	merged := mergeParsedSeedWithRing(seed, ring)
	seededContinues := 0
	for _, e := range merged {
		if e.FromReplay && e.Type == EvUserText && e.Text == "continue" {
			seededContinues++
		}
	}
	if seededContinues != 1 {
		t.Fatalf("after consumable dedup: want 1 surviving seeded 'continue', got %d (merged=%+v)", seededContinues, merged)
	}
	// And the public mergeSeedWithRing (uuid-less path) must behave the same.
	merged2 := mergeSeedWithRing(seed.events, ring)
	seededContinues = 0
	for _, e := range merged2 {
		if e.FromReplay && e.Type == EvUserText && e.Text == "continue" {
			seededContinues++
		}
	}
	if seededContinues != 1 {
		t.Fatalf("mergeSeedWithRing consumable dedup: want 1 surviving 'continue', got %d", seededContinues)
	}
}

// TestMergeSeedRingOverlapServedOnce is the regression for the Ctrl+Shift+R
// "my prompt rendered below its own answer" bug. A long-alive native pane keeps
// the WHOLE conversation in its ring; a hard refresh re-fetches since=0 so the
// transcript seed and the live ring overlap on the SAME settled turn. The old
// merge only deduped user_text, so the turn's tool call + answer DOUBLED and the
// surviving prompt was the ring's live (non-replay) copy — which the client hoists
// AFTER the seeded answer, dropping the prompt below its reply. The overlap cut
// must serve the overlapping turn exactly once, from the ring, with the prompt
// ahead of its answer.
func TestMergeSeedRingOverlapServedOnce(t *testing.T) {
	t.Parallel()
	// Seed = the full on-disk transcript of one settled turn (FromReplay), with the
	// real Anthropic message.id / tool id the live ring also carries.
	seeded := []NativeEvent{
		{Type: EvSessionInit, Model: "claude-x", FromReplay: true},
		{Type: EvUserText, Text: "is there any use of a dead pane?", FromReplay: true},
		{Type: EvThinking, Text: "Let me think.", MessageID: "msg_A", FromReplay: true},
		{Type: EvToolCall, CallID: "toolu_A", Name: "Bash", MessageID: "msg_A", FromReplay: true},
		{Type: EvToolResult, CallID: "toolu_A", Content: "ok", FromReplay: true},
		{Type: EvText, Text: "A dead pane is a finished session…", MessageID: "msg_A", FromReplay: true},
	}
	// Ring = the same turn, still live in memory (input-time user_text first, then
	// the finalized assistant blocks), small monotonic seqs.
	ring := []NativeEvent{
		{Seq: 1, Type: EvUserText, Text: "is there any use of a dead pane?"},
		{Seq: 2, Type: EvThinking, Text: "Let me think.", MessageID: "msg_A"},
		{Seq: 3, Type: EvToolCall, CallID: "toolu_A", Name: "Bash", MessageID: "msg_A"},
		{Seq: 4, Type: EvToolResult, CallID: "toolu_A", Content: "ok"},
		{Seq: 5, Type: EvText, Text: "A dead pane is a finished session…", MessageID: "msg_A"},
		{Seq: 6, Type: EvTurnEnd},
	}
	out := mergeSeedWithRing(seeded, ring)

	// No duplication: each tool call / answer / prompt appears exactly once.
	count := func(pred func(NativeEvent) bool) int {
		n := 0
		for _, e := range out {
			if pred(e) {
				n++
			}
		}
		return n
	}
	if got := count(func(e NativeEvent) bool { return e.Type == EvToolCall && e.CallID == "toolu_A" }); got != 1 {
		t.Fatalf("tool call served %d times, want 1 (duplication)", got)
	}
	if got := count(func(e NativeEvent) bool { return e.Type == EvText && e.MessageID == "msg_A" }); got != 1 {
		t.Fatalf("answer served %d times, want 1 (duplication)", got)
	}
	if got := count(func(e NativeEvent) bool { return e.Type == EvUserText }); got != 1 {
		t.Fatalf("prompt served %d times, want 1", got)
	}

	// The single surviving prompt is the ring's LIVE copy (so it renders in the live
	// section WITH its answer, both non-replay), and it precedes its answer.
	var promptIdx, answerIdx = -1, -1
	for i, e := range out {
		if e.Type == EvUserText {
			promptIdx = i
			if e.FromReplay {
				t.Fatalf("surviving prompt is the seeded copy (FromReplay) — it would orphan below the answer")
			}
		}
		if e.Type == EvText && e.MessageID == "msg_A" {
			answerIdx = i
		}
	}
	if promptIdx < 0 || answerIdx < 0 || promptIdx > answerIdx {
		t.Fatalf("prompt must precede its answer: promptIdx=%d answerIdx=%d", promptIdx, answerIdx)
	}
	// The seed still contributes its (FromReplay) session_init for the vitals bar.
	if count(func(e NativeEvent) bool { return e.Type == EvSessionInit && e.FromReplay }) != 1 {
		t.Fatalf("seeded session_init must survive the cut for the vitals bar")
	}
}

// TestMergeSeedRingNoOverlapKeepsFullSeed proves the cut is inert when there is no
// overlap: an EMPTY ring (post-resume — the seed is the only history) and a ring
// that is entirely NEWER than the transcript (a just-typed turn not yet flushed)
// both keep the whole seed.
func TestMergeSeedRingNoOverlapKeepsFullSeed(t *testing.T) {
	t.Parallel()
	seeded := []NativeEvent{
		{Type: EvUserText, Text: "old prompt", FromReplay: true},
		{Type: EvText, Text: "old answer", MessageID: "msg_old", FromReplay: true},
	}
	// Empty ring → no cut, full seed survives.
	if out := mergeSeedWithRing(seeded, nil); len(out) != 2 {
		t.Fatalf("empty ring: want full seed (2), got %d", len(out))
	}
	// Ring with a brand-new turn whose ids are absent from the seed → no overlap,
	// so the full seed is kept and the ring follows.
	ring := []NativeEvent{
		{Seq: 1, Type: EvUserText, Text: "new prompt"},
		{Seq: 2, Type: EvText, Text: "new answer", MessageID: "msg_new"},
	}
	out := mergeSeedWithRing(seeded, ring)
	if len(out) != 4 {
		t.Fatalf("disjoint ring: want full seed + ring (4), got %d (%+v)", len(out), out)
	}
	if !out[0].FromReplay || out[0].Text != "old prompt" {
		t.Fatalf("seed must come first: out[0]=%+v", out[0])
	}
}
