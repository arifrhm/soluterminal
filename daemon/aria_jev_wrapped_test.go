package main

import (
	"context"
	"strings"
	"testing"

	"soluterminal-shared/typesafe"
)

// Reconstructed from the 53-column pane-1 screenshot, with synthetic arguments.
// Codex's narrow MCP menu omits the sentence-ending period used by the old fixture.
const jevNarrowMCPPermission = `Field 1/1
Allow the soluterminal MCP server to run tool
"read_pane_tail"?

pane_id: pane_example
lines: 40

› 1. Allow                  Run the tool and
                            continue
  2. Allow for this session Run the tool and
                            remember this choice
                            for this session
  3. Always allow           Run the tool and
                            remember this choice
                            for future tool
                            calls
  4. Cancel                 Cancel this tool
                            call
enter to submit | esc to cancel`

func TestJevFullWrappedPermissionRoutes(t *testing.T) {
	longCommand := "Would you like to run the following\ncommand?\n\n$ printf '%s\\n' \\\n" + strings.Repeat("  example \\\n", 18) + "  done\n\n› 1. Yes, proceed ( y )\n  2. No (esc)\nPress enter to confirm or esc to cancel"
	spacedHint := strings.Replace(codexCommandApprovalScreen, "(y)", "( y )", 1)
	// Codex lays out text at word boundaries; do not feed an 80-column row
	// into a 53-column terminal and synthesize a split inside "start".
	spacedHint = strings.Replace(spacedHint, "commands that start with", "commands that\n     start with", 1)
	for _, tc := range []struct {
		name, tail string
		cols       uint16
	}{
		{"narrow MCP", jevNarrowMCPPermission, 53},
		{"workspace MCP", strings.Replace(strings.Replace(jevNarrowMCPPermission, "read_pane_tail", "switch_workspace", 1), "pane_id: pane_example\nlines: 40", "workspace: Soluterminal", 1), 49},
		{"long wrapped command", longCommand, 53},
		{"spaced keyboard hint", spacedHint, 53},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if reason := ariaJevScopeDecline(tc.tail); reason != "" {
				t.Fatalf("valid once permission declined: %s", reason)
			}
			evidence := ariaJevMenuEvidence(tc.tail, true)
			if !jevPermissionCandidateText(evidence, true) {
				t.Fatalf("permission did not reach Jev: %q", evidence)
			}
			a, p, frames := newJevFullHarness(t, tc.tail)
			// Match the tall/narrow live pane rather than cropping to the
			// shared harness's default 24-row terminal.
			p.mu.Lock()
			p.Cols, p.Rows = tc.cols, 61
			p.mu.Unlock()
			p.subsMu.Lock()
			p.gridEmu.Resize(int(tc.cols), 61)
			p.subsMu.Unlock()
			seedGridMenu(t, p, tc.tail)
			calls := 0
			a.jevClientFactory = func(string) jevSystemOneClient {
				return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
					calls++
					return jevDetectYesResponse(false), nil
				}}
			}
			a.evaluateObservation(context.Background(), p.ID, p.Title, "")
			t.Logf("provider calls=%d outcomes=%+v", calls, a.recentEvalOutcomes(p.ID))
			expectFrame(t, frames, "wrapped permission approval", "\r")
			// Recognized wrapped permissions fire locally; only unrecognized
			// ones wait for Jev.
			want := 1
			if isAutoApprovalPromptText(ariaJevPermissionTail(p, true), p.HasCodexDescendant() || p.HasMuseDescendant()) {
				want = 0
			}
			if calls != want {
				t.Fatalf("Jev calls=%d, want %d", calls, want)
			}
		})
	}
}

func TestJevFullUndiscoveredThreadPreservesKnownLocalApproval(t *testing.T) {
	a, p, frames := newJevFullHarness(t, jevNarrowMCPPermission)
	p.codexIdentityBlocked.Store(true)
	a.jevClientFactory = func(string) jevSystemOneClient {
		t.Fatal("unknown thread must not reach provider")
		return nil
	}
	a.evaluateObservation(context.Background(), p.ID, p.Title, "")
	expectFrame(t, frames, "known local permission with undiscovered thread", "\r")
	if got := a.recentEvalOutcomes(p.ID); len(got) == 0 || got[0].Model != "direct" || got[0].Kind != "fired" {
		t.Fatalf("missing local fallback attribution: %+v", got)
	}
}

func TestJevFullThreadBecomingUnknownDiscardsProviderAnswer(t *testing.T) {
	a, p, frames := newJevFullHarness(t, jevNovelToolMenu)
	calls := 0
	a.jevClientFactory = func(string) jevSystemOneClient {
		return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
			calls++
			p.codexIdentityBlocked.Store(true)
			return jevDetectYesResponse(false), nil
		}}
	}
	a.evaluateObservation(context.Background(), p.ID, p.Title, "")
	expectNoFrame(t, frames, "thread changed during provider call")
	if calls != 1 {
		t.Fatalf("Jev calls=%d, want 1", calls)
	}
}

func TestJevWrappedOnceLabelsRejectExtendedGrants(t *testing.T) {
	for _, label := range []string{"Allow Run the tool and continue always", "Allow Run the tool and continue for this session", "Yes, proceed ( always )", "Yes, proceed ( y ) and don't ask again"} {
		if ariaJevOnceLabel(label) {
			t.Errorf("extended grant admitted: %q", label)
		}
	}
}
