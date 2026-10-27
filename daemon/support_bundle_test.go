package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func supportBundleTestServer(t *testing.T) *apiServer {
	t.Helper()
	mgr := NewManager(t.TempDir())
	return &apiServer{ver: "test-version", backendID: "test-backend", addr: "127.0.0.1:8787", startT: time.Now().Add(-time.Minute), mgr: mgr}
}

func withFreshGlobalLogRing(t *testing.T) {
	t.Helper()
	previous := globalLogRing
	globalLogRing = newLogRing(logRingSize)
	t.Cleanup(func() { globalLogRing = previous })
}

func sectionByID(t *testing.T, resp supportBundleResponse, id string) supportBundleSection {
	t.Helper()
	for _, section := range resp.Sections {
		if section.ID == id {
			return section
		}
	}
	t.Fatalf("section %q missing", id)
	return supportBundleSection{}
}

func TestSupportBundleRedactsSyntheticSecrets(t *testing.T) {
	withFreshGlobalLogRing(t)
	t.Setenv("SOLU_DURABLE_LOG_DIR", "off")
	a := supportBundleTestServer(t)
	a.updater = &Updater{status: UpdateStatus{LastError: strings.Join([]string{
		"AKIAIOSFODNN7EXAMPLE",
		"sk-abcdefghijklmnopqrstuvwxyz123456",
		"PASSWORD=hunter2",
		"/home/tester/work/x",
	}, " ")}}

	paneSecret := "pane ghp_abcdefghijklmnopqrstuvwxyz1234567890"
	p := &Pane{ID: "pane_secret", Status: PaneRunning, buffer: NewRingBuffer(1 << 20)}
	_, _ = p.buffer.Write([]byte(paneSecret + "\n"))
	a.mgr.panes[p.ID] = p

	jwt := "Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature123456"
	jsonToken := `"access_token":"fixture-access-token-value"`
	pem := "-----BEGIN PRIVATE KEY-----\nfixture-private-material\n-----END PRIVATE KEY-----"
	opaque := strings.Repeat("a", 64)
	globalLogRing.add(strings.Join([]string{jwt, jsonToken, pem, opaque}, "\n"))

	resp := a.buildSupportBundle(p.ID)
	serialized, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	all := string(serialized)
	for _, secret := range []string{
		"AKIAIOSFODNN7EXAMPLE", "sk-abcdefghijklmnopqrstuvwxyz123456",
		"ghp_abcdefghijklmnopqrstuvwxyz1234567890", jwt,
		"hunter2", "fixture-access-token-value", "fixture-private-material", opaque,
		"/home/tester/work/x",
	} {
		if strings.Contains(all, secret) {
			t.Errorf("synthetic secret leaked: %q", secret)
		}
	}
	if !strings.Contains(all, "[REDACTED]") {
		t.Error("redaction marker missing")
	}
	if !strings.Contains(all, "~/work/x") {
		t.Error("redacted home-relative path missing")
	}
}

func TestSupportBundleEverySectionIsRedacted(t *testing.T) {
	withFreshGlobalLogRing(t)
	t.Setenv("SOLU_DURABLE_LOG_DIR", "off")
	a := supportBundleTestServer(t)
	globalLogRing.add("PASSWORD=already-caught /home/tester/project")
	resp := a.buildSupportBundle("")
	for _, section := range resp.Sections {
		if got := redactSensitive(section.Text); got != section.Text {
			t.Errorf("section %q bypassed final redaction: %q", section.ID, section.Text)
		}
	}
}

func TestSupportBundleIsBounded(t *testing.T) {
	withFreshGlobalLogRing(t)
	dir := t.TempDir()
	t.Setenv("SOLU_DURABLE_LOG_DIR", dir)
	a := supportBundleTestServer(t)
	p := &Pane{ID: "pane_big", Status: PaneRunning, buffer: NewRingBuffer(6 << 20)}
	_, _ = p.buffer.Write([]byte(strings.Repeat("pane output stays bounded\n", 230000)))
	a.mgr.panes[p.ID] = p

	for i := 0; i < 5500; i++ {
		globalLogRing.add(fmt.Sprintf("ring-%05d %s", i, strings.Repeat("r", 1800)))
	}
	first := "FIRST-LINE-MUST-NOT-APPEAR\n"
	last := "FINAL-LINE-MUST-APPEAR\n"
	durable := first + strings.Repeat("durable middle line\n", 1_100_000) + last
	if err := os.WriteFile(filepath.Join(dir, durableLogName), []byte(durable), 0o600); err != nil {
		t.Fatal(err)
	}

	resp := a.buildSupportBundle(p.ID)
	total := 0
	for _, section := range resp.Sections {
		cap := supportBundleSectionMaxBytes
		if section.ID == "logs" {
			cap = supportBundleLogBytes
		}
		if section.Bytes > cap || len(section.Text) > cap {
			t.Errorf("section %q exceeds cap: bytes=%d cap=%d", section.ID, section.Bytes, cap)
		}
		total += section.Bytes
	}
	if total > supportBundleMaxTotalBytes {
		t.Fatalf("total = %d, cap = %d", total, supportBundleMaxTotalBytes)
	}
	daemonLog := sectionByID(t, resp, "daemonLog")
	if !strings.Contains(daemonLog.Text, "FINAL-LINE-MUST-APPEAR") {
		t.Error("durable log does not contain final line")
	}
	if strings.Contains(daemonLog.Text, "FIRST-LINE-MUST-NOT-APPEAR") {
		t.Error("durable log included the head instead of the tail")
	}
}

