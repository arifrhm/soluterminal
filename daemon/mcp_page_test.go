package main

import (
	"strings"
	"testing"
)

func TestMCPPageArgs(t *testing.T) {
	limit, offset, err := mcpPageArgs(nil, 50, 200)
	if err != nil || limit != 50 || offset != 0 {
		t.Fatalf("nil args = (%d, %d, %v), want (50, 0, nil)", limit, offset, err)
	}
	limit, _, _ = mcpPageArgs(map[string]any{"limit": float64(500)}, 50, 200)
	if limit != 200 {
		t.Fatalf("clamped limit = %d, want 200", limit)
	}
	for _, value := range []float64{0, -1, 0.5} {
		limit, _, _ = mcpPageArgs(map[string]any{"limit": value}, 50, 200)
		if limit != 50 {
			t.Fatalf("limit %v = %d, want default 50", value, limit)
		}
	}
	_, offset, err = mcpPageArgs(map[string]any{"cursor": "12"}, 50, 200)
	if err != nil || offset != 12 {
		t.Fatalf("cursor 12 = (%d, %v), want (12, nil)", offset, err)
	}
	for _, cursor := range []string{"abc", "-1"} {
		_, _, err = mcpPageArgs(map[string]any{"cursor": cursor}, 50, 200)
		if err == nil || !strings.Contains(err.Error(), "nextCursor") {
			t.Fatalf("cursor %q error = %v, want nextCursor guidance", cursor, err)
		}
	}
}

func TestMCPPageBoundsRespectsLimitAndByteBudget(t *testing.T) {
	if end := mcpPageBounds(10, 2, 3, func(int) int { return 10 }, 0); end != 5 {
		t.Fatalf("limit end = %d, want 5", end)
	}
	halfBudget := mcpListByteBudget / 2
	if end := mcpPageBounds(10, 0, 10, func(int) int { return halfBudget - 1 }, 2); end != 2 {
		t.Fatalf("byte-budget end = %d, want 2", end)
	}
	if end := mcpPageBounds(10, 3, 10, func(int) int { return mcpListByteBudget + 1 }, 0); end != 4 {
		t.Fatalf("oversized-item end = %d, want 4", end)
	}
	if end := mcpPageBounds(10, 10, 3, func(int) int { return 1 }, 0); end != 10 {
		t.Fatalf("end cursor = %d, want 10", end)
	}
	if end := mcpPageBounds(10, 12, 3, func(int) int { return 1 }, 0); end != 12 {
		t.Fatalf("past-end cursor = %d, want 12", end)
	}
}

func TestMCPPageInfoFor(t *testing.T) {
	page := mcpPageInfoFor(10, 2, 5)
	if page.Returned != 3 || page.Remaining != 5 || !page.Truncated || page.NextCursor != "5" || !strings.Contains(page.Notice, `cursor="5"`) {
		t.Fatalf("truncated page = %+v", page)
	}
	page = mcpPageInfoFor(10, 8, 10)
	if page.Truncated || page.NextCursor != "" || page.Returned != 2 || page.Remaining != 0 {
		t.Fatalf("last page = %+v", page)
	}
	page = mcpPageInfoFor(10, 12, 12)
	if page.Returned != 0 || page.Truncated || page.Remaining != 0 {
		t.Fatalf("past-end page = %+v", page)
	}
}
