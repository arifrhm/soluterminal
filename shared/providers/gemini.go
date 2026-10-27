package providers

var gemini = Provider{
	Slug:        "gemini",
	Vendor:      "google",
	DisplayName: "Gemini",
	DocsURL:     "https://ai.google.dev/gemini-api/docs",
	Runtimes: map[string]RuntimeFacts{
		"headless": {
			Billing: "api",
			Binary: &BinarySpec{
				Name:               "gemini",
				HomeSubdirs:        []string{".local/bin", ".asdf/shims", ".volta/bin"},
				HomeGlobs:          []string{".nvm/versions/node/*/bin", ".asdf/installs/nodejs/*/bin"},
				LoginShellFallback: true,
				NotFoundMsg:        "gemini not found (PATH, ~/.local/bin, nvm, asdf, volta, login shell)",
			},
		},
	},
	Errors: ErrorSignatures{
		Quota:   []string{"resource_exhausted", "quota exceeded for quota metric", "you exceeded your current quota"},
		Auth:    []string{"api key not valid", "api key expired", "please pass a valid api key", "unauthenticated", "reauthentication required"},
		Billing: []string{"billing account", "billing is not enabled"},
	},
	Models: ModelCatalog{
		Selectable: []string{"gemini-3.8-flash"},
		Rows: []ModelRow{
			{Match: "flash", Tier: TierFast},
			{Match: "pro", Tier: TierPremium},
			{Match: "gemini", Tier: TierStandard},
		},
		EffortByModel: map[string][]string{
			"gemini-3.8-flash": {"low", "medium", "high"},
		},
	},
}
