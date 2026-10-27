package providers

import "strings"

// GLM's native ZCode harness has its own credential family. It is not a
// workflow runtime and must not inherit Claude's subscription identity.
var glm = Provider{
	Slug: "glm", Vendor: "zai", DisplayName: "GLM", DocsURL: "https://zcode.z.ai/en/docs/configuration",
	Creds: apiHarnessCreds("glm"),
	Harness: &HarnessFacts{
		Billings: []string{"coding-plan", "api"},
		Endpoints: map[string]string{
			"coding-plan": "https://api.z.ai/api/coding/paas/v4",
			"api":         "https://api.z.ai/api/paas/v4",
		},
		DefaultModel:  "glm-5.3",
		InstallDir:    "zcode-terminal",
		RuntimePath:   []string{"node_modules", "zcode-app-cli", "vendor", "zcode.cjs"},
		RuntimeGuard:  []string{"node_modules", "@zcode", "tui", "package.json"},
		ImportKeyFile: "zai-env-file",
	},
}

func apiHarnessCreds(slug string) *CredFacts {
	// migrate renders the mailbox DDL with UNQUOTED identifiers, so a compound
	// harness slug like "pi-glm" cannot reach SQL unchanged: PostgreSQL parses
	// the hyphen as subtraction and the CREATE TABLE fails. The URL route
	// segment and the JSON envelope key keep the slug (both accept a hyphen);
	// only the database identifiers are hyphen-free.
	ident := strings.ReplaceAll(slug, "-", "_")
	return &CredFacts{
		Mailbox: &MailboxFacts{
			RouteSegment: slug, SetTable: "soluterminal_team_" + ident + "_cred_set",
			SyncTable: "soluterminal_team_" + ident + "_cred_sync", PolicyTable: "soluterminal_team_" + ident + "_cred_policy",
			PolicyKind: "policy", EnvelopeKey: slug + "Auth", AuthKinds: []string{"api_key"},
			Validation: "auth-kind", MaxBodyBytes: 256 << 10, MaxEnvelopeBytes: 16 << 10,
			RestrictAccountLabels: true, ReservedAccountLabel: "__team__",
		},
		TeamState: &TeamStateFacts{StateFileName: slug + "_team_provision_state.json"},
		Store:     &StoreFacts{RootDirName: slug, LiveFileName: "api-key.json", ManagedFileName: "managed.json", SourcesDirName: "sources", TeamDirName: "team"},
	}
}
