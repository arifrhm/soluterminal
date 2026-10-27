package providers

import (
	"strings"
	"testing"
)

func TestRegistry(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatal(err)
	}
	all := All()
	if len(all) != 14 || all[0].Slug != "anthropic" || all[1].Slug != "claude" || all[2].Slug != "codex" || all[3].Slug != "deepseek" || all[4].Slug != "gemini" || all[5].Slug != "glm" || all[6].Slug != "grok" || all[7].Slug != "local-openai" || all[8].Slug != "muse" || all[9].Slug != "openai" || all[10].Slug != "openrouter" || all[11].Slug != "pi" || all[12].Slug != "pi-glm" || all[13].Slug != "typesafe" {
		t.Fatalf("All() = %+v", all)
	}
	p, family, facts, ok := ByRuntimeID("claude-headless")
	if !ok || p.Slug != "claude" || family != "headless" || facts.Binary == nil || facts.Binary.Name != "claude" {
		t.Fatalf("ByRuntimeID(claude-headless) = %+v, %q, %+v, %v", p, family, facts, ok)
	}
	p, family, facts, ok = ByRuntimeID("grok-headless")
	if !ok || p.Vendor != "xai" || family != "headless" || facts.Binary != nil || facts.Billing != "api" {
		t.Fatalf("ByRuntimeID(grok-headless) = %+v, %q, %+v, %v", p, family, facts, ok)
	}
	p, family, facts, ok = ByRuntimeID("deepseek-headless")
	if !ok || p.Vendor != "deepseek" || family != "headless" || facts.Binary != nil || facts.Billing != "api" {
		t.Fatalf("ByRuntimeID(deepseek-headless) = %+v, %q, %+v, %v", p, family, facts, ok)
	}
	p, family, facts, ok = ByRuntimeID("openrouter-headless")
	if !ok || p.Vendor != "openrouter" || family != "headless" || facts.Binary != nil || facts.Billing != "api" {
		t.Fatalf("ByRuntimeID(openrouter-headless) = %+v, %q, %+v, %v", p, family, facts, ok)
	}
	p, family, facts, ok = ByRuntimeID("local-openai-headless")
	if !ok || p.Vendor != "local-openai" || family != "headless" || facts.Binary != nil || facts.Billing != "api" {
		t.Fatalf("ByRuntimeID(local-openai-headless) = %+v, %q, %+v, %v", p, family, facts, ok)
	}
	p, family, facts, ok = ByRuntimeID("typesafe-api")
	if !ok || p.Vendor != "typesafe" || family != "api" || facts.Binary != nil || facts.Billing != "api" {
		t.Fatalf("ByRuntimeID(typesafe-api) = %+v, %q, %+v, %v", p, family, facts, ok)
	}
	for _, id := range []string{"fake", "nope-headless"} {
		if _, _, _, ok := ByRuntimeID(id); ok {
			t.Fatalf("ByRuntimeID(%q) unexpectedly succeeded", id)
		}
	}
}

