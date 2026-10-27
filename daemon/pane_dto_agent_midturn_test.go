package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestPaneDTOAlwaysEmitsAgentMidTurn locks the wire contract: agentMidTurn is
// emitted on EVERY pane DTO, including the false case. The field carries no
// `omitempty`, because a literal false is meaningful — it is what tells the
// client "this pane is settled, switch one-click" instead of "switch when done".
// If it were omitted on false, the client would fall back to the coarse recency
// states and re-introduce the very bug this fixes (an idle terminal pane in its
// ~3s post-turn redraw tail wrongly offering the deferred confirm).
func TestPaneDTOAlwaysEmitsAgentMidTurn(t *testing.T) {
	t.Parallel()
	settled, err := json.Marshal(paneDTO{AgentMidTurn: false})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(settled), `"agentMidTurn":false`) {
		t.Fatalf("agentMidTurn must serialize even when false (no omitempty); got %s", settled)
	}

	working, err := json.Marshal(paneDTO{AgentMidTurn: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(working), `"agentMidTurn":true`) {
		t.Fatalf("agentMidTurn=true must serialize; got %s", working)
	}
}
