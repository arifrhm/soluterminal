package typesafe

import (
	"encoding/json"
	"testing"
)

func TestResponseInputUsagePresence(t *testing.T) {
	for _, tc := range []struct {
		body   string
		known  bool
		tokens int64
	}{
		{`{"model":"jev-1.13.0"}`, false, 0},
		{`{"usage":null}`, false, 0},
		{`{"usage":{}}`, false, 0},
		{`{"usage":{"input_tokens":null}}`, false, 0},
		{`{"usage":{"input_tokens":0}}`, true, 0},
		{`{"usage":{"input_tokens":137}}`, true, 137},
		{`{"usage":{"input_tokens":-1}}`, false, -1},
	} {
		t.Run(tc.body, func(t *testing.T) {
			var response Response
			if err := json.Unmarshal([]byte(tc.body), &response); err != nil {
				t.Fatal(err)
			}
			if response.InputUsageKnown() != tc.known || response.Usage.InputTokens != tc.tokens {
				t.Fatalf("known=%t tokens=%d", response.InputUsageKnown(), response.Usage.InputTokens)
			}
		})
	}
	constructed := &Response{Usage: Usage{InputTokens: 137}}
	if !constructed.InputUsageKnown() {
		t.Fatal("constructed usage lost")
	}
	if (*Response)(nil).InputUsageKnown() {
		t.Fatal("nil usage known")
	}
}