func TestModelCatalogTiers(t *testing.T) {
	tests := []struct {
		vendor, model, tier string
		known               bool
	}{
		{"anthropic", "haiku", TierFast, true},
		{"anthropic", "claude-opus-4-8", TierPremium, true},
		{"anthropic", "claude-fable-5", TierFrontier, true},
		{"openai", "o4-mini", TierFast, true},
		{"openai", "gpt-6-astra", TierFrontier, true},
		{"openai", "gpt-6-astra-2026-09-05", TierFrontier, true},
		{"openai", "gpt-6.1-sol", TierPremium, true},
		{"openai", "gpt-6.1-sol-preview", TierPremium, true},
		{"openai", "gpt-6-sol", TierPremium, true},
		{"openai", "gpt-6-sol-preview", TierPremium, true},
		{"openai", "gpt-6-luna", TierFast, true},
		{"openai", "gpt-6-luna-preview", TierFast, true},
		{"openai", "gpt-5.6-sol", TierPremium, true},
		{"openai", "gpt-5.6-sol-preview", TierPremium, true},
		{"openai", "gpt-5.6-terra", TierStandard, true},
		{"openai", "gpt-5.6-terra-preview", TierStandard, true},
		{"openai", "gpt-5.6-luna", TierFast, true},
		{"openai", "gpt-5.6-luna-preview", TierFast, true},
		{"openai", "gpt-5-codex", TierStandard, true},
		{"openai", "gpt-5-pro", TierPremium, true},
		{"openai", "", "", false},
		{"meta", "muse-spark-1.2-contributor", TierStandard, true},
		{"meta", "muse-x", "", false},
		{"deepseek", "deepseek-reasoner", "", false},
		{"deepseek", "deepseek-flash", TierStandard, true},
		{"deepseek", "deepseek-v4-flash-vision-exp", TierStandard, true},
		{"deepseek", "deepseek-chat", "", false},
		{"deepseek", "deepseek-v4-pro", "", false},
		{"google", "gemini-3.8-flash", TierFast, true},
		{"xai", "grok-code-fast-1", TierFast, true},
		{"xai", "grok-4-mini", TierFast, true},
		{"xai", "grok-4-fast", TierStandard, true},
		{"xai", "grok-4", TierFrontier, true},
		{"xai", "grok-3", TierStandard, true},
		{"openrouter", "meta/muse-spark-1.2", TierStandard, true},
		{"openrouter", "meta/muse-spark-1.2-contributor", TierStandard, true},
		{"openrouter", "meta/muse-glimmer-30b", TierFast, true},
		{"openrouter", "anthropic/claude-opus-5", TierPremium, true},
		{"openrouter", "google/gemini-3.7-flash", TierFast, true},
		{"openrouter", "x-ai/grok-4.6", "", false},
	}
	for _, test := range tests {
		p, ok := ByVendor(test.vendor)
		if !ok {
			t.Fatalf("ByVendor(%q) failed", test.vendor)
		}
		got, known := p.Models.Tier(test.model)
		if got != test.tier || known != test.known {
			t.Errorf("%s Tier(%q) = (%q,%v), want (%q,%v)", test.vendor, test.model, got, known, test.tier, test.known)
		}
	}
}

func TestDeepSeekCurrentCatalogAndRetiredAliases(t *testing.T) {
	for _, slug := range []string{"deepseek", "pi"} {
		p, ok := Get(slug)
		if !ok {
			t.Fatalf("provider %q missing", slug)
		}
		if got := strings.Join(p.Models.Selectable, "|"); got != DeepSeekFlashModel+"|"+DeepSeekVisionModel {
			t.Fatalf("provider %q selectable=%q", slug, got)
		}
		for _, retired := range []string{"deepseek-v4-pro", "DEEPSEEK-V4-PRO", "deepseek/deepseek-v4-pro", "deepseek-v4-flash", "deepseek-chat", "deepseek-reasoner"} {
			if _, ok := p.Models.Tier(retired); ok {
				t.Fatalf("provider %q tier-resolved retired model %q", slug, retired)
			}
		}
	}
}

