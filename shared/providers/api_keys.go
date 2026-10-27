package providers

var anthropic = Provider{
	Slug: "anthropic", Vendor: "anthropic-api", DisplayName: "Anthropic API",
	DocsURL: "https://docs.anthropic.com/en/api/overview",
	KeyAPI:  &KeyAPIFacts{Env: "ANTHROPIC_API_KEY", Protocol: "anthropic", BaseURL: "https://api.anthropic.com"},
}

var openai = Provider{
	Slug: "openai", Vendor: "openai-api", DisplayName: "OpenAI API",
	DocsURL: "https://platform.openai.com/docs/api-reference",
	KeyAPI:  &KeyAPIFacts{Env: "OPENAI_API_KEY", Protocol: "openai", BaseURL: "https://api.openai.com/v1"},
}
