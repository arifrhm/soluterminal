package main

import (
	"runtime/debug"

	"testing"
)

func TestApplyMemGuardHeapLimit(t *testing.T) {
	previous := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(previous) })

	t.Setenv("GOMEMLIMIT", "")
	limit, ok := applyMemGuardHeapLimit(6144)
	if want := int64(6144/100*memGuardHeapLimitPercent) << 20; !ok || limit != want {
		t.Fatalf("applyMemGuardHeapLimit(6144) = %d, %v; want %d, true", limit, ok, want)
	}
	if got := debug.SetMemoryLimit(-1); got != limit {
		t.Fatalf("runtime memory limit = %d, want %d", got, limit)
	}
	if limit >= int64(6144)<<20 {
		t.Fatalf("limit %d must sit below the guard threshold", limit)
	}
	debug.SetMemoryLimit(previous)

	if _, ok := applyMemGuardHeapLimit(0); ok {
		t.Fatal("a disabled guard (SOLU_MEM_GUARD_MB=0) must not set a limit")
	}
	t.Setenv("GOMEMLIMIT", "3GiB")
	if _, ok := applyMemGuardHeapLimit(6144); ok {
		t.Fatal("an explicit GOMEMLIMIT is the operator's choice and must be left alone")
	}
	if got := debug.SetMemoryLimit(-1); got != previous {
		t.Fatalf("GOMEMLIMIT path changed the runtime limit to %d", got)
	}
}
