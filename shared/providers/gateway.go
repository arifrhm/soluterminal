package providers

// GatewayFacts is the API surface a local model gateway may expose. Subscription
// providers have no entry: their OAuth credentials must never be proxied.
type GatewayFacts struct {
	Protocol  string
	BaseURL   string
	RuntimeID string
	SecretEnv string
}

var gatewayFacts = map[string]GatewayFacts{
	"anthropic":    {"anthropic", "https://api.anthropic.com", "", "ANTHROPIC_API_KEY"},
	"openai":       {"openai", "https://api.openai.com/v1", "", "OPENAI_API_KEY"},
	"deepseek":     {"openai", "https://api.deepseek.com/v1", "deepseek-headless", "DEEPSEEK_API_KEY"},
	"glm":          {"openai", "https://api.z.ai/api/paas/v4", "", "ZAI_API_KEY"},
	"xai":          {"openai", "https://api.x.ai/v1", "grok-headless", "XAI_API_KEY"},
	"openrouter":   {"openai", "https://openrouter.ai/api/v1", "openrouter-headless", "OPENROUTER_API_KEY"},
	"gemini":       {"openai", "https://generativelanguage.googleapis.com/v1beta/openai", "gemini-headless", "GEMINI_API_KEY"},
	"local-openai": {"openai", "", "local-openai-headless", "LOCAL_OPENAI_API_KEY"},
	"pi":           {"openai", "https://api.deepseek.com/v1", "", "DEEPSEEK_API_KEY"},
	"pi-glm":       {"openai", "https://api.z.ai/api/coding/paas/v4", "", "ZAI_API_KEY"},
}

func Gateway(slug string) (GatewayFacts, bool) { facts, ok := gatewayFacts[slug]; return facts, ok }

func GatewaySlugs() []string {
	return []string{"anthropic", "openai", "deepseek", "glm", "xai", "openrouter", "gemini", "local-openai", "pi", "pi-glm"}
}
