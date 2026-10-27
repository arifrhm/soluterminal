package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"soluterminal-shared/voiceclean"
)

// "Clean up dictation with my Claude plan": on by default when Claude Code is
// installed and signed in. Stored in ~/.solu/voice-local.json.

const voiceLocalSettingsFile = "voice-local.json"

type voiceLocalSettings struct {
	mu      sync.Mutex
	path    string
	Enabled *bool `json:"enabled,omitempty"`
}

func newVoiceLocalSettings(dataDir string) *voiceLocalSettings {
	s := &voiceLocalSettings{path: filepath.Join(dataDir, voiceLocalSettingsFile)}
	if b, err := os.ReadFile(s.path); err == nil {
		var rec voiceLocalSettings
		if json.Unmarshal(b, &rec) == nil {
			s.Enabled = rec.Enabled
		}
	}
	return s
}

func (s *voiceLocalSettings) enabled() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Enabled == nil || *s.Enabled
}

func (s *voiceLocalSettings) set(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Enabled = &on
	b, err := json.Marshal(struct {
		Enabled *bool `json:"enabled"`
	}{s.Enabled})
	if err != nil {
		return err
	}
	return writeFileAtomicSynced(s.path, b, 0o600)
}

// localCleanupAvailable reports whether a dictation would be cleaned on the
// user's Claude login, and if not, why (shown in Settings).
func (a *apiServer) localCleanupAvailable() (bool, string) {
	if a.localCleaner() == nil || a.voiceLocal() == nil {
		return false, "unavailable"
	}
	if !a.voiceLocal().enabled() {
		return false, "off"
	}
	// Before the first Claude probe finishes we simply try: a failed run
	// falls back to the cloud like any other failure.
	if st := a.currentClaudeStatus(); st.checked {
		if !st.installed {
			return false, "not_installed"
		}
		if !st.authed {
			return false, "not_signed_in"
		}
	}
	if a.localCleaner().paused() {
		return false, "paused"
	}
	return true, ""
}

type voiceLocalResponse struct {
	Enabled   bool   `json:"enabled"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Model     string `json:"model"`
	// Profiles lists the cleanup modes /api/voice/cleanup understands, so a
	// prose client (Shinoque) can tell this daemon won't use the terminal
	// prompt on its text.
	Profiles []string `json:"profiles"`
	// WritingContext: a writing cleanup's context (the open conversation)
	// is used only on the user's own Claude login and dropped before any
	// cloud fallback. Clients send a conversation only when this is true;
	// older daemons would forward it to the cloud fallback unchanged.
	WritingContext bool `json:"writingContext"`
	// MinWritingBytes is the shortest dictation the writing profile tidies.
	MinWritingBytes int `json:"minWritingBytes"`
}

func (a *apiServer) voiceLocalSnapshot() voiceLocalResponse {
	ok, reason := a.localCleanupAvailable()
	return voiceLocalResponse{
		Enabled:   a.voiceLocal().enabled(),
		Available: ok,
		Reason:    reason,
		Model:     localCleanupModel,
		Profiles:  []string{string(voiceclean.ProfileCode), string(voiceclean.ProfileWriting)},

		WritingContext:  true,
		MinWritingBytes: voiceclean.MinWritingCleanupBytes,
	}
}

func (a *apiServer) handleGetVoiceLocal(w http.ResponseWriter, _ *http.Request) {
	if a.voiceLocal() == nil {
		http.Error(w, "voice settings are unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, a.voiceLocalSnapshot())
}

func (a *apiServer) handlePutVoiceLocal(w http.ResponseWriter, r *http.Request) {
	if a.voiceLocal() == nil {
		http.Error(w, "voice settings are unavailable", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil || body.Enabled == nil {
		http.Error(w, `body must set "enabled" to true or false`, http.StatusBadRequest)
		return
	}
	if err := a.voiceLocal().set(*body.Enabled); err != nil {
		http.Error(w, "could not save the setting", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, a.voiceLocalSnapshot())
}

// handleVoicePrewarm starts a Claude process while the user is still
// talking, so the cleanup that follows skips the start-up time.
func (a *apiServer) handleVoicePrewarm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Context bool   `json:"context"`
		Mode    string `json:"mode"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body)
	if ok, _ := a.localCleanupAvailable(); ok {
		v := cleanVariant{profile: voiceclean.ParseProfile(body.Mode), withCtx: body.Context}
		go a.localCleaner().Prewarm(v)
	}
	w.WriteHeader(http.StatusNoContent)
}
