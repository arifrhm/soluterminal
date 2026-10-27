package providers

// TypeSafe serves Jev, a System One model that answers typed questions about a
// single state with calibrated probabilities. It is a direct HTTP API with no
// CLI and no child process, so it exposes one "api" runtime family and declares
// no binary facts. The workflow runtime surfaces it as typesafe-api and the
// engine can bind it only to single-shot roles.
var typesafe = Provider{
	Slug:        "typesafe",
	Vendor:      "typesafe",
	DisplayName: "TypeSafe (Jev)",
	DocsURL:     "https://docs.typesafe.ai",
	Runtimes: map[string]RuntimeFacts{
		"api": {Billing: "api", Binary: nil},
	},
	Errors: ErrorSignatures{
		// Jev reports failures as structured HTTP statuses; the runtime records
		// them in APIErrorStatus, which is authoritative. These scoped prose
		// needles are a fallback for a wrapped or re-worded message.
		Quota: []string{"http 429", "rate limit", "http 529", "overloaded"},
		Auth:  []string{"http 401", "invalid api key", "missing api key", "unauthorized"},
		Infra: []string{"http 500", "http 502", "http 503", "http 504", "connection refused", "connection reset", ": eof", "unexpected eof", "no such host"},
	},
	Models: ModelCatalog{
		Selectable: []string{"jev-latest", "jev-preview", "jev-1.13.0"},
		Rows: []ModelRow{
			// Jev is a cheap, fast decision model (not a coding model), so all
			// of its ids sit at the fast tier; floors are never the reason to
			// exclude it.
			{Match: "jev-1.13.0", Exact: true, Tier: TierFast},
			{Match: "jev-latest", Exact: true, Tier: TierFast},
			{Match: "jev-preview", Exact: true, Tier: TierFast},
		},
	},
}
