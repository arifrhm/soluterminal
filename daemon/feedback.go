package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

// In-app feedback (bug / idea / question) capture. The web client sends only
// intent (type/title/body + which pane); the daemon gathers the trustworthy
// context — versions, platform, the active pane's recent output, the log-ring
// tail — REDACTS every byte locally, and forwards a normalized submission to
// the cloud, which holds the GitHub token and files the issue.
//
// The daemon is the redaction choke point: it is the only node that ever sees
// raw pane bytes or raw logs, so nothing unredacted can leave the machine. The
// outbound shape mirrors cloud.FeedbackSubmission by JSON tag (the daemon and
// cloud are separate modules — same arrangement as crashReportPayload).

const (
	feedbackMaxTitle         = 200
	feedbackMaxBody          = 8 << 10
	feedbackMaxSnapshotLines = 40
	feedbackMaxSnapshotBytes = 16 << 10

	feedbackMaxAttachments           = 3
	feedbackMaxAttachmentBytes       = 5 << 20
	feedbackMaxAttachmentNameLen     = 200
	feedbackMaxAttachmentsTotalBytes = 15 << 20

	feedbackCaptureNone     = "none"
	feedbackCapturePaneText = "pane_text"
	feedbackCaptureLogsOnly = "logs_only"
)

type feedbackReq struct {
	Type            string                  `json:"type"` // "bug" | "idea" | "question"
	Title           string                  `json:"title"`
	Body            string                  `json:"body"`
	PaneID          string                  `json:"paneId"`          // optional; active pane to snapshot
	IncludeSnapshot *bool                   `json:"includeSnapshot"` // default false: pane text and logs only when asked
	ClientVersion   string                  `json:"clientVersion"`   // low-trust; label/meta only
	Attachments     []feedbackAttachmentReq `json:"attachments,omitempty"`
}

// feedbackAttachmentReq is an inbound image from the web client. Data is
// standard base64 of the raw bytes, with no "data:...;base64," prefix (the
// daemon strips one defensively anyway). Untrusted: name/mime/size are all
// re-validated and the bytes are re-measured server-side.
type feedbackAttachmentReq struct {
	Name string `json:"name"`
	Mime string `json:"mime"`
	Data string `json:"data"`
}

// feedbackSubmission mirrors cloud.FeedbackSubmission (same JSON shape).
type feedbackSubmission struct {
	Schema   int                   `json:"schema"`
	Channel  string                `json:"channel"`
	Type     string                `json:"type"`
	Title    string                `json:"title"`
	Body     string                `json:"body"`
	Snapshot string                `json:"snapshot"`
	Logs     string                `json:"logs"`
	Context  feedbackSubmitContext `json:"context"`

	Attachments []feedbackAttachment `json:"attachments,omitempty"`
}

// feedbackAttachment mirrors cloud.FeedbackAttachment (same JSON tags). Bytes
// is the daemon-MEASURED decoded length (len of the decoded raw bytes), not a
// client-supplied value. Data is the prefix-stripped base64, forwarded
// verbatim — images are NEVER passed through redactSensitive.
type feedbackAttachment struct {
	Name  string `json:"name"`
	Mime  string `json:"mime"`
	Bytes int    `json:"bytes"`
	Data  string `json:"data"`
}

type feedbackSubmitContext struct {
	AppVersion        string `json:"appVersion"`
	DaemonVersion     string `json:"daemonVersion"`
	ClientVersion     string `json:"clientVersion"`
	ShellVersion      string `json:"shellVersion"`
	ShellPlatform     string `json:"shellPlatform"`
	BackendID         string `json:"backendID"`
	Platform          string `json:"platform"`
	OS                string `json:"os"`
	Arch              string `json:"arch"`
	MachineID         string `json:"machineId"`
	MachineName       string `json:"machineName"`
	PaneID            string `json:"paneId"`
	CaptureKind       string `json:"captureKind"`
	SnapshotRequested *bool  `json:"snapshotRequested,omitempty"`
	SnapshotCaptured  *bool  `json:"snapshotCaptured,omitempty"`
	LogsRequested     *bool  `json:"logsRequested,omitempty"`
	LogsCaptured      *bool  `json:"logsCaptured,omitempty"`
	SubmittedAt       string `json:"submittedAt"`
}

var feedbackAllowedTypes = map[string]bool{"bug": true, "idea": true, "question": true}

var feedbackHTTPClient = &http.Client{Timeout: 20 * time.Second}

