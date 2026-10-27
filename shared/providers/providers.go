// Package providers owns declarative provider facts shared across soluterminal.
// It must remain stdlib-only. Records are facts, never policy; ranks, lineup
// choices, and other product decisions belong to their consumers.
package providers

import "strings"

const (
	TierFast     = "fast"
	TierStandard = "standard"
	TierPremium  = "premium"
	TierFrontier = "frontier"
)

// Provider describes one provider and the runtime families it exposes.
// AuthArtifact and BundleDescriptor are deliberately deferred to the S4
// credential-bundle slice.
type Provider struct {
	Slug        string                  `json:"slug"`
	Vendor      string                  `json:"vendor"`
	DisplayName string                  `json:"displayName"`
	DocsURL     string                  `json:"docsUrl"`
	Runtimes    map[string]RuntimeFacts `json:"-"`
	Errors      ErrorSignatures         `json:"-"`
	Models      ModelCatalog            `json:"-"`
	Creds       *CredFacts              `json:"-"`
	// Harness is non-nil only for providers whose native coding-agent CLI
	// SoluTerminal installs, configures, and launches in a terminal pane. The
	// facts here replace per-provider branches in the daemon; install and launch
	// policy still stays in the daemon.
	Harness *HarnessFacts `json:"-"`
	// KeyAPI describes a standalone encrypted API-key account provider.
	KeyAPI *KeyAPIFacts `json:"-"`
}

type KeyAPIFacts struct {
	Env      string
	Protocol string
	BaseURL  string
}

// HarnessFacts describes a managed native harness provider. Every field is a
// fact about the provider, not a product decision: which billing modes exist,
// where each mode's API lives, and where the CLI installs.
type HarnessFacts struct {
	// Billings lists accepted billing values in priority order; the first is the
	// default. A value absent from this list is never accepted.
	Billings []string
	// Endpoints maps each billing value to the API base URL that mode uses.
	Endpoints map[string]string
	// DefaultModel pre-fills an account imported from an existing key.
	DefaultModel string
	// InstallDir is the directory under the shared harness root that holds the
	// installed runtime.
	InstallDir string
	// RuntimePath is the runtime entrypoint, relative to InstallDir.
	RuntimePath []string
	// RuntimeGuard, when set, is an additional path that must also exist for the
	// install to count as complete.
	RuntimeGuard []string
	// ImportKeyProvider and ImportKeyEnv name the workflow secret a key is
	// imported from when the browser supplies none. ImportKeyFile selects an
	// on-disk provider key file instead (currently "zai-env-file").
	ImportKeyProvider string
	ImportKeyEnv      string
	ImportKeyFile     string
}

type RuntimeFacts struct {
	Binary  *BinarySpec
	Billing string
}

type BinarySpec struct {
	Name        string
	HomeSubdirs []string
	HomeGlobs   []string
	// LocalAppDataSubdirs are Windows-only install folders under
	// %LOCALAPPDATA% that a vendor's standalone installer writes to.
	LocalAppDataSubdirs []string
	LoginShellFallback  bool
	// NotFoundMsg is matched by daemon infraSubstrings; a daemon registry
	// consistency test asserts that coupling.
	NotFoundMsg string
	NoHomeMsg   string
}

// ErrorSignatures are lowercase provider-specific substrings. Quota, auth and
// billing prose signatures live only here; consumers do not union them with
// generic prose lists. Duplication across providers is expected. Consumers may
// additionally match transport-generic infrastructure signatures and the
// provider-neutral windowed-limit reset rule.
//
// Bare "402" and "payment required" must not be billing needles: generic HTTP
// status prose can appear in a coding worker's own test output, where a false
// positive would park real work on a provider fault that never happened.
type ErrorSignatures struct {
	Quota   []string
	Auth    []string
	Billing []string
	Infra   []string
}

type ModelCatalog struct {
	// Selectable contains provider-accepted model identifiers or stable CLI
	// aliases. Rows are deliberately separate substring matchers used only for
	// tier classification; exposing Rows as choices can persist invalid values
	// such as "gpt" or "mini".
	Selectable []string
	Rows       []ModelRow
	// EffortByModel records the exact provider-native workflow effort values
	// accepted for a selectable model. It is intentionally exact-ID metadata:
	// model-family routing and defaults remain consumer policy.
	EffortByModel map[string][]string
}

type ModelRow struct {
	Match string
	Exact bool
	Tier  string
}

// RuntimeID returns the provider's runtime identifier for family.
func (p Provider) RuntimeID(family string) string { return p.Slug + "-" + family }

// Tier returns the first catalog tier matching model.
func (c ModelCatalog) Tier(model string) (string, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return "", false
	}
	for _, row := range c.Rows {
		match := strings.ToLower(row.Match)
		if (row.Exact && model == match) || (!row.Exact && strings.Contains(model, match)) {
			return row.Tier, true
		}
	}
	return "", false
}

// EffortLevelsFor returns the exact model's ordered native workflow effort
// levels. IDs are trimmed for persisted-input compatibility, but deliberately
// are not case-folded or family-matched. The returned slice is safe to mutate.
func (c ModelCatalog) EffortLevelsFor(model string) []string {
	levels := c.EffortByModel[strings.TrimSpace(model)]
	return append([]string(nil), levels...)
}
