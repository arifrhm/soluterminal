package main

import (
	"context"
	"strings"
	"testing"

	"soluterminal-shared/typesafe"
)

// The rendered 40-column, 26-row Shinoque pane: the fourth option is
// below the viewport and the right edge clips the Escape hint.
const codexMcpClippedPermission = `

  Field 1/1
  Allow the soluterminal MCP server to
  run tool "send_input"?

  pane_id: owner_wft_example
  text: Read-only tooling discovery
  for current UI evidence: cach...

  › 1. Allow               Run the
                           tool and
                           continue
    2. Allow for this      Run the
       session             tool and
                           remember
                           this choice
                           for this
                           session
    3. Always allow        Run the
                           tool and
                           remember
                           this choice
                           for future
  option 1/4 | enter to submit | esc t

`

func TestCodexMcpClippedPermissionRecognition(t *testing.T) {
	for _, hint := range []string{"esc", "esc t", "esc to", "esc to c", "esc to cancel"} {
		t.Run(hint, func(t *testing.T) {
			text := strings.Replace(codexMcpClippedPermission, "| esc t", "| "+hint, 1)
			if reason := autoApprovalDeclineReason(text, true); reason != "" {
				t.Fatalf("clipped permission declined: %s", reason)
			}
			if isAutoApprovalPromptText(text, false) {
				t.Fatal("clipped MCP recognition must require Codex identity")
			}
			evidence := ariaJevMenuEvidence(text, true)
			if reason := jevFullCandidateDecline(evidence); reason != "" {
				t.Fatalf("clipped permission excluded from Jev: %s; evidence=%q", reason, evidence)
			}
		})
	}

	for name, text := range map[string]string{
		"missing escape hint":         strings.Replace(codexMcpClippedPermission, " | esc t", "", 1),
		"unrelated escape hint":       strings.Replace(codexMcpClippedPermission, "esc t", "esc to interrupt", 1),
		"wrong option count":          strings.Replace(codexMcpClippedPermission, "option 1/4", "option 1/3", 1),
		"wrong selection":             strings.Replace(codexMcpClippedPermission, "option 1/4", "option 2/4", 1),
		"missing permission question": strings.Replace(codexMcpClippedPermission, "Allow the soluterminal MCP server to", "Which way should we proceed to", 1),
		"missing session row":         strings.Replace(codexMcpClippedPermission, "2. Allow for this", "2. Continue with this", 1),
		"missing persistent row":      strings.Replace(codexMcpClippedPermission, "3. Always allow", "3. Continue", 1),
		"focused persistent grant":    strings.Replace(codexMcpClippedPermission, "1. Allow               Run the", "1. Always allow        Run the", 1),
		"focused session grant":       strings.Replace(codexMcpClippedPermission, "1. Allow               Run the", "1. Allow for session   Run the", 1),
		"question footer":             strings.Replace(codexMcpClippedPermission, "enter to submit", "enter to select", 1),
		"trailing question":           codexMcpClippedPermission + "Which option?\n› 1. Allow\n  2. No\nEnter to select\n",
		"stale permission header":     codexMcpClippedPermission + "Choose a policy\n› 1. Allow\n  2. Allow for this session\n  3. Always allow\noption 1/4 | enter to submit | esc t\n",
	} {
		t.Run(name, func(t *testing.T) {
			if isAutoApprovalPromptText(text, true) {
				t.Fatal("incomplete, stale, or nonpermission menu was approved")
			}
		})
	}
}

func TestCodexMcpClippedPermissionAutoFire(t *testing.T) {
	for _, route := range []string{"Jev", "local rollback", "undiscovered thread"} {
		t.Run(route, func(t *testing.T) {
			a, p, frames := newJevFullHarness(t, codexMcpClippedPermission)
			p.mu.Lock()
			p.Cols, p.Rows = 40, 26
			p.mu.Unlock()
			p.subsMu.Lock()
			p.gridEmu.Resize(40, 26)
			p.subsMu.Unlock()
			seedGridMenu(t, p, codexMcpClippedPermission)
			if route == "local rollback" {
				t.Setenv(ariaJevFullEnv, "0")
			}
			if route == "undiscovered thread" {
				p.codexIdentityBlocked.Store(true)
			}
			calls := 0
			a.jevClientFactory = func(string) jevSystemOneClient {
				return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
					calls++
					return jevDetectYesResponse(false), nil
				}}
			}
			a.evaluateObservation(context.Background(), p.ID, p.Title, "")
			expectFrame(t, frames, "clipped MCP once-only approval", "\r")
			expectNoFrame(t, frames, "clipped MCP must send only one Enter")
			// Recognized locally on every route, so Jev is never consulted.
			wantCalls, wantModel := 0, "direct"
			if calls != wantCalls {
				t.Fatalf("provider calls=%d, want %d", calls, wantCalls)
			}
			if got := a.recentEvalOutcomes(p.ID); len(got) == 0 || got[0].Kind != "fired" || got[0].Model != wantModel {
				t.Fatalf("unexpected approval outcome: %+v", got)
			}
		})
	}
}