func (a *apiServer) handleFeedback(w http.ResponseWriter, r *http.Request) {
	var req feedbackReq
	if !decodeJSON(w, r, &req, defaultJSONBodyBytes) {
		return
	}
	req.Type = strings.TrimSpace(strings.ToLower(req.Type))
	req.Title = strings.TrimSpace(req.Title)
	req.Body = strings.TrimSpace(req.Body)
	if !feedbackAllowedTypes[req.Type] {
		http.Error(w, "type must be bug, idea, or question", http.StatusBadRequest)
		return
	}
	if req.Title == "" {
		http.Error(w, "title required", http.StatusBadRequest)
		return
	}
	if req.Body == "" {
		http.Error(w, "description required", http.StatusBadRequest)
		return
	}

	// Attachments are LENIENT: a bad image (unsupported MIME, undecodable,
	// oversize, over-count) is skipped, never a 4xx — the text report always
	// forwards. The web client-side validation is the hard UX gate. Images are
	// never run through redactSensitive.
	var attachments []feedbackAttachment
	var attachmentsTotal int
	for _, att := range req.Attachments {
		if len(attachments) >= feedbackMaxAttachments {
			break
		}
		ext := pastedImageExt(att.Mime)
		if ext == "" {
			continue // unsupported MIME
		}
		stripped := feedbackStripDataURL(att.Data)
		raw, err := base64.StdEncoding.DecodeString(stripped)
		if err != nil {
			continue // undecodable base64
		}
		if len(raw) == 0 || len(raw) > feedbackMaxAttachmentBytes {
			continue
		}
		if attachmentsTotal+len(raw) > feedbackMaxAttachmentsTotalBytes {
			break // aggregate guard reached
		}
		attachmentsTotal += len(raw)
		attachments = append(attachments, feedbackAttachment{
			Name:  feedbackClamp(filepath.Base(att.Name), feedbackMaxAttachmentNameLen),
			Mime:  att.Mime,
			Bytes: len(raw),
			Data:  stripped,
		})
	}

	// Pane text and logs leave the computer only when the request asks for
	// them. The composer's box starts unticked; a client that omits the field
	// (an older or third-party one) must not send them silently.
	include := req.IncludeSnapshot != nil && *req.IncludeSnapshot
	snapshot := ""
	logs := ""
	snapshotRequested := include && req.PaneID != ""
	logsRequested := include
	if include {
		if req.PaneID != "" {
			snapshot = a.feedbackPaneSnapshot(req.PaneID)
		}
		logs = recentLogContext() // already redacted at capture (logring.go)
	}
	captureKind := feedbackCaptureNone
	if snapshotRequested {
		captureKind = feedbackCapturePaneText
	} else if logsRequested {
		captureKind = feedbackCaptureLogsOnly
	}
	snapshotCaptured := snapshot != ""
	logsCaptured := logs != ""
	machineID, machineName := a.feedbackMachineIdentity()

	sub := feedbackSubmission{
		Schema:   1,
		Channel:  "inapp",
		Type:     req.Type,
		Title:    redactSensitive(feedbackClamp(req.Title, feedbackMaxTitle)),
		Body:     redactSensitive(feedbackClamp(req.Body, feedbackMaxBody)),
		Snapshot: snapshot,
		Logs:     logs,
		Context: feedbackSubmitContext{
			AppVersion:        a.ver,
			DaemonVersion:     a.ver,
			ClientVersion:     strings.TrimSpace(req.ClientVersion),
			ShellVersion:      a.liveShellVersion(),
			ShellPlatform:     a.shellPlatform,
			BackendID:         a.backendID,
			Platform:          a.shellPlatform,
			OS:                runtime.GOOS,
			Arch:              runtime.GOARCH,
			MachineID:         machineID,
			MachineName:       machineName,
			PaneID:            req.PaneID,
			CaptureKind:       captureKind,
			SnapshotRequested: feedbackBoolPtr(snapshotRequested),
			SnapshotCaptured:  feedbackBoolPtr(snapshotCaptured),
			LogsRequested:     feedbackBoolPtr(logsRequested),
			LogsCaptured:      feedbackBoolPtr(logsCaptured),
			SubmittedAt:       time.Now().UTC().Format(time.RFC3339),
		},
		Attachments: attachments,
	}

	base, token := a.cloudCreds()
	if base == "" || token == "" {
		http.Error(w, "feedback needs a cloud connection — pair this machine first", http.StatusServiceUnavailable)
		return
	}

	payload, err := json.Marshal(sub)
	if err != nil {
		http.Error(w, "encode failed", http.StatusInternalServerError)
		return
	}
	httpReq, err := http.NewRequestWithContext(r.Context(), "POST", base+"/api/feedback", bytes.NewReader(payload))
	if err != nil {
		http.Error(w, "build request failed", http.StatusInternalServerError)
		return
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := feedbackHTTPClient.Do(httpReq)
	if err != nil {
		http.Error(w, "could not reach the feedback service — please try again", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))

	if resp.StatusCode == http.StatusTooManyRequests {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			w.Header().Set("Retry-After", ra)
		}
		http.Error(w, "daily feedback limit reached — please try again later", http.StatusTooManyRequests)
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(w, "could not file feedback right now — please try again", http.StatusBadGateway)
		return
	}

	var out struct {
		OK                bool   `json:"ok"`
		Number            int    `json:"number"`
		URL               string `json:"url"`
		AttachmentsStored *int   `json:"attachmentsStored"`
		AttachmentsFailed *int   `json:"attachmentsFailed"`
		// Queued: the cloud kept the report to file once GitHub accepts it.
		Queued bool `json:"queued"`
	}
	_ = json.Unmarshal(respBody, &out)
	respJSON := map[string]any{"ok": true, "number": out.Number, "url": out.URL}
	if out.Queued {
		respJSON["queued"] = true
	}
	if out.AttachmentsStored != nil {
		respJSON["attachmentsStored"] = *out.AttachmentsStored
	}
	if out.AttachmentsFailed != nil {
		respJSON["attachmentsFailed"] = *out.AttachmentsFailed
	}
	writeJSON(w, http.StatusOK, respJSON)
}

