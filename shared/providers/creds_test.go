package providers

import "testing"

var nonCredentialProviders = map[string]bool{
	"anthropic":    true,
	"openai":       true,
	"gemini":       true,
	"grok":         true,
	"local-openai": true,
	"openrouter":   true,
	"typesafe":     true,
}

func TestShippedCredentialFactsValidate(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatal(err)
	}
	for _, p := range All() {
		if nonCredentialProviders[p.Slug] {
			if p.Creds != nil {
				t.Fatalf("allowlisted non-credential provider %q has credential facts", p.Slug)
			}
			continue
		}
		if p.Creds == nil || p.Creds.Mailbox == nil || p.Creds.TeamState == nil || p.Creds.Store == nil {
			t.Fatalf("provider %q has incomplete credential facts", p.Slug)
		}
	}
}

func TestCredentialFactsValidation(t *testing.T) {
	base, _ := Get("claude")
	tests := []struct {
		name string
		edit func(*Provider)
	}{
		{"missing table", func(p *Provider) { p.Creds.Mailbox.SetTable = "" }},
		{"bad validation", func(p *Provider) { p.Creds.Mailbox.Validation = "decode-secrets" }},
		{"envelope-only without max envelope", func(p *Provider) {
			p.Creds.Mailbox.Validation = "envelope-only"
			p.Creds.Mailbox.MaxEnvelopeBytes = 0
		}},
		{"reserved label without restriction", func(p *Provider) {
			p.Creds.Mailbox.ReservedAccountLabel = "__team__"
			p.Creds.Mailbox.RestrictAccountLabels = false
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := cloneProviderCreds(base)
			tt.edit(&p)
			if err := validateProviders([]Provider{p}); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	t.Run("duplicate route", func(t *testing.T) {
		claude, _ := Get("claude")
		codex, _ := Get("codex")
		a, b := cloneProviderCreds(claude), cloneProviderCreds(codex)
		b.Creds.Mailbox.RouteSegment = a.Creds.Mailbox.RouteSegment
		if err := validateProviders([]Provider{a, b}); err == nil {
			t.Fatal("expected duplicate route error")
		}
	})
}

func cloneProviderCreds(p Provider) Provider {
	c := *p.Creds
	m := *c.Mailbox
	t := *c.TeamState
	s := *c.Store
	c.Mailbox, c.TeamState, c.Store = &m, &t, &s
	p.Creds = &c
	return p
}
