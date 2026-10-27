package main

import (
	"fmt"
	"strconv"
)

// The MCP host's tool-result budget is roughly 25k tokens. A 156 KiB board
// exceeded it, and the resulting scratch-file fallback is unreadable by
// agents, so list pages stay comfortably below that ceiling (32 KiB is
// approximately 8-11k tokens).
const mcpListByteBudget = 32 * 1024

type mcpPageInfo struct {
	Returned   int    `json:"returned"`
	Offset     int    `json:"offset"`
	Total      int    `json:"total"`
	Remaining  int    `json:"remaining"`
	Truncated  bool   `json:"truncated"`
	NextCursor string `json:"nextCursor,omitempty"`
	Notice     string `json:"notice,omitempty"`
}

func mcpPageArgs(args map[string]any, def, max int) (limit, offset int, err error) {
	limit = def
	if value, ok := args["limit"].(float64); ok && value > 0 {
		if requested := int(value); requested > 0 {
			limit = requested
		}
	}
	if limit > max {
		limit = max
	}
	cursor := mcpArgString(args, "cursor")
	if cursor == "" {
		return limit, 0, nil
	}
	offset, err = strconv.Atoi(cursor)
	if err != nil || offset < 0 {
		return 0, 0, fmt.Errorf("invalid cursor %q: pass the nextCursor value from the previous call", cursor)
	}
	return limit, offset, nil
}

func mcpPageBounds(total, offset, limit int, itemSize func(i int) int, overhead int) (end int) {
	if offset >= total {
		return offset
	}
	end = offset
	size := overhead
	for end < total && end-offset < limit {
		nextSize := itemSize(end)
		if end > offset && size+nextSize > mcpListByteBudget {
			break
		}
		size += nextSize
		end++
	}
	return end
}

func mcpPageInfoFor(total, offset, end int) mcpPageInfo {
	if offset >= total {
		return mcpPageInfo{
			Offset: offset,
			Total:  total,
			Notice: "cursor is past the end of the list",
		}
	}
	page := mcpPageInfo{
		Returned:  end - offset,
		Offset:    offset,
		Total:     total,
		Remaining: total - end,
	}
	page.Truncated = page.Remaining > 0
	if page.Truncated {
		page.NextCursor = strconv.Itoa(end)
		page.Notice = fmt.Sprintf(
			"result truncated: showing items %d-%d of %d (%d more); call again with cursor=%q and the same filter arguments to continue",
			offset+1, end, total, page.Remaining, page.NextCursor,
		)
	}
	return page
}

func mcpPageArgBool(args map[string]any, keys ...string) (value bool, present bool) {
	for _, key := range keys {
		if value, ok := args[key].(bool); ok {
			return value, true
		}
	}
	return false, false
}
