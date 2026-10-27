package main

import "testing"

func TestConfirmRiskLevelDefaults(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"send_input", "resolve_permission"} {
		if got := confirmRiskLevel(confirmOpts{Tool: tool}); got != "medium" {
			t.Fatalf("confirmRiskLevel(%s) = %s, want medium", tool, got)
		}
	}
	if got := confirmRiskLevel(confirmOpts{Tool: "send_input", Risk: "low"}); got != "low" {
		t.Fatalf("explicit risk = %s, want low", got)
	}
}

func TestRiskForSendInput(t *testing.T) {
	t.Parallel()
	if got := riskForSendInput("1\n"); got != "low" {
		t.Fatalf("riskForSendInput(menu reply) = %s, want low", got)
	}
	if got := riskForSendInput("git push origin main\n"); got != "high" {
		t.Fatalf("riskForSendInput(git push) = %s, want high", got)
	}
	if got := riskForSendInput("hello"); got != "medium" {
		t.Fatalf("riskForSendInput(text) = %s, want medium", got)
	}
}
