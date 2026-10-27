package main

import (
	"bytes"
	"encoding/json"
)

// Pure extraction logic for Claude Code transcript lines.
//
// A Claude Code transcript is newline-delimited JSON ("JSONL"): one JSON
// object per line, appended live as the session runs. We care only about
// "assistant" lines that carry a token-usage block — those are the billed
// turns. This file is deliberately free of any I/O or daemon state so it
// can be unit-tested over raw byte fixtures; the scanner (ccusage_scan.go)
// owns the file reading, cursor bookkeeping and in_app tagging.
//
// Robustness notes:
//   - We split on '\n' and process only COMPLETE lines (those terminated by
//     a newline). The trailing partial line — Claude may be mid-append — is
//     NOT consumed; the caller carries it via its byte-offset cursor and we
//     re-see it next pass once the newline lands.
//   - A line that fails to parse (corruption, a partial JSON object that
//     somehow got a newline, a future schema we don't understand) is
//     skipped, never fatal.

// usageLine is the subset of a transcript line we decode. Fields we don't
// need are left out so json.Unmarshal stays cheap and we never touch
// message content.
type usageLine struct {
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp"`
	SessionID   string `json:"sessionId"`
	RequestID   string `json:"requestId"`
	CWD         string `json:"cwd"`
	GitBranch   string `json:"gitBranch"`
	Version     string `json:"version"`
	IsSidechain bool   `json:"isSidechain"`
	Message     *struct {
		ID         string       `json:"id"`
		Model      string       `json:"model"`
		StopReason string       `json:"stop_reason"`
		Usage      *usageTokens `json:"usage"`
	} `json:"message"`
}

// usageTokens mirrors message.usage. cache_creation can be either absent or
// the nested {ephemeral_5m,ephemeral_1h} object.
type usageTokens struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
	CacheCreation       *struct {
		Ephemeral5m int `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h int `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	ServiceTier   string `json:"service_tier"`
	Speed         string `json:"speed"`
	ServerToolUse *struct {
		WebSearchRequests int `json:"web_search_requests"`
		WebFetchRequests  int `json:"web_fetch_requests"`
	} `json:"server_tool_use"`
}

// usagePrefilter is the cheap byte-substring gate run before any JSON parse.
// Only lines mentioning a usage block can possibly be billed turns, so the
// vast majority of transcript lines (user messages, tool results, summaries)
// are rejected without an allocation.
var usagePrefilter = []byte(`"usage"`)

// extractUsageLines parses every COMPLETE line in buf and returns the kept
// usage records plus consumed — the number of bytes up to and INCLUDING the
// last '\n'. The trailing partial line (if any) is not consumed; the caller
// must carry it via its offset cursor.
//
// The returned records have InApp left at its zero value (false) and
// IsSidechain set from the line's own isSidechain field. The scanner
// finalizes both from the file path + ledger — it knows things this pure
// function can't.
func extractUsageLines(buf []byte) (recs []ccUsageRecord, consumed int) {
	for {
		nl := bytes.IndexByte(buf[consumed:], '\n')
		if nl < 0 {
			// No more complete lines — the rest is a partial we leave for
			// next pass.
			break
		}
		line := buf[consumed : consumed+nl]
		consumed += nl + 1 // include the newline

		// Cheap reject before paying for a JSON parse.
		if !bytes.Contains(line, usagePrefilter) {
			continue
		}
		rec, ok := parseUsageLine(line)
		if !ok {
			continue
		}
		recs = append(recs, rec)
	}
	return recs, consumed
}

