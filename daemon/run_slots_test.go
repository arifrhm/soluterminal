package main

import (
	"strings"
	"testing"
)

func testAgentRunCaps(maxRuns, reserved int) agentRunCaps {
	return agentRunCaps{
		MaxRuns:  resolvedCap{Value: maxRuns, Source: capSourceSettings},
		Reserved: resolvedCap{Value: reserved, Source: capSourceSettings},
	}
}

func TestAdmitRunSlotReservesBookkeeping(t *testing.T) {
	held, reserved := map[string]bool{}, map[string]bool{}
	caps := testAgentRunCaps(2, 1)
	for _, key := range []string{"g1", "g2"} {
		if ok, detail := admitRunSlot(held, reserved, key, false, caps, "agent"); !ok {
			t.Fatalf("%s refused: %s", key, detail)
		}
	}
	if ok, detail := admitRunSlot(held, reserved, "g3", false, caps, "agent"); ok || detail != "busy — 2 agent runs already in flight" {
		t.Fatalf("g3 admission=%v/%q", ok, detail)
	}
	if ok, detail := admitRunSlot(held, reserved, "bk1", true, caps, "agent"); !ok || detail != "" || !reserved["bk1"] {
		t.Fatalf("bk1 admission=%v/%q reserved=%v", ok, detail, reserved)
	}
	if len(held) > 3 {
		t.Fatalf("held=%d exceeds cap+reserve", len(held))
	}
	if ok, detail := admitRunSlot(held, reserved, "bk2", true, caps, "agent"); ok || !strings.HasPrefix(detail, "busy —") || !strings.Contains(detail, "bookkeeping reserve 1/1") {
		t.Fatalf("bk2 admission=%v/%q", ok, detail)
	}
	releaseRunSlot(held, reserved, "g1")
	if ok, detail := admitRunSlot(held, reserved, "g3", false, caps, "agent"); !ok {
		t.Fatalf("g3 after release refused: %s", detail)
	}
	releaseRunSlot(held, reserved, "bk1")
	if len(reserved) != 0 {
		t.Fatalf("reserve leaked: %v", reserved)
	}
}

func TestAdmitRunSlotBookkeepingPrefersReserve(t *testing.T) {
	held, reserved := map[string]bool{}, map[string]bool{}
	caps := testAgentRunCaps(2, 1)
	if ok, detail := admitRunSlot(held, reserved, "bk1", true, caps, "agent"); !ok {
		t.Fatal(detail)
	}
	if !reserved["bk1"] || len(held)-len(reserved) != 0 {
		t.Fatalf("bookkeeping did not prefer reserve: held=%v reserved=%v", held, reserved)
	}
	for _, key := range []string{"g1", "g2"} {
		if ok, detail := admitRunSlot(held, reserved, key, false, caps, "agent"); !ok {
			t.Fatalf("%s refused: %s", key, detail)
		}
	}
}

func TestAdmitRunSlotSingleFlightBeatsReserve(t *testing.T) {
	held := map[string]bool{"bk1": true}
	reserved := map[string]bool{}
	if ok, detail := admitRunSlot(held, reserved, "bk1", true, testAgentRunCaps(2, 1), "agent"); ok || detail != "previous run still going" {
		t.Fatalf("repeat admission=%v/%q", ok, detail)
	}
}

func TestAdmitRunSlotZeroReserveIsOneSharedPool(t *testing.T) {
	held, reserved := map[string]bool{}, map[string]bool{}
	caps := testAgentRunCaps(2, 0)
	for _, key := range []string{"g1", "g2"} {
		if ok, detail := admitRunSlot(held, reserved, key, false, caps, "agent"); !ok {
			t.Fatalf("%s refused: %s", key, detail)
		}
	}
	if ok, detail := admitRunSlot(held, reserved, "bk1", true, caps, "agent"); ok || !strings.HasPrefix(detail, "busy —") {
		t.Fatalf("bookkeeping admission=%v/%q", ok, detail)
	}
}

func TestAgentRunCapsHardCeiling(t *testing.T) {
	t.Setenv(agentRunsEnv, "999")
	t.Setenv(agentRunsReservedEnv, "999")
	for name, caps := range map[string]agentRunCaps{
		"env": defaultAgentRunCaps(),
		"settings": resolveAgentRunCaps(resourceSettings{
			MaxAgentRuns: intPtr(999), ReservedBookkeepingRuns: intPtr(999),
		}),
	} {
		t.Run(name, func(t *testing.T) {
			if caps.MaxRuns.Value != agentRunsCeiling || caps.Reserved.Value != agentRunsReservedCeiling {
				t.Fatalf("caps=%+v", caps)
			}
			held, reserved := map[string]bool{}, map[string]bool{}
			admitted := 0
			for i := range 50 {
				bookkeeping := i%2 == 1
				if ok, _ := admitRunSlot(held, reserved, string(rune('a'+i)), bookkeeping, caps, "agent"); ok {
					admitted++
				}
			}
			if admitted != agentRunsHardCeiling {
				t.Fatalf("admitted=%d want %d", admitted, agentRunsHardCeiling)
			}
		})
	}
}
