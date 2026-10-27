package brokerbody

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func encodeGzip(t *testing.T, body []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestReadJSONLimitsPlainAndDecodedGzip(t *testing.T) {
	const limit int64 = 512
	for _, encoding := range []string{"", "identity", "gzip"} {
		for _, size := range []int{int(limit) - 1, int(limit), int(limit) + 1} {
			t.Run(fmt.Sprintf("%s/%d", encoding, size), func(t *testing.T) {
				body := []byte(`{"input":"` + strings.Repeat("a", size-len(`{"input":""}`)) + `"}`)
				payload := body
				if encoding == "gzip" {
					payload = encodeGzip(t, body)
				}
				r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
				r.Header.Set("Content-Encoding", encoding)
				got, err := ReadJSON(httptest.NewRecorder(), r, limit)
				if size <= int(limit) {
					if err != nil || !bytes.Equal(got, body) {
						t.Fatalf("size=%d err=%v", size, err)
					}
				} else {
					var oversized *http.MaxBytesError
					if !errors.As(err, &oversized) {
						t.Fatalf("size error = %v", err)
					}
					w := httptest.NewRecorder()
					WriteError(w, err, limit)
					if w.Code != 413 || !strings.Contains(w.Body.String(), `"request_too_large"`) || strings.Contains(w.Body.String(), "valid JSON") {
						t.Fatalf("size error: %d %s", w.Code, w.Body.String())
					}
				}
			})
		}
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("synthetic-private-content") }

func TestReadJSONRejectionTypesAndSafeMessages(t *testing.T) {
	for _, tc := range []struct {
		name, encoding, body, kind string
		status                     int
	}{
		{"empty", "", "", "invalid_request_error", 400},
		{"malformed", "", `{"secret":`, "invalid_request_error", 400},
		{"array", "", `[]`, "invalid_request_error", 400},
		{"null", "", `null`, "invalid_request_error", 400},
		{"unsupported", "zstd", `{}`, "unsupported_content_encoding", 415},
		{"bad-gzip", "gzip", `{"synthetic-private-content":true}`, "request_read_error", 400},
		{"read-failure", "", "", "request_read_error", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			if tc.name == "read-failure" {
				r.Body = io.NopCloser(failingReader{})
			}
			r.Header.Set("Content-Encoding", tc.encoding)
			_, err := ReadJSON(httptest.NewRecorder(), r, 512)
			if err == nil {
				t.Fatal("invalid request accepted")
			}
			w := httptest.NewRecorder()
			WriteError(w, err, 512)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.kind) || strings.Contains(w.Body.String(), "synthetic-private-content") || !json.Valid(w.Body.Bytes()) {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestRequestBudgetsAreScopedToCodexResponses(t *testing.T) {
	if Limit("codex", "responses") != 128<<20 || WireLimit(CodexResponsesLimit) <= CodexResponsesLimit {
		t.Fatal("missing media allowance")
	}
	for _, pair := range [][2]string{{"codex", "messages"}, {"claude", "messages"}, {"muse", "responses"}, {"deepseek", "chat-completions"}} {
		if Limit(pair[0], pair[1]) != 32<<20 {
			t.Fatalf("unexpected allowance for %v", pair)
		}
	}
}

func TestReadJSONBoundsGzipWireBeforeDecompression(t *testing.T) {
	// Concatenated empty gzip members consume wire bytes without approaching
	// the decoded budget. Neither budget may be bypassed by compression.
	member := encodeGzip(t, nil)
	payload := bytes.Repeat(member, int(WireLimit(512))/len(member)+1)
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	r.Header.Set("Content-Encoding", "gzip")
	_, err := ReadJSON(httptest.NewRecorder(), r, 512)
	var oversized *http.MaxBytesError
	if !errors.As(err, &oversized) {
		t.Fatalf("wire budget error=%v", err)
	}
}
