package providers

var localOpenAI = Provider{
	Slug:        "local-openai",
	Vendor:      "local-openai",
	DisplayName: "Local OpenAI-compatible",
	DocsURL:     "https://platform.openai.com/docs/api-reference/models/list",
	Runtimes: map[string]RuntimeFacts{
		"headless": {Billing: "api", Binary: nil},
	},
	Errors: ErrorSignatures{
		Quota: []string{"http 429", "rate limit"},
		Auth: []string{
			"http 401", "http 403", "invalid api key", "unauthorized", "forbidden",
		},
		Billing: []string{"http 402", "insufficient credits"},
		Infra: []string{
			"endpoint is not configured", "is not configured", "health check failed", "model list",
			"is not served", "http 500", "http 502", "http 503", "http 504",
			"connection refused", "connection reset", ": eof", "unexpected eof", "no such host", "cannot run role", "does not support permission preset",
		},
	},
	// Local model identifiers are user-defined; a fixed tier catalog would be
	// misleading and would reject otherwise valid servers.
	Models: ModelCatalog{},
}
