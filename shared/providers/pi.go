package providers

// Pi is the official, model-agnostic Pi coding harness
// (github.com/earendil-works/pi, MIT). SoluTerminal manages its install and
// launch. The managed harness account store carries exactly one key per
// provider, so this first cut binds a Pi account to a DeepSeek API key and
// launches Pi on its first-party "deepseek" provider. Pi's own catalog is
// multi-vendor; adding another vendor later means another harness row, not a
// change to this one.
var pi = Provider{
	Slug:        "pi",
	Vendor:      "earendil-works",
	DisplayName: "Pi",
	DocsURL:     "https://pi.dev/docs/latest",
	Creds:       apiHarnessCreds("pi"),
	Harness: &HarnessFacts{
		Billings:          []string{"api"},
		Endpoints:         map[string]string{"api": "https://api.deepseek.com"},
		DefaultModel:      DeepSeekFlashModel,
		InstallDir:        "pi",
		RuntimePath:       []string{"node_modules", "@earendil-works", "pi-coding-agent", "dist", "bundle", "cli.js"},
		ImportKeyProvider: "deepseek-headless",
		ImportKeyEnv:      "DEEPSEEK_API_KEY",
	},
	Errors: ErrorSignatures{
		Quota:   []string{"http 429", "rate limit"},
		Auth:    []string{"http 401", "http 403", "invalid api key", "authentication fails"},
		Billing: []string{"insufficient balance", "http 402"},
		Infra:   []string{"http 500", "http 502", "http 503", "http 504", "connection refused", "connection reset", ": eof", "unexpected eof", "no such host"},
	},
	Models: ModelCatalog{
		Selectable: []string{DeepSeekFlashModel, DeepSeekVisionModel}, Rows: []ModelRow{
			{Match: DeepSeekFlashModel, Exact: true, Tier: TierStandard},
			{Match: DeepSeekVisionModel, Exact: true, Tier: TierStandard},
		},
	},
}
