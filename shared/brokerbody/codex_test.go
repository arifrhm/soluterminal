package brokerbody

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCodexCompletedImageHistoryKeepsFreshImages(t *testing.T) {
	image := `{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AAA"}]}`
	final := `{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"saved"}]}`
	comment := strings.Replace(final, "final_answer", "commentary", 1)
	hi := `{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`
	for _, tc := range []struct {
		name, input string
		want        bool
	}{
		{"completed images", image + "," + final + "," + hi, true},
		{"fresh images", image + "," + final + "," + hi + "," + image, false},
		{"still inspecting", hi + "," + image + "," + comment, false},
		{"new image input", hi + "," + image, false},
		{"text only", hi + "," + final + "," + hi, false},
		{"later final", image + "," + final + "," + image + "," + final + "," + hi, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"stream":true,"input":[` + tc.input + `]}`)
			if got := codexCompletedImageHistory(body); got != tc.want {
				t.Fatalf("got=%v want=%v", got, tc.want)
			}
			if codexCompletedImageHistory([]byte(strings.Replace(string(body), `"stream":true`, `"stream":false`, 1))) {
				t.Fatal("nonstreaming request compacted")
			}
		})
	}
}

func TestCodexStreamingPartialRequestAndExplicitOptOut(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("X-Codex-Turn-Metadata", `{"request_kind":"compaction"}`)
	if !CodexStreaming(r, []byte(`{"input":[`)) {
		t.Fatal("partial native compaction not recognized")
	}
	if CodexStreaming(r, []byte(`{"stream":false}`)) {
		t.Fatal("explicit nonstreaming request overridden")
	}
	r.Header.Set("X-Codex-Turn-Metadata", `{"request_kind":"unknown"}`)
	if CodexStreaming(r, nil) {
		t.Fatal("unknown client treated as streaming")
	}
	r.Header.Set("X-Codex-Turn-Metadata", strings.Repeat("a", 8193))
	if CodexRequestKind(r) != "" {
		t.Fatal("unbounded dispatch metadata accepted")
	}
	r.Header.Set("X-Solu-Codex-Stream", "true")
	if !CodexStreaming(r, nil) {
		t.Fatal("paired daemon stream marker lost")
	}
}

func TestCodexContextBudgetIsAFailedResponse(t *testing.T) {
	w := httptest.NewRecorder()
	WriteCodexContextLimit(w, CodexHistoryLimit)
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/event-stream" || w.Header().Get("X-Solu-Team-Broker-Error") != "context_budget" {
		t.Fatalf("response: %d %v", w.Code, w.Header())
	}
	var event struct {
		Type     string `json:"type"`
		Response struct {
			Status string `json:"status"`
			Error  struct {
				Code     string `json:"code"`
				MaxBytes int64  `json:"max_bytes"`
			} `json:"error"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(w.Body.String(), "data: "))), &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "response.failed" || event.Response.Status != "failed" || event.Response.Error.Code != "context_length_exceeded" || event.Response.Error.MaxBytes != CodexHistoryLimit {
		t.Fatalf("unexpected failure event: %s", w.Body.String())
	}
}

func TestReadJSONRejectsDeclaredOversizeBeforeReading(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.ContentLength = CodexResponsesLimit + 1
	r.Body = nil // A read would panic; reject on the known size without buffering.
	_, err := ReadJSON(httptest.NewRecorder(), r, CodexResponsesLimit)
	if ErrorType(err) != "request_too_large" {
		t.Fatalf("error=%v", err)
	}
}
