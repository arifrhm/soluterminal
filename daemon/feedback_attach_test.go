package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureFeedbackSubmission spins a fake cloud endpoint, posts the request, and
// returns the forwarded submission. Asserts a 200 round-trip.
func captureFeedbackSubmission(t *testing.T, req feedbackReq) feedbackSubmission {
	t.Helper()
	var got feedbackSubmission
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true,"number":1}`)
	}))
	defer srv.Close()

	a := &apiServer{}
	a.cloudBase = srv.URL
	a.cloudToken = "tok"
	w := postDaemonFeedback(t, a, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	return got
}

func b64(n int) string {
	return base64.StdEncoding.EncodeToString(make([]byte, n))
}

func TestDaemonFeedbackAttachmentOversizeSkipped(t *testing.T) {
	got := captureFeedbackSubmission(t, feedbackReq{
		Type: "bug", Title: "t", Body: "b", IncludeSnapshot: boolPtr(false),
		Attachments: []feedbackAttachmentReq{
			{Name: "huge.png", Mime: "image/png", Data: b64(feedbackMaxAttachmentBytes + 1)},
		},
	})
	if len(got.Attachments) != 0 {
		t.Fatalf("oversize attachment not skipped: %+v", got.Attachments)
	}
}

func TestDaemonFeedbackAttachmentUnsupportedMimeSkipped(t *testing.T) {
	got := captureFeedbackSubmission(t, feedbackReq{
		Type: "bug", Title: "t", Body: "b", IncludeSnapshot: boolPtr(false),
		Attachments: []feedbackAttachmentReq{
			{Name: "evil.svg", Mime: "image/svg+xml", Data: b64(16)},
			{Name: "doc.pdf", Mime: "application/pdf", Data: b64(16)},
		},
	})
	if len(got.Attachments) != 0 {
		t.Fatalf("unsupported MIME not skipped: %+v", got.Attachments)
	}
}

func TestDaemonFeedbackAttachmentsTruncatedToMax(t *testing.T) {
	var reqs []feedbackAttachmentReq
	for i := 0; i < 5; i++ {
		reqs = append(reqs, feedbackAttachmentReq{Name: "a.png", Mime: "image/png", Data: b64(8)})
	}
	got := captureFeedbackSubmission(t, feedbackReq{
		Type: "bug", Title: "t", Body: "b", IncludeSnapshot: boolPtr(false),
		Attachments: reqs,
	})
	if len(got.Attachments) != feedbackMaxAttachments {
		t.Fatalf("got %d attachments, want %d", len(got.Attachments), feedbackMaxAttachments)
	}
}

func TestDaemonFeedbackAttachmentValidForwarded(t *testing.T) {
	const n = 1234
	got := captureFeedbackSubmission(t, feedbackReq{
		Type: "bug", Title: "t", Body: "b", IncludeSnapshot: boolPtr(false),
		Attachments: []feedbackAttachmentReq{
			{Name: "/some/dir/shot.png", Mime: "image/png", Data: b64(n)},
		},
	})
	if len(got.Attachments) != 1 {
		t.Fatalf("valid attachment not forwarded: %+v", got.Attachments)
	}
	att := got.Attachments[0]
	if att.Bytes != n {
		t.Fatalf("Bytes = %d, want decoded length %d", att.Bytes, n)
	}
	if att.Name != "shot.png" {
		t.Fatalf("Name not base-cleaned: %q", att.Name)
	}
	if att.Mime != "image/png" {
		t.Fatalf("Mime = %q", att.Mime)
	}
	if att.Data != b64(n) {
		t.Fatalf("Data not forwarded verbatim")
	}
}

func TestDaemonFeedbackAttachmentDataURLPrefixStripped(t *testing.T) {
	const n = 64
	raw := b64(n)
	got := captureFeedbackSubmission(t, feedbackReq{
		Type: "bug", Title: "t", Body: "b", IncludeSnapshot: boolPtr(false),
		Attachments: []feedbackAttachmentReq{
			{Name: "shot.png", Mime: "image/png", Data: "data:image/png;base64," + raw},
		},
	})
	if len(got.Attachments) != 1 {
		t.Fatalf("prefixed attachment not accepted: %+v", got.Attachments)
	}
	att := got.Attachments[0]
	if att.Bytes != n {
		t.Fatalf("Bytes = %d, want %d", att.Bytes, n)
	}
	if strings.HasPrefix(att.Data, "data:") || att.Data != raw {
		t.Fatalf("data-URL prefix not stripped: %q", att.Data)
	}
}
