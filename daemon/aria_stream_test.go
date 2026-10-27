package main

import (
	"strings"
	"testing"
)

func TestAriaConsumeStreamRequiresTerminalEvent(t *testing.T) {
	t.Parallel()
	a := &Aria{}
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":10}}}`,
		``,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
		``,
	}, "\n")

	if _, _, _, err := a.consumeStream(strings.NewReader(stream), func(map[string]any) {}); err == nil {
		t.Fatal("consumeStream succeeded without message_stop, want error")
	}
}

func TestAriaConsumeStreamAcceptsMessageStop(t *testing.T) {
	t.Parallel()
	a := &Aria{}
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":10}}}`,
		``,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		``,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		``,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	blocks, stopReason, usage, err := a.consumeStream(strings.NewReader(stream), func(map[string]any) {})
	if err != nil {
		t.Fatalf("consumeStream() error = %v", err)
	}
	if stopReason != "end_turn" {
		t.Fatalf("stopReason = %q, want end_turn", stopReason)
	}
	if usage.InputTokens != 10 || usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v, want input=10 output=2", usage)
	}
	if len(blocks) != 1 || blocks[0].Type != "text" || blocks[0].Text != "ok" {
		t.Fatalf("blocks = %+v, want one text block", blocks)
	}
}
