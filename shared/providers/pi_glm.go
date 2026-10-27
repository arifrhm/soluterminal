package providers

// PiGLM is the official, model-agnostic Pi coding harness
// (github.com/earendil-works/pi, MIT) bound to Pi's first-party "zai"
// provider. It shares the Pi runtime binary with the "pi" row; the rows
// differ only in credential family: this one carries a Z.ai Coding Plan key
// (ZAI_API_KEY) and launches Pi with `--provider zai`, while "pi" carries a
// DeepSeek key. One row per vendor is deliberate: the managed account store
// carries exactly one key per provider.
//
// Pi's zai provider has a fixed base URL (the Coding Plan endpoint) and no
// base-URL override, so this row declares only the coding-plan billing mode.
var piGlm = Provider{
	Slug:        "pi-glm",
	Vendor:      "earendil-works",
	DisplayName: "Pi GLM",
	DocsURL:     "https://pi.dev/docs/latest",
	Creds:       apiHarnessCreds("pi-glm"),
	Harness: &HarnessFacts{
		Billings:      []string{"coding-plan"},
		Endpoints:     map[string]string{"coding-plan": "https://api.z.ai/api/coding/paas/v4"},
		DefaultModel:  "glm-5.3-flash",
		InstallDir:    "pi",
		RuntimePath:   []string{"node_modules", "@earendil-works", "pi-coding-agent", "dist", "bundle", "cli.js"},
		ImportKeyFile: "zai-env-file",
	},
	Errors: ErrorSignatures{
		Quota:   []string{"http 429", "rate limit"},
		Auth:    []string{"http 401", "http 403", "invalid api key", "authentication fails"},
		Billing: []string{"insufficient balance", "http 402"},
		Infra:   []string{"http 500", "http 502", "http 503", "http 504", "connection refused", "connection reset", ": eof", "unexpected eof", "no such host"},
	},
	// Flash first: the "glm-5.3" matcher is a substring, so it would
	// otherwise swallow the flash id into the premium tier.
	Models: ModelCatalog{Selectable: []string{"glm-5.3-flash", "glm-5.3"}, Rows: []ModelRow{
		{Match: "glm-5.3-flash", Tier: TierStandard},
		{Match: "glm-5.3", Tier: TierPremium},
	}},
}
