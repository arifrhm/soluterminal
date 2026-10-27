package main

import (
	"testing"
	"time"
)

// A Claude pane running parallel subagents makes ~15-20 genuine approvals a
// minute. There is no hourly cap for that to exhaust (2026-10-12: a shared
// 500/hr cap parked every other always-mode pane for the rest of the hour).
func TestAutoFireHasNoHourlyCap(t *testing.T) {
	a := &Aria{confirms: map[string]*ariaPendingConfirm{}}
	now := time.Now()
	for i := range 2000 {
		a.autoFireTimes = append(a.autoFireTimes, now.Add(-2*time.Second-time.Duration(i)*time.Second))
	}
	if allowed, reason := a.autoAllowed(); !allowed {
		t.Fatalf("autoAllowed after 2000 fires in the hour = false (%q)", reason)
	}
	if !a.tryReserveAutoFireSlot("next") {
		t.Fatal("reserve refused after 2000 fires in the hour")
	}
	a.autoRecordFire("next")
	if got := a.autoStatus()["auto_fires_hr"]; got != 2001 {
		t.Fatalf("auto_fires_hr = %v, want 2001", got)
	}
}

// The per-second burst cap is still the tight-loop brake.
func TestAutoFireBurstCapStillApplies(t *testing.T) {
	a := &Aria{confirms: map[string]*ariaPendingConfirm{}}
	for range ariaAutoMaxPerSecond {
		a.autoFireTimes = append(a.autoFireTimes, time.Now())
	}
	if allowed, reason := a.autoAllowed(); allowed || reason != "burst-cap" {
		t.Fatalf("autoAllowed = %v (%q), want burst-cap", allowed, reason)
	}
	if a.tryReserveAutoFireSlot("burst") {
		t.Fatal("reserve allowed past the burst cap")
	}
}
