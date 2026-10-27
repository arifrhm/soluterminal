package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestVoiceWordsPersistAndSanitize(t *testing.T) {
	dir := t.TempDir()
	v := newVoiceWords(dir)
	if len(v.list()) != 0 {
		t.Fatal("a new list must be empty")
	}
	got, err := v.set([]string{" Mara  Ellison ", "Shinoque", "<b>", "Ledger"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "Mara Ellison|Ledger" {
		t.Fatalf("saved %q", got)
	}
	if strings.Join(newVoiceWords(dir).list(), "|") != "Mara Ellison|Ledger" {
		t.Fatal("list not persisted")
	}
	if newVoiceWords(dir).formValue() != "Mara Ellison, Ledger" {
		t.Fatal("form value")
	}
}

func TestVoiceWordsPutIsLocalOnly(t *testing.T) {
	a := withVoice(&apiServer{}, &voiceServerState{voiceWords: newVoiceWords(t.TempDir())})
	body := bytes.NewBufferString(`{"words":["Mara"]}`)
	req := httptest.NewRequest("PUT", "/api/voice/vocabulary", body)
	req.Header.Set("X-Forwarded-Prefix", "/m/machine-1")
	rec := httptest.NewRecorder()
	a.handlePutVoiceWords(rec, req)
	if rec.Code != http.StatusForbidden || len(a.voiceWords().list()) != 0 {
		t.Fatalf("relay PUT: code=%d words=%v", rec.Code, a.voiceWords().list())
	}
	rec = httptest.NewRecorder()
	a.handlePutVoiceWords(rec, httptest.NewRequest("PUT", "/api/voice/vocabulary", bytes.NewBufferString(`{"words":["Mara"]}`)))
	var got voiceWordsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || len(got.Words) != 1 || got.Words[0] != "Mara" || len(got.Built) == 0 {
		t.Fatalf("local PUT: %d %+v", rec.Code, got)
	}
}

func multipartAudio(t *testing.T) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("audio", "audio.webm")
	_, _ = fw.Write([]byte("AUDIO"))
	_ = mw.WriteField("language", "en")
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

// Without local cleanup the daemon streams the upload to the cloud; with a
// word list it adds the "vocabulary" field and keeps the audio intact.
func TestProxyTranscribeSendsUserWords(t *testing.T) {
	for _, words := range [][]string{nil, {"Mara", "Ledger"}} {
		var fields map[string][]string
		var file string
		cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("cloud parse: %v", err)
			}
			fields = r.MultipartForm.Value
			if f, _, err := r.FormFile("audio"); err == nil {
				b, _ := io.ReadAll(f)
				file = string(b)
			}
			writeJSON(w, 200, map[string]string{"text": "ok"})
		}))
		a := withVoice(&apiServer{}, &voiceServerState{voiceWords: newVoiceWords(t.TempDir()), voiceLocal: newVoiceLocalSettings(t.TempDir())})
		if _, err := a.voiceWords().set(words); err != nil {
			t.Fatal(err)
		}
		a.cloudBase, a.cloudToken = cloud.URL, "tok"
		buf, ct := multipartAudio(t)
		req := httptest.NewRequest("POST", "/api/transcribe", buf)
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		a.transcribeAudio(rec, req)
		cloud.Close()
		if rec.Code != 200 || file != "AUDIO" || fields["language"][0] != "en" {
			t.Fatalf("words=%v: %d fields=%v file=%q", words, rec.Code, fields, file)
		}
		got := strings.Join(fields["vocabulary"], "")
		if want := strings.Join(words, ", "); got != want {
			t.Fatalf("vocabulary sent %q, want %q", got, want)
		}
	}
}

