package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"soluterminal-shared/typesafe"
)

func TestCautiousAutoChecksSelectedRowOfCurrentMenu(t *testing.T) {
	safe := claudeEditPermissionScreen
	persistent := strings.Join([]string{
		"Bash command",
		"  gofmt -d src/app.go",
		"Do you want to proceed?",
		"❯ 1. Yes, and don't ask again for all Bash commands",
		"  2. No (esc)",
	}, "\n")

	if got := choiceLineForInput(safe+"\n"+persistent, "1\n"); !strings.Contains(got, "don't ask again") {
		t.Fatalf("selected stale option 1: %q", got)
	}
	if reason := cautiousAutoDenyReason(safe+"\n"+persistent, "1\n"); reason == "" {
		t.Fatal("current persistent grant passed cautious safety gate")
	}
	if got := choiceLineForInput(persistent+"\n"+safe, "1\n"); !strings.Contains(got, "❯ 1. Yes") || strings.Contains(got, "don't ask again") {
		t.Fatalf("selected stale persistent row: %q", got)
	}
}

func TestJevRequiresVisibleCurrentActionContext(t *testing.T) {
	for _, tc := range []struct {
		name string
		tail string
		want bool
	}{
		{"specific command", "Bash command\n  git status --short\nDo you want to proceed?\n❯ 1. Yes\n  2. No", true},
		{"specific edit", claudeEditPermissionScreen, true},
		{"specific suggestion", "Suggestion: Add an explanatory comment to the README draft.\nDo you want to accept this suggestion?\n❯ 1. Yes\n  2. No", true},
		{"generic menu", "Do you want to proceed?\n❯ 1. Yes\n  2. No", false},
		{"header without command", "Bash command\nDo you want to proceed?\n❯ 1. Yes\n  2. No", false},
		{"stale action", "Bash command\n  git status --short\nDo you want to proceed?\n❯ 1. Yes\n  2. No\n\nDo you want to proceed?\n❯ 1. Yes\n  2. No", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ariaJevHasActionContext(tc.tail); got != tc.want {
				t.Fatalf("ariaJevHasActionContext=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestJevContextlessMenuNeverReachesProvider(t *testing.T) {
	tail := "Do you want to proceed?\n❯ 1. Yes\n  2. No (esc)"
	a, p := ariaJevSafetyFixture(t, tail, true)
	if reason := cautiousAutoDenyReason(tail, "1\n"); reason != "" {
		t.Fatalf("fixture must pass existing cautious menu gate: %s", reason)
	}
	called := false
	a.jevClientFactory = func(string) jevSystemOneClient {
		return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
			called = true
			return ariaJevSafeResponse(), nil
		}}
	}
	if a.tryJevCautiousTerminal(context.Background(), p, p.ID, p.Title, tail, time.Now()) || called {
		t.Fatal("contextless menu must fall back without TypeSafe egress")
	}
}