func TestDeepSeekDispatchPolicy(t *testing.T) {
	for _, raw := range []string{"", "  ", "deepseek-v4-pro", " DEEPSEEK-V4-PRO ", "deepseek/deepseek-v4-pro", "deepseek-v4-flash", "deepseek-chat", "deepseek-reasoner"} {
		if got, err := CanonicalDeepSeekDispatchModel(raw); err != nil || got != DeepSeekFlashModel {
			t.Errorf("CanonicalDeepSeekDispatchModel(%q)=(%q,%v)", raw, got, err)
		}
	}
	if got, err := CanonicalDeepSeekDispatchModel("deepseek/" + DeepSeekVisionModel); err != nil || got != DeepSeekVisionModel {
		t.Fatalf("vision canonical=(%q,%v)", got, err)
	}
	for _, raw := range []string{"future-model", "deepseek-v4-pro-preview", "deepseek//deepseek-v4-pro"} {
		if _, err := CanonicalDeepSeekDispatchModel(raw); err == nil {
			t.Errorf("CanonicalDeepSeekDispatchModel(%q) unexpectedly succeeded", raw)
		}
	}
	for _, raw := range []string{"deepseek-v4-pro", "DEEPSEEK/deepseek-v4-flash", "vendor/deepseek-chat", "deepseek-reasoner", "deepseek-v4-pro-preview"} {
		if !HasRetiredDeepSeekReference(raw) {
			t.Errorf("HasRetiredDeepSeekReference(%q)=false", raw)
		}
	}
	for _, raw := range []string{"deepseek-flash", DeepSeekVisionModel, "unrelated-chat"} {
		if HasRetiredDeepSeekReference(raw) {
			t.Errorf("HasRetiredDeepSeekReference(%q)=true", raw)
		}
	}
	for _, raw := range []string{DeepSeekFlashModel, DeepSeekVisionModel} {
		if err := ValidateDeepSeekSelector(raw); err != nil {
			t.Errorf("ValidateDeepSeekSelector(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{"", " deepseek-flash ", "DEEPSEEK-FLASH", "deepseek-v4-pro", "deepseek/deepseek-v4-pro"} {
		if err := ValidateDeepSeekSelector(raw); err == nil {
			t.Errorf("ValidateDeepSeekSelector(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestCodexSelectableModelsAndExactEffortFacts(t *testing.T) {
	p, ok := Get("codex")
	if !ok {
		t.Fatal("codex provider missing")
	}
	want := []string{"gpt-6.1-sol", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.6-codex", "gpt-5.5", "gpt-5.4"}
	wantTiers := map[string]string{
		"gpt-6.1-sol": TierPremium, "gpt-6-astra": TierFrontier, "gpt-6-sol": TierPremium, "gpt-6-luna": TierFast, "gpt-5.6-sol": TierPremium,
		"gpt-5.6-terra": TierStandard, "gpt-5.6-luna": TierFast,
		"gpt-5.6-codex": TierStandard, "gpt-5.5": TierStandard, "gpt-5.4": TierStandard,
	}
	if strings.Join(p.Models.Selectable, "|") != strings.Join(want, "|") {
		t.Fatalf("codex selectable=%v, want %v", p.Models.Selectable, want)
	}
	newModels := map[string]bool{"gpt-6.1-sol": true, "gpt-6-astra": true, "gpt-6-sol": true, "gpt-6-luna": true, "gpt-5.6-sol": true, "gpt-5.6-terra": true, "gpt-5.6-luna": true}
	for _, model := range want {
		if tier, ok := p.Models.Tier(model); !ok || tier != wantTiers[model] {
			t.Fatalf("%s tier=(%q,%v), want %q", model, tier, ok, wantTiers[model])
		}
		levels := p.Models.EffortLevelsFor("  " + model + "  ")
		if len(levels) == 0 {
			t.Fatalf("%s has no effort facts", model)
		}
		hasMax := false
		for _, level := range levels {
			if level == "ultra" {
				t.Fatalf("%s advertises unsupported workflow effort ultra", model)
			}
			hasMax = hasMax || level == "max"
		}
		if hasMax != newModels[model] {
			t.Fatalf("%s levels=%v, max presence=%v", model, levels, hasMax)
		}
		levels[0] = "mutated"
		if got := p.Models.EffortLevelsFor(model)[0]; got != "low" {
			t.Fatalf("EffortLevelsFor returned shared storage: first=%q", got)
		}
	}
	if levels := p.Models.EffortLevelsFor("GPT-6-ASTRA"); levels != nil {
		t.Fatalf("effort lookup case-folded exact id: %v", levels)
	}
	if levels := p.Models.EffortLevelsFor("gpt-6-astra-preview"); levels != nil {
		t.Fatalf("effort lookup family-matched exact id: %v", levels)
	}
}

func TestSelectableModelsArePersistableCatalogChoices(t *testing.T) {
	for _, provider := range All() {
		seen := map[string]bool{}
		for _, model := range provider.Models.Selectable {
			if strings.TrimSpace(model) == "" {
				t.Fatalf("%s has a blank selectable model", provider.Slug)
			}
			if seen[model] {
				t.Fatalf("%s repeats selectable model %q", provider.Slug, model)
			}
			seen[model] = true
			if _, known := provider.Models.Tier(model); !known {
				t.Fatalf("%s selectable model %q is not tier-classifiable", provider.Slug, model)
			}
			for _, fragment := range []string{"gpt", "mini", "spark", "nano", "pro", "codex"} {
				if model == fragment {
					t.Fatalf("%s exposes classifier matcher fragment %q as a persisted model choice", provider.Slug, model)
				}
			}
		}
	}
}

func TestValidateRejectsInvalidSelectableModels(t *testing.T) {
	original := registry
	defer func() { registry = original }()
	base := Provider{Slug: "catalog-test", Vendor: "catalog-test", Models: ModelCatalog{
		Rows: []ModelRow{{Match: "known", Tier: TierStandard}},
	}}
	for _, test := range []struct {
		name       string
		selectable []string
		want       string
	}{
		{name: "blank", selectable: []string{" "}, want: "empty selectable"},
		{name: "duplicate", selectable: []string{"known", "known"}, want: "repeats selectable"},
		{name: "unclassified", selectable: []string{"alien"}, want: "has no tier matcher"},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			candidate.Models.Selectable = test.selectable
			registry = []Provider{candidate}
			if err := Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate()=%v, want error containing %q", err, test.want)
			}
		})
	}
}

func TestValidateRejectsInvalidModelEffortFacts(t *testing.T) {
	base := Provider{Slug: "catalog-test", Vendor: "catalog-test", Models: ModelCatalog{
		Selectable: []string{"known"},
		Rows:       []ModelRow{{Match: "known", Tier: TierStandard}},
	}}
	for _, test := range []struct {
		name   string
		facts  map[string][]string
		needle string
	}{
		{name: "orphaned", facts: map[string][]string{"other": {"low"}}, needle: "non-selectable"},
		{name: "blank", facts: map[string][]string{"known": {"low", " "}}, needle: "blank effort"},
		{name: "uppercase", facts: map[string][]string{"known": {"High"}}, needle: "non-lowercase"},
		{name: "duplicate", facts: map[string][]string{"known": {"low", "low"}}, needle: "repeats effort"},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			candidate.Models.EffortByModel = test.facts
			if err := validateProviders([]Provider{candidate}); err == nil || !strings.Contains(err.Error(), test.needle) {
				t.Fatalf("validateProviders()=%v, want error containing %q", err, test.needle)
			}
		})
	}
}

func TestValidateErrorSignatures(t *testing.T) {
	original := registry
	defer func() { registry = original }()

	tests := []struct {
		name       string
		errors     ErrorSignatures
		set        string
		wantNeedle string
	}{
		{name: "uppercase", errors: ErrorSignatures{Quota: []string{"Hit Your Limit"}}, set: "quota", wantNeedle: "Hit Your Limit"},
		{name: "whitespace", errors: ErrorSignatures{Billing: []string{" \t"}}, set: "billing", wantNeedle: `\t`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry = []Provider{{Slug: "bad-provider", Vendor: "vendor", Errors: test.errors}}
			err := Validate()
			if err == nil {
				t.Fatal("Validate unexpectedly passed")
			}
			for _, want := range []string{"bad-provider", test.set, test.wantNeedle} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("Validate error %q does not name %q", err, want)
				}
			}
		})
	}
}

