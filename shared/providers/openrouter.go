package providers

// Error needles were audited against OpenRouter's documented HTTP errors on
// 2026-08-25 (401 credentials, 402 credits/key limits, 403 moderation, 408
// timeout, 429 rate limits, and 502/503 provider outages). The model catalog
// was verified the same day against GET https://openrouter.ai/api/v1/models,
// which listed meta/muse-spark-1.2, meta/muse-spark-1.2-contributor,
// meta/muse-spark-1.1, and meta/muse-glimmer-30b with response_format,
// structured_outputs, and reasoning_effort support.
var openrouter = Provider{
	Slug:        "openrouter",
	Vendor:      "openrouter",
	DisplayName: "OpenRouter",
	DocsURL:     "https://openrouter.ai/docs/quickstart",
	Runtimes: map[string]RuntimeFacts{
		"headless": {
			Billing: "api",
			Binary:  nil,
		},
	},
	Errors: ErrorSignatures{
		Quota: []string{"http 429", "rate limit"},
		Auth: []string{
			"http 401", "invalid api key", "no auth credentials found", "user not found",
		},
		Billing: []string{"http 402", "insufficient credits", "requires more credits", "key limit exceeded"},
		Infra: []string{
			"does not support permission preset", "http 408", "http 500", "http 502", "http 503", "http 504",
			"connection refused", "connection reset", ": eof", "unexpected eof", "no such host",
		},
	},
	// OpenRouter uses HTTP 403 for moderation-flagged input, so it is
	// deliberately not an auth needle and remains a logic failure.
	Models: ModelCatalog{Selectable: []string{
		"meta/muse-spark-1.2", "meta/muse-spark-1.2-contributor", "meta/muse-spark-1.1",
		"meta/muse-glimmer-30b", "anthropic/claude-opus-5", "google/gemini-3.7-flash",
	}, Rows: []ModelRow{
		{Match: "muse-spark", Tier: TierStandard},
		{Match: "glimmer", Tier: TierFast},
		{Match: "haiku", Tier: TierFast},
		{Match: "flash", Tier: TierFast},
		{Match: "mini", Tier: TierFast},
		{Match: "nano", Tier: TierFast},
		{Match: "sonnet", Tier: TierStandard},
		{Match: "opus", Tier: TierPremium},
		{Match: "fable", Tier: TierFrontier},
	}},
	// The API key is local-only and is never team-distributed.
	Creds: nil,
}
