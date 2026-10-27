package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"soluterminal-shared/typesafe"
)

// Reconstructed from the 60-column Render approval that stalled on 2026-10-09.
// Some MCP tools offer only a one-time Allow and Cancel, with no persistent grant.
const codexTwoOptionMCP = `Field 1/1
Allow the render MCP server to run tool "list_logs"?

direction: backward
limit: 100
resource: ["synthetic-service"]

  › 1. Allow   Run the tool and continue
    2. Cancel  Cancel this tool call
  enter to submit | esc to cancel`

const codexFixtureStopCommand = `Would you like to run the following command?

Environment: local

Reason: May I stop the completed throwaway database
fixture?

$ /usr/lib/postgresql/16/bin/pg_ctl -D
/tmp/solu-broker-refresh-pg-20261009 stop -m fast

› 1. Yes, proceed (y)
  2. Yes, and don't ask again for commands that start with
     ` + "`/usr/lib/postgresql/16/bin/pg_ctl -D /tmp/solu-broker-\n" +
	"     refresh-pg-20261009 stop -m fast` (p)\n" + `  3. No, and tell Codex what to do differently (esc)

Press enter to confirm or esc to cancel`

func TestTwoOptionMCPPermissionRecognition(t *testing.T) {
	for _, menu := range []string{codexTwoOptionMCP, strings.Replace(codexTwoOptionMCP, "tool and continue", "tool and\n              continue", 1)} {
		if reason := autoApprovalDeclineReason(menu, true); reason != "" {
			t.Errorf("one-time MCP permission rejected: %s", reason)
		}
		if reason := jevFullCandidateDecline(ariaJevMenuEvidence(menu, true)); reason != "" {
			t.Errorf("one-time MCP permission excluded from Jev: %s", reason)
		}
	}
}

func TestTwoOptionMCPRejectsOtherMenus(t *testing.T) {
	for _, tc := range []struct{ name, menu string }{
		{"missing question", strings.Replace(codexTwoOptionMCP, `Allow the render MCP server to run tool "list_logs"?`, "Choose an action", 1)},
		{"persistent grant", strings.Replace(codexTwoOptionMCP, "1. Allow   Run the tool and continue", "1. Allow for this session", 1)},
		{"missing cancel", strings.Replace(codexTwoOptionMCP, "2. Cancel  Cancel this tool call", "2. Other", 1)},
		{"question footer", strings.Replace(codexTwoOptionMCP, "enter to submit | esc to cancel", "Enter to select · Type your answer", 1)},
		{"later question", codexTwoOptionMCP + "\nChoose a target\n› 1. Development\n  2. Production\nEnter to select"},
		{"later plan", codexTwoOptionMCP + "\nImplement this plan?\n› 1. Yes, implement this plan\n  2. No"},
		{"cancel focused", strings.Replace(strings.Replace(codexTwoOptionMCP, "› 1.", "  1.", 1), "  2.", "› 2.", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if isAutoApprovalPromptText(tc.menu, true) {
				t.Fatal("nonpermission menu admitted by local classifier")
			}
			if jevFullCandidateDecline(ariaJevMenuEvidence(tc.menu, true)) == "" {
				t.Fatal("nonpermission menu admitted by Jev precheck")
			}
		})
	}
}

func TestTwoOptionMCPAutoApprovalRoutes(t *testing.T) {
	for _, route := range []string{"direct", "jev", "fallback"} {
		t.Run(route, func(t *testing.T) {
			a, p, frames := newJevFullHarness(t, codexTwoOptionMCP)
			p.mu.Lock()
			p.Cols, p.Rows = 60, 58
			p.mu.Unlock()
			p.resizeGridEmu(60, 58)
			seedGridMenu(t, p, codexTwoOptionMCP)
			calls := 0
			a.jevClientFactory = func(string) jevSystemOneClient {
				return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
					calls++
					if route == "fallback" {
						return nil, context.DeadlineExceeded
					}
					return jevDetectYesResponse(false), nil
				}}
			}
			if route == "direct" {
				a.directAutoFire(context.Background(), p.ID, p.Title)
			} else {
				a.evaluateObservation(context.Background(), p.ID, p.Title, "")
			}
			expectFrame(t, frames, "one-time MCP approval", "\r")
			// A recognized one-time MCP permission never waits for Jev.
			if calls != 0 {
				t.Fatalf("provider calls=%d, want 0", calls)
			}
		})
	}
}

func TestTwoOptionMCPManualReplyDoesNotHoldFollowingCommand(t *testing.T) {
	p, frames := newTypingHoldPane(t, "two-option-mcp-manual")
	p.Cols, p.Rows = 60, 58
	a := testAriaWithPane(t, p)
	a.sweepStreaks = map[string]*sweepStreak{}
	a.evalLog = newAriaEvalLog()
	seedGridMenu(t, p, codexTwoOptionMCP)
	if _, err := p.Write([]byte("1")); err != nil {
		t.Fatal(err)
	}
	expectFrame(t, frames, "manual MCP choice", "1")
	if n := p.composerPendingLen(); n != 0 {
		t.Fatalf("MCP menu choice recorded as %d bytes of unsent draft", n)
	}
	atomic.StoreInt64(&p.LastInputMs, time.Now().Add(-ariaAutoTypingSettle-time.Second).UnixMilli())
	seedGridMenu(t, p, codexFixtureStopCommand)
	a.directAutoFire(context.Background(), p.ID, p.Title)
	expectFrame(t, frames, "following database stop permission", "\r")
}

func TestAutoApprovalDeclineReportsRenderedReason(t *testing.T) {
	a, p, frames, _ := newDirectAutoRepeatHarness(t, "decline-rendered-reason")
	seedGridMenu(t, p, "Implement this plan?\n› 1. Yes, implement this plan\n  2. No")
	a.directAutoFire(context.Background(), p.ID, p.Title)
	expectNoFrame(t, frames, "plan still needs review")
	if outcomes := a.recentEvalOutcomes(p.ID); len(outcomes) != 1 || outcomes[0].Reason != "plan-exit menu" {
		t.Fatalf("missing specific rendered-menu reason: %+v", outcomes)
	}
}
