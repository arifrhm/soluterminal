// Package wire contains stable transport declarations shared by the daemon
// and cloud executables.
package wire

// CCUsageRecord is one billed assistant turn as it travels from the daemon to
// the cloud. JSON tags, field types, and field order are the wire contract;
// both executables compile against this single declaration, so do not rename a
// tag without changing both sides together.
//
// Identity fields (machine_id and user_id) are deliberately not on the wire:
// the cloud resolves them from the machine token and never trusts the body for
// identity.
//
// EventTS is the transcript line's own timestamp (RFC3339/ISO8601 UTC, for
// example "2026-06-17T11:01:05.277Z"), not the ingest time. TotalTokens is the
// daemon-computed sum of input, output, cache-read, and cache-create tokens. The
// cloud accepts that field for forward compatibility but recomputes the same
// component sum server-side for keep-max ordering.
type CCUsageRecord struct {
	EventTS     string `json:"event_ts"`
	SessionID   string `json:"session_id"`
	MessageID   string `json:"message_id"`
	RequestID   string `json:"request_id"`
	Model       string `json:"model"`
	InApp       bool   `json:"in_app"`
	IsSidechain bool   `json:"is_sidechain"`
	// Account is the Claude account name that produced this turn (captured at
	// SessionStart), or "" when unattributable. The cloud still stamps user_id
	// from the machine token for authorization; Account is the within-machine
	// disambiguator so multiple accounts on one host do not merge into one row.
	// "" means fall back to the machine owner.
	Account              string `json:"account"`
	WorkflowInvocationID string `json:"workflow_invocation_id"`
	WorkflowRole         string `json:"workflow_role"`
	CacheTTLClass        string `json:"cache_ttl_class"`
	CacheTTLControl      string `json:"cache_ttl_control"`
	LongGapObserved      bool   `json:"long_gap_observed"`

	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheReadTokens     int `json:"cache_read_tokens"`
	CacheCreateTokens   int `json:"cache_create_tokens"`
	CacheCreate5mTokens int `json:"cache_create_5m_tokens"`
	CacheCreate1hTokens int `json:"cache_create_1h_tokens"`
	TotalTokens         int `json:"total_tokens"`

	ServiceTier string `json:"service_tier"`
	Speed       string `json:"speed"`
	Cwd         string `json:"cwd"`
	GitBranch   string `json:"git_branch"`
	// GitRepo is the normalized HTTPS remote URL for Cwd, resolved from the
	// working tree's git config. It is empty when Cwd is not a git repository
	// or has no origin.
	GitRepo   string `json:"git_repo"`
	CCVersion string `json:"cc_version"`

	WebSearchRequests int `json:"web_search_requests"`
	WebFetchRequests  int `json:"web_fetch_requests"`

	StopReason string `json:"stop_reason"`

	// Provider names the agent CLI that produced the turn: "claude" (Claude
	// Code transcripts) or "codex" (OpenAI Codex rollouts). Empty means
	// "claude" so records from older daemons keep their meaning. Codex token
	// classes are normalized daemon-side to the Claude shape: InputTokens is
	// fresh (uncached) input, CacheReadTokens is cached input, and
	// CacheCreateTokens is cache-write input.
	Provider string `json:"provider,omitempty"`
	// ReasoningOutputTokens is the reasoning share of OutputTokens (a subset,
	// never added to the total). Codex reports it; Claude leaves it zero.
	ReasoningOutputTokens int `json:"reasoning_output_tokens,omitempty"`
}

// CCUsageBatch is the request envelope: {"records": [...]}. Identity remains
// token-derived and is deliberately not part of this envelope.
type CCUsageBatch struct {
	Records []CCUsageRecord `json:"records"`
}
