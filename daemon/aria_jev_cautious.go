package main

import (
	"context"
	"time"
)

// Smart approvals for non-team installs. "Approve with caution" used to send
// every permission to the ARIA model, which only the team can reach, so free
// and no-account users got nothing approved. For them Jev is now the only
// cautious evaluator: a confident "safe" approves after the usual cancel
// window, and anything else stays with the human with a one-line reason.
// Team installs keep the ARIA path (with the opt-in Jev fast path first).

type jevCautiousMemo struct {
	pos     int64
	hash    uint64
	outcome string
	policy  ariaAutoModePolicy
}

// cautiousJevOnly reports whether this install answers cautious permissions
// with Jev alone. Unknown (no verdict hook) keeps the team path, which is
// what every install did before the public beta.
func (a *Aria) cautiousJevOnly() bool {
	return a.teamFeatures != nil && !a.teamFeatures()
}

func (a *Aria) jevCautiousSeen(paneID string, pos int64, hash uint64) (string, bool) {
	a.mu.Lock()
	m, ok := a.jevCautiousMemo[paneID]
	a.mu.Unlock()
	if !ok || m.pos != pos || m.hash != hash {
		return "", false
	}
	p, exists := a.mgr.Get(paneID)
	if !exists || !m.policy.matches(p) {
		return "", false
	}
	return m.outcome, true
}

func (a *Aria) rememberJevCautious(paneID string, pos int64, hash uint64, outcome string) {
	p, ok := a.mgr.Get(paneID)
	if !ok {
		return
	}
	a.rememberJevCautiousForPolicy(ariaAutoModePolicy{pane: p, policy: p.autoModePolicy()}, pos, hash, outcome)
}

// Serialize memo publication with mode changes. An old provider or countdown
// must not overwrite the decision belonging to a newly armed policy.
func (a *Aria) rememberJevCautiousForPolicy(policy ariaAutoModePolicy, pos int64, hash uint64, outcome string) {
	p := policy.pane
	if p == nil {
		return
	}
	p.autoModeMu.Lock()
	defer p.autoModeMu.Unlock()
	if !policy.matches(p) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.jevCautiousMemo == nil {
		a.jevCautiousMemo = map[string]jevCautiousMemo{}
	}
	a.jevCautiousMemo[p.ID] = jevCautiousMemo{pos: pos, hash: hash, outcome: outcome, policy: policy}
}

// evaluateCautiousWithJev runs the non-team cautious decision. When Jev
// doesn't approve, the permission is left for the human with a reason the
// attention alert shows.
func (a *Aria) evaluateCautiousWithJev(ctx context.Context, p *Pane, paneID, paneTitle, observationText string, started time.Time) {
	handled, outcome := a.jevCautiousDecide(ctx, p, paneID, paneTitle, observationText, started, true)
	if handled {
		return
	}
	if outcome == "" || outcome == "not_cautious" {
		return
	}
	a.recordAutoEvalOutcome(paneID, false)
	a.recordEvalOutcomeDedup(AriaEvalOutcome{
		PaneID:    paneID,
		Kind:      "declined",
		Reason:    jevCautiousReason(outcome),
		Model:     ariaJevModel,
		LatencyMs: time.Since(started).Milliseconds(),
	})
}

// jevCautiousReason turns an audit outcome into the line the user sees on
// the "needs you" alert. Every reason ends with the human answering.
func jevCautiousReason(outcome string) string {
	switch outcome {
	case "uncertain_fallback", "invalid_fallback":
		return "Smart approvals weren't confident this is safe, so it's yours to answer."
	case "denylist_fallback":
		return "Smart approvals never auto-approve this kind of request."
	case "sensitive_tail_fallback":
		return "The screen shows something that looks like a secret, so it wasn't sent for checking."
	case "empty_tail_fallback", "long_tail_fallback", "precheck_fallback", "identity_fallback":
		return "Smart approvals couldn't read this request clearly, so it's yours to answer."
	case jevCloudOutcomeQuota:
		return "Today's smart approvals are used up (100 a day). They reset tomorrow."
	case jevCloudOutcomeUnavailable:
		return "Smart approvals are unavailable right now, so this one is yours to answer."
	case "provider_fallback":
		return "Your Jev key didn't answer, so this one is yours to answer."
	case "credential_fallback":
		if runtimeSecretStore == nil {
			// No key store in this build: an own key comes only from the
			// daemon's environment, but the free account works the same.
			return "Sign in (free) to use smart approvals, or set TYPESAFE_API_KEY for your own key."
		}
		return "Smart approvals need a free account, or your own Jev key in Credentials."
	case "disabled_fallback":
		return "Smart approvals are switched off on this computer."
	case "native_fallback":
		return "Smart approvals can't check this kind of pane yet."
	}
	return "Smart approvals left this one for you."
}
