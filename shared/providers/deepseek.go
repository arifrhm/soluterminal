package providers

import (
	"fmt"
	"strings"
)

const (
	// DeepSeekFlashModel is the only current non-vision DeepSeek model that
	// SoluTerminal may select or dispatch.
	DeepSeekFlashModel = "deepseek-flash"
	// DeepSeekVisionModel is the current experimental vision route.
	DeepSeekVisionModel = "deepseek-v4-flash-vision-exp"
)

var deepSeekLegacyNonVisionModels = map[string]bool{
	"deepseek-v4-pro":   true,
	"deepseek-v4-flash": true,
	"deepseek-chat":     true,
	"deepseek-reasoner": true,
}

// DeepSeekModelComponent returns the normalized final path component used by
// provider-prefixed model identifiers. Empty path components are deliberately
// rejected; accepting them would turn malformed input into a vendor request.
func DeepSeekModelComponent(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	parts := strings.Split(trimmed, "/")
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return "", fmt.Errorf("unsupported DeepSeek model %q", raw)
		}
	}
	return strings.ToLower(parts[len(parts)-1]), nil
}

// HasRetiredDeepSeekProReference detects Pro even when it is attached to a
// non-DeepSeek runtime. It is intentionally broader than the recovery parser:
// malformed Pro-containing identifiers must fail closed, never pass through.
func HasRetiredDeepSeekProReference(raw string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(raw)), "deepseek-v4-pro")
}

// HasRetiredDeepSeekReference recognizes every known retired non-vision
// selector, including provider-prefixed forms. Pro remains a substring check
// so malformed Pro variants fail closed; the other legacy identifiers must
// match a complete final path component to avoid rejecting unrelated vendors.
func HasRetiredDeepSeekReference(raw string) bool {
	if HasRetiredDeepSeekProReference(raw) {
		return true
	}
	component, err := DeepSeekModelComponent(raw)
	return err == nil && deepSeekLegacyNonVisionModels[component]
}

// CanonicalDeepSeekDispatchModel is the persisted-data recovery policy used at
// the final dispatch boundary. Blank and known legacy non-vision identifiers
// safely recover to Flash; the current vision identifier is preserved. Unknown
// or malformed input is rejected before provider spend.
func CanonicalDeepSeekDispatchModel(raw string) (string, error) {
	component, err := DeepSeekModelComponent(raw)
	if err != nil {
		return "", err
	}
	switch component {
	case "", DeepSeekFlashModel:
		return DeepSeekFlashModel, nil
	case DeepSeekVisionModel:
		return DeepSeekVisionModel, nil
	default:
		if deepSeekLegacyNonVisionModels[component] {
			return DeepSeekFlashModel, nil
		}
		return "", fmt.Errorf("unsupported DeepSeek model %q", raw)
	}
}

// ValidateDeepSeekSelector validates new configuration. Recovery aliases are
// intentionally not accepted here: only canonical current identifiers may be
// written into active configuration.
func ValidateDeepSeekSelector(raw string) error {
	if raw == DeepSeekFlashModel || raw == DeepSeekVisionModel {
		return nil
	}
	return fmt.Errorf("unsupported DeepSeek model %q; use %s or %s", raw, DeepSeekFlashModel, DeepSeekVisionModel)
}

var deepseek = Provider{
	Slug:        "deepseek",
	Vendor:      "deepseek",
	DisplayName: "DeepSeek",
	DocsURL:     "https://api-docs.deepseek.com/",
	Creds:       apiHarnessCreds("deepseek"),
	Harness: &HarnessFacts{
		Billings:          []string{"api"},
		Endpoints:         map[string]string{"api": "https://api.deepseek.com"},
		DefaultModel:      DeepSeekFlashModel,
		InstallDir:        "deepseek",
		RuntimePath:       []string{"node_modules", "@deepseek-ai", "dsh", "lib", "bin.js"},
		ImportKeyProvider: "deepseek-headless",
		ImportKeyEnv:      "DEEPSEEK_API_KEY",
	},
	Runtimes: map[string]RuntimeFacts{
		"headless": {
			Billing: "api",
			Binary:  nil,
		},
		// The DSH CLI runtime (deepseek-dsh). It locates its own node + dsh
		// entrypoint, so it declares no generic binary spec, and it is a
		// required-secret consumer of DEEPSEEK_API_KEY like the HTTP runtime.
		"dsh": {
			Billing: "api",
			Binary:  nil,
		},
	},
	Errors: ErrorSignatures{
		Quota: []string{"http 429", "rate limit"},
		Auth: []string{
			"http 401", "http 403", "invalid api key", "incorrect api key", "authentication fails",
		},
		Billing: []string{"insufficient balance", "http 402"},
		Infra: []string{
			"does not support permission preset", "http 500", "http 502", "http 503", "http 504",
			"connection refused", "connection reset", ": eof", "unexpected eof", "no such host",
		},
	},
	Models: ModelCatalog{
		// Historical pricing lives in shared/deepseekusage. Dispatch catalogs
		// contain current choices only so tier lookup cannot resurrect a retired
		// model as an escalation or lineup candidate.
		Selectable: []string{DeepSeekFlashModel, DeepSeekVisionModel}, Rows: []ModelRow{
			{Match: DeepSeekFlashModel, Exact: true, Tier: TierStandard},
			{Match: DeepSeekVisionModel, Exact: true, Tier: TierStandard},
		},
	},
}
