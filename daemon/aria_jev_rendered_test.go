package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"soluterminal-shared/typesafe"
)

// Claude repaints only the changed rows. The raw byte tail may therefore end
// with options 1 and 4 while the visible screen still has all four options.
const jevClaudePartialRedrawMenu = `Bash command
  Prepare local retry script
  sed 's/old/new/g' /tmp/retry.sh > /tmp/retry-next.sh
  This command requires approval
  Do you want to proceed?
 ❯ 1. Yes
   2. Yes, and don’t ask again for: sed *
   3. Yes, and switch to auto mode · auto mode
      handles these prompts for you
   4. No
 Esc to cancel · Tab to amend`

func TestJevFullRevalidatesRenderedMenuAfterPartialRawRedraw(t *testing.T) {
	a, p, frames := newJevFullHarness(t, jevClaudePartialRedrawMenu)
	p.forceCodexForTest = false
	partial := "Do you want to proceed?\n ❯ 1. Yes\n   4. No\n Esc to cancel · Tab to amend"
	appendDirectAutoMenuRedraw(t, p, partial)
	visible := ariaJevPermissionTail(p, true)
	rawHash := approvalPromptFingerprint(tailOfPaneAdaptive(p, 30), false)
	_, _, renderedHash, ok := currentChoicePromptIdentity(p, "")
	if !ok || rawHash == approvalPromptFingerprint(visible, false) {
		t.Fatal("fixture must reproduce raw/rendered disagreement")
	}
	if renderedHash != approvalPromptFingerprint(visible, false) {
		t.Fatal("current permission identity must use the rendered menu")
	}
	calls := 0
	a.jevClientFactory = func(string) jevSystemOneClient {
		return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
			calls++
			return jevDetectYesResponse(false), nil
		}}
	}
	a.evaluateObservation(context.Background(), p.ID, p.Title, "")
	expectFrame(t, frames, "rendered once-only permission", "\r")
	// The rendered menu is a recognized Claude permission, so it fires on
	// the local route without a provider round trip.
	if calls != 0 {
		t.Fatalf("provider calls=%d, want 0", calls)
	}
	outcomes := a.recentEvalOutcomes(p.ID)
	if len(outcomes) == 0 || outcomes[0].Kind != "fired" || outcomes[0].Model != "direct" {
		t.Fatalf("outcomes=%+v", outcomes)
	}
}

func TestJevFullRenderedRecheckRejectsChangedActionOrSelection(t *testing.T) {
	for name, changed := range map[string]string{
		"action":    strings.Replace(jevNovelToolMenu, "src/app.go", "src/other.go", 1),
		"selection": strings.Replace(strings.Replace(jevNovelToolMenu, "❯ 1.", "  1.", 1), "  2.", "❯ 2.", 1),
	} {
		t.Run(name, func(t *testing.T) {
			a, p, frames := newJevFullHarness(t, jevNovelToolMenu)
			a.jevClientFactory = func(string) jevSystemOneClient {
				return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
					seedGridMenu(t, p, changed)
					return jevDetectYesResponse(false), nil
				}}
			}
			a.evaluateObservation(context.Background(), p.ID, p.Title, "")
			expectNoFrame(t, frames, "changed visible menu")
		})
	}
}

func TestJevTransientRechecksDoNotSuspendRecovery(t *testing.T) {
	for _, failAt := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			a, p, frames := newJevFullHarness(t, jevClaudePartialRedrawMenu)
			tail := ariaJevPermissionTail(p, true)
			_, pos, fp, ok := currentChoicePromptIdentity(p, tail)
			if !ok {
				t.Fatal("missing identity")
			}
			proof := &jevAmbiguousProof{dataDir: a.dataDir, mode: "always", full: true, classified: true, pos: pos, fingerprint: fp, tailDigest: sha256.Sum256([]byte(ariaJevMenuEvidence(tail, true)))}
			bindJevTestProof(t, a, p, proof)
			probe := proof.probe
			for attempt := 0; attempt < 4; attempt++ {
				probes := 0
				proof.probe = func(ctx context.Context, pid int) (jevProcessIdentity, error) {
					probes++
					if probes == failAt {
						return jevProcessIdentity{}, errJevProcessUnavailable
					}
					return probe(ctx, pid)
				}
				a.directAutoFireWithJevProof(context.Background(), p.ID, p.Title, proof)
				expectNoFrame(t, frames, "transient recheck")
				if a.shouldSkipSweeper(p.ID, pos) {
					t.Fatal("transient observation suspended recovery")
				}
				outcomes := a.recentEvalOutcomes(p.ID)
				if len(outcomes) == 0 || outcomes[0].Kind != "held" {
					t.Fatalf("outcomes=%+v", outcomes)
				}
			}
			proof.probe = probe
			a.directAutoFireWithJevProof(context.Background(), p.ID, p.Title, proof)
			expectFrame(t, frames, "recovered current observation", "\r")
		})
	}
}