// parseUsageLine unmarshals one complete line and, if it is a billed
// assistant turn, maps it to a ccUsageRecord. ok is false only for genuinely
// unusable lines we skip (parse error, wrong type, no usage, synthetic model).
// Turns missing message.id/requestId are KEPT — parseUsageLine synthesizes
// deterministic surrogate keys (fix H1) so they are counted, not dropped.
func parseUsageLine(line []byte) (ccUsageRecord, bool) {
	var u usageLine
	if err := json.Unmarshal(line, &u); err != nil {
		return ccUsageRecord{}, false
	}
	// Keep only assistant turns that actually carry a usage block.
	if u.Type != "assistant" || u.Message == nil || u.Message.Usage == nil {
		return ccUsageRecord{}, false
	}
	// "<synthetic>" model lines are 0-token placeholders Claude writes for
	// internal/interrupt turns — never a real billed request.
	if u.Message.Model == "<synthetic>" {
		return ccUsageRecord{}, false
	}
	if !validUsageLine(&u) {
		return ccUsageRecord{}, false
	}

	usage := u.Message.Usage
	var create5m, create1h int
	if usage.CacheCreation != nil {
		create5m = usage.CacheCreation.Ephemeral5m
		create1h = usage.CacheCreation.Ephemeral1h
	}
	var webSearch, webFetch int
	if usage.ServerToolUse != nil {
		webSearch = usage.ServerToolUse.WebSearchRequests
		webFetch = usage.ServerToolUse.WebFetchRequests
	}

	// Synthesize stable surrogate keys for turns that lack a message.id or
	// requestId (fix H1). Empty identity fields used to survive the daemon but
	// were then DROPPED by the cloud (its unique key rejects empty
	// message_id/request_id), silently losing those billed turns. Instead we
	// derive deterministic keys from the line's own content (sessionId +
	// timestamp), so the same turn yields the SAME surrogate on every re-scan —
	// the daemon's dedupKeepMax and the cloud's keep-max upsert therefore agree
	// across passes/restarts and never double-count.
	messageID := u.Message.ID
	if messageID == "" {
		messageID = "synmsg:" + u.SessionID + ":" + u.Timestamp
	}
	requestID := u.RequestID
	if requestID == "" {
		requestID = "synreq:" + messageID
	}

	rec := ccUsageRecord{
		EventTS:     u.Timestamp,
		SessionID:   u.SessionID,
		MessageID:   messageID,
		RequestID:   requestID,
		Model:       u.Message.Model,
		IsSidechain: u.IsSidechain, // scanner may override from the file path
		// InApp finalized by the scanner from the ledger.

		InputTokens:         usage.InputTokens,
		OutputTokens:        usage.OutputTokens,
		CacheReadTokens:     usage.CacheReadTokens,
		CacheCreateTokens:   usage.CacheCreationTokens,
		CacheCreate5mTokens: create5m,
		CacheCreate1hTokens: create1h,

		ServiceTier: usage.ServiceTier,
		Speed:       usage.Speed,
		Cwd:         u.CWD,
		GitBranch:   u.GitBranch,
		CCVersion:   u.Version,

		WebSearchRequests: webSearch,
		WebFetchRequests:  webFetch,

		StopReason: u.Message.StopReason,
	}
	// total = input + output + cache_read + cache_create. Used for the
	// cloud's keep-max upsert (and our in-batch dedup). The 5m/1h split is a
	// breakdown OF cache_creation_input_tokens, so it's not added again.
	rec.TotalTokens = rec.InputTokens + rec.OutputTokens + rec.CacheReadTokens + rec.CacheCreateTokens
	return rec, true
}

// validUsageLine is the defensive validity filter (plan A3). The genuinely
// unusable lines (non-assistant, no usage block, "<synthetic>" model) are
// already rejected by parseUsageLine before this is called, so there is
// currently nothing left to reject here — we keep the turn.
//
// Deliberately NOT rejected (fix L1): a `version` that doesn't look like a
// semver prefix. A future Claude Code release could carry a non-numeric
// version string, and using it as a hard reject would silently zero out a
// machine's entire usage. We still capture the raw version into CCVersion;
// it just no longer gates acceptance.
//
// Empty identity fields (message.id / requestId) are also no longer a reason
// to drop: fix H1 synthesizes deterministic surrogate keys for those in
// parseUsageLine, so the turn is counted with stable keys instead.
func validUsageLine(u *usageLine) bool {
	return true
}

// dedupKeepMax collapses records that share a (MessageID, RequestID) key,
// keeping the one with the largest TotalTokens. This is REQUIRED before
// POSTing a batch (Decision 3): the cloud's multi-row upsert errors if the
// same conflict key appears twice in one statement, and streaming writes the
// same turn 2–5× with growing output. Cross-batch / cross-restart dedup is
// the DB's job (keep-max upsert); this only handles within-pass duplicates.
//
// Order is preserved by first appearance so the output is deterministic
// (helps tests and keeps batches stable across passes).
func dedupKeepMax(recs []ccUsageRecord) []ccUsageRecord {
	if len(recs) <= 1 {
		return recs
	}
	type key struct{ msg, req string }
	idx := make(map[key]int, len(recs))
	out := make([]ccUsageRecord, 0, len(recs))
	for _, r := range recs {
		k := key{r.MessageID, r.RequestID}
		if pos, ok := idx[k]; ok {
			prior := out[pos]
			winner := prior
			if r.TotalTokens > prior.TotalTokens {
				winner = r
			}
			mergeCCUsageTelemetry(&winner, prior, r)
			out[pos] = winner
			continue
		}
		idx[k] = len(out)
		out = append(out, r)
	}
	return out
}

func mergeCCUsageTelemetry(dst *ccUsageRecord, first, second ccUsageRecord) {
	firstNonempty := func(a, b string) string {
		if a != "" {
			return a
		}
		return b
	}
	dst.WorkflowInvocationID = firstNonempty(first.WorkflowInvocationID, second.WorkflowInvocationID)
	dst.WorkflowRole = firstNonempty(first.WorkflowRole, second.WorkflowRole)
	dst.CacheTTLClass = firstNonempty(first.CacheTTLClass, second.CacheTTLClass)
	dst.CacheTTLControl = firstNonempty(first.CacheTTLControl, second.CacheTTLControl)
	dst.LongGapObserved = first.LongGapObserved || second.LongGapObserved
}