// Under "Only devices I approve" the proxied upload leaves the terminal
// snapshot behind; under "any" it goes with the audio as before.
func TestProxyTranscribeDropsContextWhenApprovedOnly(t *testing.T) {
	for _, policy := range []string{remoteAccessAny, remoteAccessApproved} {
		var fields map[string][]string
		var file string
		cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("cloud parse: %v", err)
			}
			fields = r.MultipartForm.Value
			if f, _, err := r.FormFile("audio"); err == nil {
				b, _ := io.ReadAll(f)
				file = string(b)
			}
			writeJSON(w, 200, map[string]string{"text": "ok"})
		}))
		dir := t.TempDir()
		a := withVoice(&apiServer{trustedDevices: newTrustedDevices(dir)}, &voiceServerState{voiceWords: newVoiceWords(dir), voiceLocal: newVoiceLocalSettings(dir)})
		if err := a.trustedDevices.setPolicy(policy); err != nil {
			t.Fatal(err)
		}
		a.cloudBase, a.cloudToken = cloud.URL, "tok"
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("audio", "audio.webm")
		_, _ = fw.Write([]byte("AUDIO"))
		_ = mw.WriteField("language", "en")
		_ = mw.WriteField("context", "$ cat secrets.env")
		_ = mw.Close()
		req := httptest.NewRequest("POST", "/api/transcribe", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		a.transcribeAudio(rec, req)
		cloud.Close()
		if rec.Code != 200 || file != "AUDIO" || fields["language"][0] != "en" {
			t.Fatalf("%s: %d fields=%v file=%q", policy, rec.Code, fields, file)
		}
		_, sent := fields["context"]
		if want := policy == remoteAccessAny; sent != want {
			t.Fatalf("%s: context sent=%v, want %v", policy, sent, want)
		}
	}
}

func promptField(t *testing.T, req *http.Request) string {
	t.Helper()
	_, params, _ := mime.ParseMediaType(req.Header.Get("Content-Type"))
	mr := multipart.NewReader(req.Body, params["boundary"])
	for {
		p, err := mr.NextPart()
		if err != nil {
			return ""
		}
		if p.FormName() == "prompt" {
			b, _ := io.ReadAll(p)
			return string(b)
		}
	}
}

type daemonRoundTripper func(*http.Request) (*http.Response, error)

func (f daemonRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func groqReply(text string) *http.Response {
	b, _ := json.Marshal(map[string]string{"text": text})
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(b))}
}

func TestOwnKeyTranscriptionChecksShortAnswers(t *testing.T) {
	prev := groqClient
	t.Cleanup(func() { groqClient = prev })
	var mu sync.Mutex
	var prompts []string
	reply := func(prompted, unprompted string) {
		groqClient = &http.Client{Transport: daemonRoundTripper(func(r *http.Request) (*http.Response, error) {
			p := promptField(t, r)
			mu.Lock()
			prompts = append(prompts, p)
			mu.Unlock()
			if p != "" {
				return groqReply(prompted), nil
			}
			return groqReply(unprompted), nil
		})}
	}
	sawPrompt := func(word string) bool {
		mu.Lock()
		defer mu.Unlock()
		for _, p := range prompts {
			if strings.Contains(p, word) && strings.Contains(p, "Shinoque") {
				return true
			}
		}
		return false
	}

	// Room tone: the prompted run invents names, the unprompted one hears nothing.
	reply("Sosu.", ".")
	got := transcribeWithVocabulary(t.Context(), "key", "a.webm", []byte("A"), "en", []string{"Mara"})
	if got.text != "." || !sawPrompt("Mara") {
		t.Fatalf("room tone: text=%q prompts=%q", got.text, prompts)
	}

	// Short real speech keeps the better-spelled prompted answer.
	prompts = nil
	reply("Deploy Shinoque.", "Deploy shinoki.")
	if got := transcribeWithVocabulary(t.Context(), "key", "a.webm", []byte("A"), "en", nil); got.text != "Deploy Shinoque." {
		t.Fatalf("short speech: %q", got.text)
	}

	// A long answer on a big recording needs only the prompted run.
	prompts = nil
	long := "Restart SoluTerminal and reconnect all of the panes now."
	reply(long, "unused")
	big := bytes.Repeat([]byte("A"), 200<<10)
	got = transcribeWithVocabulary(t.Context(), "key", "a.webm", big, "en", nil)
	mu.Lock()
	n := len(prompts)
	mu.Unlock()
	if got.text != long || n != 1 {
		t.Fatalf("long: text=%q calls=%d", got.text, n)
	}
}