func TestHarnessFacts(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatal(err)
	}
	if got := HarnessSlugs(); len(got) != 4 || got[0] != "deepseek" || got[1] != "glm" || got[2] != "pi" || got[3] != "pi-glm" {
		t.Fatalf("HarnessSlugs() = %v", got)
	}
	if !IsHarness("deepseek") || !IsHarness("glm") || !IsHarness("pi") || !IsHarness("pi-glm") || IsHarness("claude") || IsHarness("nope") {
		t.Fatal("IsHarness mismatch")
	}
	deepseek, _ := Get("deepseek")
	if deepseek.Harness == nil || deepseek.Harness.Endpoints["api"] != "https://api.deepseek.com" ||
		deepseek.Harness.DefaultModel != "deepseek-flash" || deepseek.Harness.InstallDir != "deepseek" {
		t.Fatalf("deepseek harness facts = %+v", deepseek.Harness)
	}
	glm, _ := Get("glm")
	if glm.Harness == nil || len(glm.Harness.Billings) != 2 ||
		glm.Harness.Endpoints["coding-plan"] != "https://api.z.ai/api/coding/paas/v4" ||
		glm.Harness.Endpoints["api"] != "https://api.z.ai/api/paas/v4" ||
		len(glm.Harness.RuntimeGuard) == 0 {
		t.Fatalf("glm harness facts = %+v", glm.Harness)
	}
	pi, _ := Get("pi")
	if pi.Harness == nil || pi.Harness.Endpoints["api"] != "https://api.deepseek.com" ||
		pi.Harness.DefaultModel != "deepseek-flash" || pi.Harness.InstallDir != "pi" ||
		pi.Harness.ImportKeyEnv != "DEEPSEEK_API_KEY" ||
		pi.Harness.RuntimePath[len(pi.Harness.RuntimePath)-1] != "cli.js" {
		t.Fatalf("pi harness facts = %+v", pi.Harness)
	}
	piGlm, _ := Get("pi-glm")
	if piGlm.Harness == nil || len(piGlm.Harness.Billings) != 1 || piGlm.Harness.Billings[0] != "coding-plan" ||
		piGlm.Harness.Endpoints["coding-plan"] != "https://api.z.ai/api/coding/paas/v4" ||
		piGlm.Harness.DefaultModel != "glm-5.3-flash" || piGlm.Harness.InstallDir != "pi" ||
		piGlm.Harness.ImportKeyFile != "zai-env-file" ||
		piGlm.Harness.RuntimePath[len(piGlm.Harness.RuntimePath)-1] != "cli.js" {
		t.Fatalf("pi-glm harness facts = %+v", piGlm.Harness)
	}
	if tier, ok := piGlm.Models.Tier("glm-5.3-flash"); !ok || tier != TierStandard {
		t.Fatalf("pi-glm flash tier = %q, %v", tier, ok)
	}
	if tier, ok := piGlm.Models.Tier("glm-5.3"); !ok || tier != TierPremium {
		t.Fatalf("pi-glm flagship tier = %q, %v", tier, ok)
	}
}

