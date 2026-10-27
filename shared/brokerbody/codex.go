package brokerbody

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Leave room for Codex to summarize history before hitting the hard transport
// budget. This applies only to previously completed image-bearing turns.
const CodexHistoryLimit int64 = 64 << 20

func CodexRequestKind(r *http.Request) string {
	kind := r.Header.Get("X-Solu-Codex-Request-Kind")
	if kind == "turn" || kind == "compaction" {
		return kind
	}
	metadata := r.Header.Get("X-Codex-Turn-Metadata")
	if len(metadata) > 8192 {
		return ""
	}
	var value struct {
		RequestKind string `json:"request_kind"`
	}
	if json.Unmarshal([]byte(metadata), &value) == nil && (value.RequestKind == "turn" || value.RequestKind == "compaction") {
		return value.RequestKind
	}
	return ""
}

// Valid request bodies are authoritative. On an over-limit partial read the
// stream flag may be after the retained images, so use Codex's bounded dispatch
// metadata or the paired daemon's fixed streaming marker instead.
func CodexStreaming(r *http.Request, body []byte) bool {
	var value struct {
		Stream *bool `json:"stream"`
	}
	if json.Unmarshal(body, &value) == nil && value.Stream != nil {
		return *value.Stream
	}
	return r.Header.Get("X-Solu-Codex-Stream") == "true" || CodexRequestKind(r) != ""
}

func CodexHistoryNeedsCompaction(r *http.Request, body []byte) bool {
	if int64(len(body)) <= CodexHistoryLimit || CodexRequestKind(r) != "turn" {
		return false
	}
	return codexCompletedImageHistory(body)
}

// Decode only item kinds, roles and phases. Image URLs and large tool outputs
// are skipped by encoding/json without allocating copies of their strings.
// Fresh images after the most recent final answer remain untouched; a
// compaction request always retains the full hard budget.
func codexCompletedImageHistory(body []byte) bool {
	var value struct {
		Stream bool `json:"stream"`
		Input  []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Phase   string `json:"phase"`
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"input"`
	}
	if json.Unmarshal(body, &value) != nil || !value.Stream {
		return false
	}
	images, completedImages := false, false
	for _, item := range value.Input {
		if item.Type != "message" {
			continue
		}
		if item.Role == "assistant" && item.Phase == "final_answer" {
			completedImages = completedImages || images
			images = false
		}
		if item.Role == "user" {
			for _, content := range item.Content {
				if content.Type == "input_image" {
					images = true
				}
			}
		}
	}
	return completedImages && !images
}

// Codex recognizes context_length_exceeded in response.failed SSE events,
// rather than in a generic HTTP 413. Its next turn compacts active history; a
// local compaction that still exceeds the hard budget can trim and retry.
// This is a failed model response, never an invented successful completion.
func WriteCodexContextLimit(w http.ResponseWriter, limit int64) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Solu-Team-Broker-Error", "context_budget")
	w.WriteHeader(http.StatusOK)
	event := map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"id": "solu_broker_context_limit", "status": "failed",
			"error": map[string]any{
				"type": "invalid_request_error", "code": "context_length_exceeded", "max_bytes": limit,
				"message": fmt.Sprintf("SoluTerminal's media context budget is %d MiB. Send continue to let Codex compact its history, or run /compact. Saved files are unaffected.", limit>>20),
			},
		},
	}
	encoded, _ := json.Marshal(event)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
}
