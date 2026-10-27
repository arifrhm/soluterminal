package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"soluterminal-shared/voiceclean"
)

// "Words to recognise": the user's own names and terms, added to the fixed
// product list that biases Whisper's spelling. Stored in
// ~/.solu/voice-words.json, sent with each dictation's audio, and never mixed
// with terminal text.

const voiceWordsFile = "voice-words.json"

type voiceWords struct {
	mu    sync.Mutex
	path  string
	words []string
}

func newVoiceWords(dataDir string) *voiceWords {
	v := &voiceWords{path: filepath.Join(dataDir, voiceWordsFile)}
	if b, err := os.ReadFile(v.path); err == nil {
		var rec struct {
			Words []string `json:"words"`
		}
		if json.Unmarshal(b, &rec) == nil {
			v.words = voiceclean.SanitizeWords(rec.Words)
		}
	}
	return v
}

func (v *voiceWords) list() []string {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string{}, v.words...)
}

func (v *voiceWords) set(words []string) ([]string, error) {
	clean := voiceclean.SanitizeWords(words)
	v.mu.Lock()
	defer v.mu.Unlock()
	b, err := json.Marshal(struct {
		Words []string `json:"words"`
	}{clean})
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomicSynced(v.path, b, 0o600); err != nil {
		return nil, err
	}
	v.words = clean
	return append([]string{}, clean...), nil
}

// formValue is the "vocabulary" field sent to the cloud with the audio.
func (v *voiceWords) formValue() string {
	return strings.Join(v.list(), ", ")
}

type voiceWordsResponse struct {
	Words    []string `json:"words"`
	Built    []string `json:"builtIn"`
	MaxWords int      `json:"maxWords"`
}

func (a *apiServer) voiceWordsSnapshot() voiceWordsResponse {
	return voiceWordsResponse{
		Words:    a.voiceWords().list(),
		Built:    voiceclean.BaseVocabulary,
		MaxWords: voiceclean.MaxCustomWords,
	}
}

func (a *apiServer) handleGetVoiceWords(w http.ResponseWriter, _ *http.Request) {
	if a.voiceWords() == nil {
		http.Error(w, "voice settings are unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, a.voiceWordsSnapshot())
}

// handlePutVoiceWords is local-only: the list is chosen on the computer itself.
func (a *apiServer) handlePutVoiceWords(w http.ResponseWriter, r *http.Request) {
	if requestViaRelay(r) {
		http.Error(w, "this setting can only be changed on the computer itself", http.StatusForbidden)
		return
	}
	if a.voiceWords() == nil {
		http.Error(w, "voice settings are unavailable", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Words *[]string `json:"words"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil || body.Words == nil {
		http.Error(w, `body must set "words" to a list`, http.StatusBadRequest)
		return
	}
	if _, err := a.voiceWords().set(*body.Words); err != nil {
		http.Error(w, "could not save the setting", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, a.voiceWordsSnapshot())
}

type groqTranscribeResult struct {
	text     string
	verbose  voiceclean.Verbose
	status   int
	raw      []byte
	err      error
	parseErr error
}

// groqTranscribe is one Whisper request on the user's own Groq key.
func groqTranscribe(ctx context.Context, apiKey, filename string, audio []byte, language, prompt string) groqTranscribeResult {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return groqTranscribeResult{err: err}
	}
	if _, err := part.Write(audio); err != nil {
		return groqTranscribeResult{err: err}
	}
	_ = mw.WriteField("model", groqModel)
	// verbose_json adds the duration and segment timings the silence checks use.
	_ = mw.WriteField("response_format", "verbose_json")
	if language != "" {
		_ = mw.WriteField("language", language)
	}
	if prompt != "" {
		_ = mw.WriteField("prompt", prompt)
	}
	if err := mw.Close(); err != nil {
		return groqTranscribeResult{err: err}
	}
	req, err := http.NewRequestWithContext(ctx, "POST", groqTranscribeURL, &body)
	if err != nil {
		return groqTranscribeResult{err: err}
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := groqClient.Do(req)
	if err != nil {
		return groqTranscribeResult{err: err}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := groqTranscribeResult{status: resp.StatusCode, raw: raw}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if v, err := voiceclean.ParseVerbose(raw); err != nil {
			out.parseErr = err
		} else {
			out.text, out.verbose = v.Text, v
		}
	}
	return out
}

// transcribeWithVocabulary runs Whisper with the name list and checks short
// answers against an unprompted run (voiceclean.RunWithVocabulary), so room
// tone doesn't come back as name-like words.
func transcribeWithVocabulary(ctx context.Context, apiKey, filename string, audio []byte, language string, custom []string) groqTranscribeResult {
	language = voiceclean.NormalizeLanguage(language)
	gr, note := voiceclean.RunWithVocabulary(ctx, len(audio), language, custom, voiceclean.VocabularyPrompt(custom),
		func(ctx context.Context, p string) groqTranscribeResult {
			return groqTranscribe(ctx, apiKey, filename, audio, language, p)
		}, func(r groqTranscribeResult) (string, bool) {
			return r.text, r.err == nil && r.status == http.StatusOK && r.parseErr == nil
		})
	if note != "" {
		log.Printf("voice[daemon]: used the unprompted transcription (%s)", note)
	}
	if bad, why := voiceclean.SilenceHallucination(gr.verbose); bad {
		// Whisper invented words from silence or noise: try once without the
		// prompt, else report no speech (empty text).
		u := groqTranscribe(ctx, apiKey, filename, audio, language, "")
		if again, _ := voiceclean.SilenceHallucination(u.verbose); u.err == nil && u.status == http.StatusOK && u.parseErr == nil && !again && !voiceclean.IsNoSpeech(u.text) {
			return u
		}
		log.Printf("voice[daemon]: treated as no speech (%s)", why)
		gr.text = ""
		gr.verbose = voiceclean.Verbose{}
	}
	return gr
}