func TestSupportBundleLocalOnly(t *testing.T) {
	t.Setenv("SOLU_DURABLE_LOG_DIR", "off")
	a := &apiServer{}
	remote := httptest.NewRequest(http.MethodGet, "/api/support/bundle", nil)
	remote.Header.Set("X-Forwarded-Prefix", "/m/abc123")
	remoteRecorder := httptest.NewRecorder()
	a.handleSupportBundle(remoteRecorder, remote)
	if remoteRecorder.Code != http.StatusForbidden {
		t.Fatalf("remote status = %d, want 403", remoteRecorder.Code)
	}
	if strings.Contains(remoteRecorder.Body.String(), "sections") {
		t.Fatalf("remote response exposed section data: %s", remoteRecorder.Body.String())
	}

	local := httptest.NewRequest(http.MethodGet, "/api/support/bundle", nil)
	localRecorder := httptest.NewRecorder()
	a.handleSupportBundle(localRecorder, local)
	if localRecorder.Code != http.StatusOK {
		t.Fatalf("local status = %d, want 200", localRecorder.Code)
	}
}

func TestSupportBundleTakesNoCallerPath(t *testing.T) {
	withFreshGlobalLogRing(t)
	dir := t.TempDir()
	t.Setenv("SOLU_DURABLE_LOG_DIR", dir)
	canonical := filepath.Join(dir, durableLogName)
	if err := os.WriteFile(canonical, []byte("canonical log only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	decoy := "DECOY-CONTENT-MUST-NOT-BE-READ"
	if err := os.WriteFile(filepath.Join(dir, "decoy.log"), []byte(decoy), 0o600); err != nil {
		t.Fatal(err)
	}
	a := supportBundleTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/api/support/bundle?paneId=..%2F..%2Fetc%2Fpasswd&path=%2Fetc%2Fpasswd&file=decoy.log", nil)
	w := httptest.NewRecorder()
	a.handleSupportBundle(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp supportBundleResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.LogPaths) != 1 || resp.LogPaths[0] != redactSensitive(canonical) {
		t.Fatalf("logPaths = %#v, want only %q", resp.LogPaths, canonical)
	}
	if strings.Contains(w.Body.String(), decoy) {
		t.Fatal("caller-selected decoy file was read")
	}
	paneText := sectionByID(t, resp, "paneText")
	if !paneText.Empty || paneText.Note == "" {
		t.Fatalf("traversal pane id should be an opaque missing id: %+v", paneText)
	}
}

func TestSupportBundleDefaultsExcludePaneText(t *testing.T) {
	withFreshGlobalLogRing(t)
	t.Setenv("SOLU_DURABLE_LOG_DIR", "off")
	a := supportBundleTestServer(t)
	p := &Pane{ID: "pane_1", Status: PaneRunning, buffer: NewRingBuffer(1 << 20)}
	_, _ = p.buffer.Write([]byte("visible pane text\n"))
	a.mgr.panes[p.ID] = p
	resp := a.buildSupportBundle(p.ID)
	if sectionByID(t, resp, "paneText").SelectedByDefault {
		t.Error("pane text must be opt-in")
	}
	for _, id := range []string{"versions", "logs"} {
		if !sectionByID(t, resp, id).SelectedByDefault {
			t.Errorf("%s should be selected by default", id)
		}
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var wire any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{"attachment": true, "attachments": true, "image": true, "images": true, "data": true, "dataUrl": true, "upload": true, "uploadUrl": true}
	var checkKeys func(any)
	checkKeys = func(value any) {
		switch x := value.(type) {
		case map[string]any:
			for key, child := range x {
				if forbidden[key] {
					t.Errorf("forbidden wire field %q", key)
				}
				checkKeys(child)
			}
		case []any:
			for _, child := range x {
				checkKeys(child)
			}
		}
	}
	checkKeys(wire)
}

func TestSupportBundleEmptyDaemon(t *testing.T) {
	withFreshGlobalLogRing(t)
	t.Setenv("SOLU_DURABLE_LOG_DIR", "off")
	a := &apiServer{}
	r := httptest.NewRequest(http.MethodGet, "/api/support/bundle", nil)
	w := httptest.NewRecorder()
	a.handleSupportBundle(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp supportBundleResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Sections) == 0 {
		t.Fatal("empty daemon returned no section inventory")
	}
	for _, section := range resp.Sections {
		if !section.Empty || section.Note == "" {
			t.Errorf("section should be empty with a note: %+v", section)
		}
	}
}

func TestSupportBundleEmptyDaemonUsesAvailableLogs(t *testing.T) {
	withFreshGlobalLogRing(t)
	t.Setenv("SOLU_DURABLE_LOG_DIR", "off")
	globalLogRing.add("available-log")

	resp := (&apiServer{}).buildSupportBundle("")
	logs := sectionByID(t, resp, "logs")
	if logs.Empty || !strings.Contains(logs.Text, "available-log") {
		t.Fatalf("zero-value server bypassed its available log collector: %+v", logs)
	}
}

func TestSupportBundleOmitsPromptBearingLogRecords(t *testing.T) {
	withFreshGlobalLogRing(t)
	dir := t.TempDir()
	t.Setenv("SOLU_DURABLE_LOG_DIR", dir)
	const privatePrompt = "Prepare confidential acquisition of Acme"
	const operational = "ops-sentinel"
	promptRecord := `[native-input] pane=pane_private freshTurn=true userSeq=9 textLen=40 text="` + privatePrompt + `"`
	globalLogRing.add(operational)
	globalLogRing.add(promptRecord)
	if err := os.WriteFile(filepath.Join(dir, durableLogName), []byte(operational+"\n"+promptRecord+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	resp := supportBundleTestServer(t).buildSupportBundle("")
	for _, id := range []string{"logs", "daemonLog"} {
		section := sectionByID(t, resp, id)
		if !section.SelectedByDefault {
			t.Fatalf("%s must remain selected by default for this regression to exercise the default download", id)
		}
		if strings.Contains(section.Text, privatePrompt) {
			t.Errorf("%s leaked a private submitted prompt: %q", id, section.Text)
		}
		if !strings.Contains(section.Text, operational) {
			t.Errorf("%s discarded unrelated operational diagnostics: %q", id, section.Text)
		}
	}
}

func TestSupportBundleKVRedactsSecretsAfterLineBreaks(t *testing.T) {
	const awsKey = "AKIAIOSFODNN7EXAMPLE"
	for _, tc := range []struct {
		name  string
		value string
	}{
		{name: "newline", value: "update failed\n" + awsKey},
		{name: "carriage return", value: "update failed\r" + awsKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := supportBundleKV("update", map[string]any{"lastError": tc.value})
			section := finalizeSupportSection(supportBundleSection{Text: raw}, supportBundleSectionMaxBytes)
			wire, err := json.Marshal(section)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(section.Text, awsKey) || strings.Contains(string(wire), awsKey) {
				t.Fatalf("structured field leaked a secret after %s: text=%q wire=%s", tc.name, section.Text, wire)
			}
			if !strings.Contains(section.Text, "[REDACTED]") {
				t.Fatalf("structured field has no redaction marker after %s: %q", tc.name, section.Text)
			}
		})
	}
}

func TestSupportBundleDurableLogRedactsEncodedCredentials(t *testing.T) {
	const secret = "fixture-access-token-value"
	for _, format := range []string{"", "json"} {
		name := format
		if name == "" {
			name = "text"
		}
		t.Run(name, func(t *testing.T) {
			withFreshGlobalLogRing(t)
			dir := t.TempDir()
			t.Setenv("SOLU_DURABLE_LOG_DIR", dir)
			t.Setenv("LOG_LEVEL", "")
			t.Setenv("LOG_FORMAT", format)
			if format == "" {
				if err := os.Unsetenv("LOG_FORMAT"); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(dir, durableLogName)
			file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			logger := slog.New(buildLogHandler(file))
			logger.Info(`serialized credential {"access_token":"` + secret + `"}`)
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}

			section := sectionByID(t, supportBundleTestServer(t).buildSupportBundle(""), "daemonLog")
			if strings.Contains(section.Text, secret) {
				t.Fatalf("%s slog record leaked an encoded credential: %q", name, section.Text)
			}
			if !strings.Contains(section.Text, "[REDACTED]") {
				t.Fatalf("%s slog record has no redaction marker: %q", name, section.Text)
			}
		})
	}
}

func TestSupportBundleKVRedactsKeyedSecretsAcrossLineBreaks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		value  string
		secret string
	}{
		{name: "password after newline", value: "PASSWORD=\nhunter2", secret: "hunter2"},
		{name: "password after carriage return", value: "PASSWORD=\rhunter2", secret: "hunter2"},
		{name: "bearer after newline", value: "Bearer\nfixtureBearerToken123", secret: "fixtureBearerToken123"},
		{name: "bearer after carriage return", value: "Bearer\rfixtureBearerToken123", secret: "fixtureBearerToken123"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withFreshGlobalLogRing(t)
			t.Setenv("SOLU_DURABLE_LOG_DIR", "off")
			a := supportBundleTestServer(t)
			a.updater = &Updater{status: UpdateStatus{LastError: tc.value}}
			section := sectionByID(t, a.buildSupportBundle(""), "updates")
			wire, err := json.Marshal(section)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(section.Text, tc.secret) || strings.Contains(string(wire), tc.secret) {
				t.Fatalf("updates section leaked %q across a line break: text=%q wire=%s", tc.secret, section.Text, wire)
			}
			if !strings.Contains(section.Text, "[REDACTED]") {
				t.Fatalf("updates section has no redaction marker: %q", section.Text)
			}
		})
	}
}

func TestSupportBundlePaneTextRedactsBeforeTruncation(t *testing.T) {
	withFreshGlobalLogRing(t)
	t.Setenv("SOLU_DURABLE_LOG_DIR", "off")
	a := supportBundleTestServer(t)
	const secret = "hunter2"
	content := "PASSWORD=" + secret + " " + strings.Repeat("x ", 8188)
	if len(content) != feedbackMaxSnapshotBytes+len("PASSWORD=") {
		t.Fatalf("fixture length = %d, want %d", len(content), feedbackMaxSnapshotBytes+len("PASSWORD="))
	}
	p := &Pane{ID: "pane_boundary", Status: PaneRunning, buffer: NewRingBuffer(1 << 20)}
	_, _ = p.buffer.Write([]byte(content))
	a.mgr.panes[p.ID] = p

	section := sectionByID(t, a.buildSupportBundle(p.ID), "paneText")
	if strings.Contains(section.Text, secret) {
		t.Fatalf("pane tail leaked a credential whose label was outside the byte tail: %q", section.Text[:min(len(section.Text), 80)])
	}
	if section.Bytes > feedbackMaxSnapshotBytes {
		t.Fatalf("pane tail size = %d, want at most %d", section.Bytes, feedbackMaxSnapshotBytes)
	}
}

// TestSupportBundleOmitsModelReplyLogRecords pins the todo-normalization error
// path: when the normalizer's JSON does not parse it logs up to 120 characters
// of the model reply, and that reply routinely echoes the user's private todo
// text. Token redaction cannot make ordinary prose safe, so the record must be
// omitted from both default-selected log sections while surrounding operational
// diagnostics survive.
func TestSupportBundleOmitsModelReplyLogRecords(t *testing.T) {
	const privateProse = "Prepare confidential acquisition of Acme"
	const operational = "ops-normalize-sentinel"
	for _, tc := range []struct {
		name   string
		format string
	}{
		{name: "text", format: ""},
		{name: "json", format: "json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withFreshGlobalLogRing(t)
			dir := t.TempDir()
			t.Setenv("SOLU_DURABLE_LOG_DIR", dir)
			t.Setenv("LOG_LEVEL", "")
			t.Setenv("LOG_FORMAT", tc.format)
			if tc.format == "" {
				if err := os.Unsetenv("LOG_FORMAT"); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(dir, durableLogName)
			file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			logger := slog.New(buildLogHandler(file))
			logger.Info(operational)
			logger.Info(`todos: normalize todo_private: invalid json reply: ` + privateProse)
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}

			resp := supportBundleTestServer(t).buildSupportBundle("")
			for _, id := range []string{"logs", "daemonLog"} {
				section := sectionByID(t, resp, id)
				if !section.SelectedByDefault {
					t.Fatalf("%s must remain selected by default for this regression to exercise the default download", id)
				}
				if strings.Contains(section.Text, privateProse) {
					t.Errorf("%s leaked private model-reply prose: %q", id, section.Text)
				}
				if !strings.Contains(section.Text, operational) {
					t.Errorf("%s discarded unrelated operational diagnostics: %q", id, section.Text)
				}
			}
		})
	}
}
