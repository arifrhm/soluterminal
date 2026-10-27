package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"soluterminal-shared/voiceclean"
)

// The writing profile (Shinoque) starts Claude with the prose prompt. Context
// it is given is the open conversation: on this local path it rides along,
// labelled as conversation, never with the terminal instructions, and it
// doesn't reuse a spare started for the terminal.
func TestLocalCleanerWritingProfileCarriesConversationLocally(t *testing.T) {
	c, dir := newFakeCleaner(t, "ok")
	c.Prewarm(cleanVariant{profile: voiceclean.ProfileCode, withCtx: true})
	voiceWaitFor(t, "a terminal spare", func() bool { return countFiles(t, dir, "args") == 1 })

	convo := "Mara: Testing the new voice typing?"
	out := c.Clean(context.Background(), voiceclean.ProfileWriting, rawDictation, convo)
	if out.Status != "ok" {
		t.Fatalf("clean = %+v", out)
	}

	inputs, _ := filepath.Glob(filepath.Join(dir, "input.*"))
	if len(inputs) != 1 {
		t.Fatalf("used processes = %d, want 1", len(inputs))
	}
	id := strings.TrimPrefix(filepath.Base(inputs[0]), "input.")
	args, err := os.ReadFile(filepath.Join(dir, "args."+id))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "You post-process raw voice-dictation transcripts of everyday writing") {
		t.Error("writing run was not started with the writing prompt")
	}
	if strings.Contains(string(args), voiceclean.ContextInstructions) {
		t.Error("writing run must not carry terminal context instructions")
	}
	if !strings.Contains(string(args), "the conversation the user is writing in") {
		t.Error("writing run with a conversation lacks the conversation instructions")
	}

	b, _ := os.ReadFile(inputs[0])
	var msg struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(b, &msg); err != nil {
		t.Fatal(err)
	}
	want := "CONTEXT (conversation, for names and words only — do not copy or reply):\n" + convo +
		"\n\n---\n\nDICTATION TO CLEAN:\n" + rawDictation
	if msg.Message.Content != want {
		t.Errorf("user message = %q", msg.Message.Content)
	}
}

func TestNewCleanVariant(t *testing.T) {
	if v := newCleanVariant(voiceclean.ProfileWriting, ""); v.withCtx || v.profile != voiceclean.ProfileWriting {
		t.Errorf("writing variant = %+v", v)
	}
	if v := newCleanVariant(voiceclean.ProfileWriting, "Mara: hi"); !v.withCtx || v.profile != voiceclean.ProfileWriting {
		t.Errorf("writing variant with conversation = %+v", v)
	}
	if v := newCleanVariant(voiceclean.ProfileCode, "ctx"); !v.withCtx {
		t.Errorf("code variant with context = %+v", v)
	}
	if v := newCleanVariant("", ""); v.profile != voiceclean.ProfileCode || v.withCtx {
		t.Errorf("default variant = %+v", v)
	}
}

func TestVoiceCleanupWritingModeRunsLocallyAndEchoesMode(t *testing.T) {
	a, _ := newVoiceAPI(t, "ok")
	body, _ := json.Marshal(map[string]string{"text": rawDictation, "context": "Mara: hi", "mode": "writing"})
	rec := httptest.NewRecorder()
	a.voiceCleanup(rec, httptest.NewRequest("POST", "/api/voice/cleanup", bytes.NewReader(body)))
	var got map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || got["by"] != "local" || got["mode"] != "writing" {
		t.Fatalf("%d %v", rec.Code, got)
	}

	// Chat messages are often a few words; the writing profile tidies them.
	short, _ := json.Marshal(map[string]string{"text": "ok thanks", "mode": "writing"})
	rec = httptest.NewRecorder()
	a.voiceCleanup(rec, httptest.NewRequest("POST", "/api/voice/cleanup", bytes.NewReader(short)))
	got = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["cleanup"] != "ok" || got["by"] != "local" || got["mode"] != "writing" {
		t.Fatalf("short writing clip: %v", got)
	}

	// The terminal profile still skips short clips.
	code, _ := json.Marshal(map[string]string{"text": "ok thanks"})
	rec = httptest.NewRecorder()
	a.voiceCleanup(rec, httptest.NewRequest("POST", "/api/voice/cleanup", bytes.NewReader(code)))
	got = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["cleanup"] != "skipped" {
		t.Fatalf("short terminal clip: %v", got)
	}
}

// The conversation only ever goes to the user's own Claude login: when the
// local run fails, the cloud fallback gets the words without it.
func TestWritingCloudFallbackDropsTheConversation(t *testing.T) {
	a, _ := newVoiceAPI(t, "error")
	var saw map[string]any
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&saw)
		writeJSON(w, 200, map[string]string{"text": "Cloud cleaned.", "cleanup": "ok", "mode": "writing"})
	}))
	defer cloud.Close()
	a.cloudBase, a.cloudToken = cloud.URL, "tok"

	body, _ := json.Marshal(map[string]string{"text": rawDictation, "context": "Mara: private chat", "mode": "writing"})
	rec := httptest.NewRecorder()
	a.voiceCleanup(rec, httptest.NewRequest("POST", "/api/voice/cleanup", bytes.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if _, has := saw["context"]; has || saw["mode"] != "writing" || saw["text"] != rawDictation {
		t.Fatalf("cloud saw %v", saw)
	}
}

func TestCloudCleanupBodyKeepsTerminalContext(t *testing.T) {
	in, _ := json.Marshal(map[string]string{"text": "x", "context": "$ ls"})
	if string(cloudCleanupBody(in, false)) != string(in) {
		t.Fatal("terminal context must still reach the cloud fallback")
	}
}

func TestVoiceLocalSnapshotListsProfiles(t *testing.T) {
	a, _ := newVoiceAPI(t, "ok")
	snap := a.voiceLocalSnapshot()
	if strings.Join(snap.Profiles, ",") != "code,writing" {
		t.Fatalf("profiles = %v", snap.Profiles)
	}
	if !snap.WritingContext || snap.MinWritingBytes != voiceclean.MinWritingCleanupBytes {
		t.Fatalf("writing context capability = %v, min = %d", snap.WritingContext, snap.MinWritingBytes)
	}
}

// With only approved devices allowed, terminal context never reaches the
// cloud cleanup either.
func TestCloudCleanupBodyDropsContextWhenPrivate(t *testing.T) {
	in := []byte(`{"mode":"terminal","text":"deploy","context":"$ cat ~/.aws/credentials"}`)
	if out := string(cloudCleanupBody(in, true)); strings.Contains(out, "credentials") || !strings.Contains(out, `"text":"deploy"`) {
		t.Fatalf("private cleanup body = %s", out)
	}
	if out := string(cloudCleanupBody(in, false)); !strings.Contains(out, "credentials") {
		t.Fatalf("terminal context dropped outside private mode: %s", out)
	}
}