func (a *apiServer) feedbackMachineIdentity() (machineID, machineName string) {
	if a == nil || strings.TrimSpace(a.dataDir) == "" {
		return "", ""
	}
	c, err := LoadClaim(a.dataDir)
	if err != nil || c == nil {
		return "", ""
	}
	return strings.TrimSpace(c.MachineID), redactSensitive(strings.TrimSpace(c.MachineName))
}

func feedbackBoolPtr(v bool) *bool { return &v }

// feedbackPaneSnapshot returns the redacted, ANSI-stripped tail of a pane's
// ring buffer (last feedbackMaxSnapshotLines lines, capped to
// feedbackMaxSnapshotBytes). Empty if the pane is gone — a missing snapshot
// never fails the report. Mirrors the paneDebugTail accessor chain.
func (a *apiServer) feedbackPaneSnapshot(paneID string) string {
	p, ok := a.mgr.Get(paneID)
	if !ok {
		return ""
	}
	stripped := feedbackTailLines(stripANSI(p.buffer.Snapshot()), feedbackMaxSnapshotLines)
	if len(stripped) > feedbackMaxSnapshotBytes {
		stripped = stripped[len(stripped)-feedbackMaxSnapshotBytes:]
	}
	return redactSensitive(stripped)
}

// feedbackTailLines returns the last n lines of s (trailing blank lines
// dropped first), oldest -> newest.
func feedbackTailLines(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if s == "" || n <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// feedbackStripDataURL defensively removes an optional "data:...;base64,"
// prefix from a base64 string. The web client is supposed to send raw base64,
// but a stray data-URL prefix would otherwise corrupt the decode.
func feedbackStripDataURL(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "data:") {
		if i := strings.Index(s, ";base64,"); i >= 0 {
			return s[i+len(";base64,"):]
		}
		if i := strings.Index(s, ","); i >= 0 {
			return s[i+1:]
		}
	}
	return s
}

// feedbackClamp trims s to at most max bytes without splitting a UTF-8 rune: if
// the byte cut lands mid-rune, the incomplete trailing rune is dropped.
func feedbackClamp(s string, max int) string {
	if len(s) <= max {
		return s
	}
	b := s[:max]
	for len(b) > 0 {
		if r, size := utf8.DecodeLastRuneInString(b); r == utf8.RuneError && size <= 1 {
			b = b[:len(b)-1]
			continue
		}
		break
	}
	return b
}

// --- Reporter loop proxies (cloud feedback_reports.go) ---------------------
//
// The web client never talks to the cloud directly; these mirror the filing
// proxy above: machine Bearer auth, pass-through JSON, calm error text.

