package providers

var grok = Provider{
	Slug:        "grok",
	Vendor:      "xai",
	DisplayName: "Grok",
	DocsURL:     "https://docs.x.ai/",
	Runtimes: map[string]RuntimeFacts{
		"headless": {
			Billing: "api",
			Binary:  nil,
		},
	},
	Errors: ErrorSignatures{
		Quota: []string{"http 429", "rate limit"},
		Auth: []string{
			"http 401", "http 403", "incorrect api key", "invalid api key", "invalid authentication",
		},
		Infra: []string{
			"does not support permission preset", "http 500", "http 502", "http 503", "http 504",
			"connection refused", "connection reset", ": eof", "unexpected eof", "no such host",
		},
	},
	Models: ModelCatalog{Selectable: []string{"grok-code-fast-1", "grok-4-fast", "grok-4", "grok-3", "grok-3-mini"}, Rows: []ModelRow{
		{Match: "grok-code", Tier: TierFast},
		{Match: "mini", Tier: TierFast},
		{Match: "grok-4-fast", Tier: TierStandard},
		{Match: "grok-4", Tier: TierFrontier},
		{Match: "grok-3", Tier: TierStandard},
	}},
}
