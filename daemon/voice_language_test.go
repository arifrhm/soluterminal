package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type voiceLanguageTransport func(*http.Request) (*http.Response, error)

func (f voiceLanguageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func voiceLanguageResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func voiceLanguageForm(language string) (*bytes.Buffer, string) {
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	p, _ := mw.CreateFormFile("audio", "audio.wav")
	_, _ = p.Write(make([]byte, 100000))
	_ = mw.WriteField("language", language)
	_ = mw.Close()
	return &b, mw.FormDataContentType()
}
func TestVoiceLanguageDirectAndTransparentProxy(t *testing.T) {
	for _, proxy := range []bool{false, true} {
		for _, language := range []string{"en", ""} {
			t.Run(strings.Join([]string{map[bool]string{true: "proxy", false: "direct"}[proxy], language}, "/"), func(t *testing.T) {
				t.Setenv("GROQ_API_KEY", "test")
				previousGroq, previousCloud := groqClient, cloudProxyClient
				t.Cleanup(func() { groqClient, cloudProxyClient = previousGroq, previousCloud })
				transport := voiceLanguageTransport(func(req *http.Request) (*http.Response, error) {
					if err := req.ParseMultipartForm(4 << 20); err != nil {
						t.Fatal(err)
					}
					defer req.MultipartForm.RemoveAll()
					if req.FormValue("language") != language {
						t.Fatalf("language=%q", req.FormValue("language"))
					}
					if proxy {
						return voiceLanguageResponse(`{"text":"hello","requestedLanguage":"en","detectedLanguage":"ms","unknownFuture":true}`), nil
					}
					return voiceLanguageResponse(`{"text":"hello","language":"English","duration":60.0004}`), nil
				})
				groqClient = &http.Client{Transport: transport}
				cloudProxyClient = &http.Client{Transport: transport}
				a := withVoice(&apiServer{}, &voiceServerState{voiceWords: newVoiceWords(t.TempDir())})
				if proxy {
					a.cloudBase = "https://fixture.invalid"
					a.cloudToken = "test"
					_, _ = a.voiceWords().set([]string{"Mara"})
				}
				b, ct := voiceLanguageForm(language)
				req := httptest.NewRequest("POST", "/api/transcribe", b)
				req.Header.Set("Content-Type", ct)
				rec := httptest.NewRecorder()
				a.transcribeAudio(rec, req)
				if rec.Code != 200 {
					t.Fatal(rec.Code, rec.Body.String())
				}
				var out map[string]any
				_ = json.Unmarshal(rec.Body.Bytes(), &out)
				if proxy {
					if out["unknownFuture"] != true || out["detectedLanguage"] != "ms" {
						t.Fatal(out)
					}
				} else {
					requested := language
					if requested == "" {
						requested = "auto"
					}
					if out["requestedLanguage"] != requested || out["detectedLanguage"] != "en" || out["audioDurationMs"] != float64(60000) {
						t.Fatal(out)
					}
				}
			})
		}
	}
}
func TestVoiceLanguageCleanupHintIsRequestLocal(t *testing.T) {
	a, dir := newVoiceAPI(t, "ok")
	for _, language := range []string{"en", "ms", ""} {
		body, _ := json.Marshal(map[string]string{"text": rawDictation, "language": language, "mode": "writing", "context": "private conversation"})
		rec := httptest.NewRecorder()
		a.voiceCleanup(rec, httptest.NewRequest("POST", "/api/voice/cleanup", bytes.NewReader(body)))
		if rec.Code != 200 {
			t.Fatal(rec.Code, rec.Body.String())
		}
		if language != "" && !strings.Contains(rec.Body.String(), `"requestedLanguage":"`+language+`"`) {
			t.Fatal(rec.Body.String())
		}
	}
	_ = dir // fake-cleaner prompt payloads are exercised by the shared prompt tests.
}
func TestVoiceLanguageRebuiltMultipartBudget(t *testing.T) {
	previous := cloudProxyClient
	t.Cleanup(func() { cloudProxyClient = previous })
	calls := 0
	cloudProxyClient = &http.Client{Transport: voiceLanguageTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		return voiceLanguageResponse(`{"text":"unexpected"}`), nil
	})}
	a := withVoice(&apiServer{cloudBase: "https://fixture.invalid", cloudToken: "test"}, &voiceServerState{voiceWords: newVoiceWords(t.TempDir())})
	_, _ = a.voiceWords().set([]string{"Mara"})
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	p, _ := mw.CreateFormFile("audio", "audio.wav")
	_, _ = p.Write(make([]byte, cloudVoiceBodyBytes))
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/api/transcribe", &b)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	a.transcribeAudio(rec, req)
	if rec.Code != 413 || calls != 0 {
		t.Fatal(rec.Code, calls)
	}
}