// handleFeedbackMine proxies GET /api/feedback/mine — the reporter's filed
// reports with live pipeline status.
func (a *apiServer) handleFeedbackMine(w http.ResponseWriter, r *http.Request) {
	base, token := a.cloudCreds()
	if base == "" || token == "" {
		http.Error(w, "feedback needs a cloud connection — pair this machine first", http.StatusServiceUnavailable)
		return
	}
	httpReq, err := http.NewRequestWithContext(r.Context(), "GET", base+"/api/feedback/mine", nil)
	if err != nil {
		http.Error(w, "build request failed", http.StatusInternalServerError)
		return
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	resp, err := feedbackHTTPClient.Do(httpReq)
	if err != nil {
		http.Error(w, "could not reach the feedback service — please try again", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(w, "could not load your reports right now", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// handleFeedbackReply proxies POST /api/feedback/reply — appends the
// reporter's follow-up to their own report's body (which unparks a
// needs-info report via the feedback repo's edited-retriage).
func (a *apiServer) handleFeedbackReply(w http.ResponseWriter, r *http.Request) {
	base, token := a.cloudCreds()
	if base == "" || token == "" {
		http.Error(w, "feedback needs a cloud connection — pair this machine first", http.StatusServiceUnavailable)
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		http.Error(w, "read failed", http.StatusBadRequest)
		return
	}
	httpReq, err := http.NewRequestWithContext(r.Context(), "POST", base+"/api/feedback/reply", bytes.NewReader(payload))
	if err != nil {
		http.Error(w, "build request failed", http.StatusInternalServerError)
		return
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := feedbackHTTPClient.Do(httpReq)
	if err != nil {
		http.Error(w, "could not reach the feedback service — please try again", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(w, "could not save the reply right now", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// handleFeedbackInbox proxies GET /api/feedback/inbox — the reporter's
// feedback-pipeline events (needs-info / fixed) with read state. Pure-DB on
// the cloud side, so the web client can poll it for the inbox chip without
// hammering GitHub.
func (a *apiServer) handleFeedbackInbox(w http.ResponseWriter, r *http.Request) {
	base, token := a.cloudCreds()
	if base == "" || token == "" {
		// Calm empty set, NOT an error: the inbox chip polls this ambiently,
		// and an unpaired machine simply has no feedback loop to show.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"events":[]}`))
		return
	}
	httpReq, err := http.NewRequestWithContext(r.Context(), "GET", base+"/api/feedback/inbox", nil)
	if err != nil {
		http.Error(w, "build request failed", http.StatusInternalServerError)
		return
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	resp, err := feedbackHTTPClient.Do(httpReq)
	if err != nil {
		http.Error(w, "could not reach the feedback service — please try again", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(w, "could not load feedback events right now", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// handleFeedbackInboxRead proxies POST /api/feedback/inbox/read — stamps a
// feedback event read so the unread badge clears on every device.
func (a *apiServer) handleFeedbackInboxRead(w http.ResponseWriter, r *http.Request) {
	base, token := a.cloudCreds()
	if base == "" || token == "" {
		http.Error(w, "feedback needs a cloud connection — pair this machine first", http.StatusServiceUnavailable)
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		http.Error(w, "read failed", http.StatusBadRequest)
		return
	}
	httpReq, err := http.NewRequestWithContext(r.Context(), "POST", base+"/api/feedback/inbox/read", bytes.NewReader(payload))
	if err != nil {
		http.Error(w, "build request failed", http.StatusInternalServerError)
		return
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := feedbackHTTPClient.Do(httpReq)
	if err != nil {
		http.Error(w, "could not reach the feedback service — please try again", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(w, "could not mark it read right now", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// handleFeedbackThread proxies GET /api/feedback/thread?number=N — one
// report's conversation (filed → pipeline events → replies) for the inbox's
// thread view.
func (a *apiServer) handleFeedbackThread(w http.ResponseWriter, r *http.Request) {
	base, token := a.cloudCreds()
	if base == "" || token == "" {
		http.Error(w, "feedback needs a cloud connection — pair this machine first", http.StatusServiceUnavailable)
		return
	}
	httpReq, err := http.NewRequestWithContext(r.Context(), "GET",
		base+"/api/feedback/thread?number="+url.QueryEscape(r.URL.Query().Get("number")), nil)
	if err != nil {
		http.Error(w, "build request failed", http.StatusInternalServerError)
		return
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	resp, err := feedbackHTTPClient.Do(httpReq)
	if err != nil {
		http.Error(w, "could not reach the feedback service — please try again", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(w, "could not load the report thread right now", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// handleFeedbackStatus tells the form up front whether feedback can be sent
// from this computer, so a user without a connected account sees how to reach
// us before typing instead of a 503 after. reason is "signed_out" (no account
// on this install) or "unpaired" (an account, but this computer is not
// connected to the cloud).
func (a *apiServer) handleFeedbackStatus(w http.ResponseWriter, _ *http.Request) {
	if base, token := a.cloudCreds(); base != "" && token != "" {
		writeJSON(w, http.StatusOK, map[string]any{"available": true})
		return
	}
	reason := "signed_out"
	if a.entitlements != nil && a.entitlements.cachedTier() != tierNone {
		reason = "unpaired"
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": false, "reason": reason})
}