func TestValidateRejectsIncompleteHarnessFacts(t *testing.T) {
	base := Provider{Slug: "harness-test", Vendor: "harness-test"}
	for _, test := range []struct {
		name   string
		facts  *HarnessFacts
		needle string
	}{
		{name: "no-billings", facts: &HarnessFacts{InstallDir: "x", RuntimePath: []string{"bin.js"}}, needle: "no billing modes"},
		{name: "missing-endpoint", facts: &HarnessFacts{Billings: []string{"api"}, InstallDir: "x", RuntimePath: []string{"bin.js"}}, needle: "has no endpoint"},
		{name: "unlisted-endpoint", facts: &HarnessFacts{Billings: []string{"api"}, Endpoints: map[string]string{"api": "https://a", "other": "https://b"}, InstallDir: "x", RuntimePath: []string{"bin.js"}}, needle: "unlisted billing"},
		{name: "no-install", facts: &HarnessFacts{Billings: []string{"api"}, Endpoints: map[string]string{"api": "https://a"}}, needle: "install facts are incomplete"},
		{name: "both-import-sources", facts: &HarnessFacts{Billings: []string{"api"}, Endpoints: map[string]string{"api": "https://a"}, InstallDir: "x", RuntimePath: []string{"bin.js"}, ImportKeyEnv: "K", ImportKeyFile: "f"}, needle: "both a file and a secret"},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			candidate.Harness = test.facts
			if err := validateProviders([]Provider{candidate}); err == nil || !strings.Contains(err.Error(), test.needle) {
				t.Fatalf("validateProviders()=%v, want error containing %q", err, test.needle)
			}
		})
	}
}
