package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"soluterminal-shared/providers"
)

type apiKeyAccount struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type apiKeyAccountState struct {
	Version  int                      `json:"version"`
	Selected string                   `json:"selected"`
	Accounts map[string]apiKeyAccount `json:"accounts"`
}

type apiKeyAccountStore struct {
	mu                *sync.Mutex
	dataDir, provider string
}

var errAPIKeyAccountUnavailable = errors.New("API key account unavailable")

// HTTP handlers and gateway requests construct short-lived store handles.
// Share the lock for a backing file so concurrent edits cannot lose accounts.
var apiKeyAccountLocks sync.Map

func newAPIKeyAccountStore(dataDir, provider string) *apiKeyAccountStore {
	path := filepath.Join(dataDir, "api-keys", provider+".json")
	lock, _ := apiKeyAccountLocks.LoadOrStore(path, &sync.Mutex{})
	return &apiKeyAccountStore{mu: lock.(*sync.Mutex), dataDir: dataDir, provider: provider}
}

func (s *apiKeyAccountStore) path() string {
	return filepath.Join(s.dataDir, "api-keys", s.provider+".json")
}

func (s *apiKeyAccountStore) readLocked() (apiKeyAccountState, error) {
	st := apiKeyAccountState{Version: 1, Accounts: map[string]apiKeyAccount{}}
	raw, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil || len(raw) > 1<<20 {
		return st, errors.New("API key account store unavailable")
	}
	if json.Unmarshal(raw, &st) != nil || st.Version != 1 || st.Accounts == nil || len(st.Accounts) > 128 {
		return st, errors.New("API key account store malformed")
	}
	key, err := harnessSecretKey(s.dataDir)
	if err != nil {
		return st, err
	}
	for name, account := range st.Accounts {
		if !validCredName(name) || account.Name != name || !strings.HasPrefix(account.Value, harnessSecretPrefix) {
			return st, errors.New("API key account store malformed")
		}
		account.Value, err = openHarnessSecret(key, account.Value)
		if err != nil {
			return st, err
		}
		st.Accounts[name] = account
	}
	return st, nil
}

func (s *apiKeyAccountStore) writeLocked(st apiKeyAccountState) error {
	key, err := harnessSecretKey(s.dataDir)
	if err != nil {
		return err
	}
	disk := apiKeyAccountState{Version: 1, Selected: st.Selected, Accounts: map[string]apiKeyAccount{}}
	for name, account := range st.Accounts {
		sealed, err := sealHarnessSecret(key, account.Value)
		if err != nil {
			return err
		}
		disk.Accounts[name] = apiKeyAccount{Name: name, Value: sealed}
	}
	raw, err := json.Marshal(disk)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path()), 0700); err != nil {
		return err
	}
	return writeFileAtomicSynced(s.path(), raw, 0600)
}

func (s *apiKeyAccountStore) save(name, value string) error {
	if !validCredName(name) || name == "__team__" || value == "" || len(value) > 8192 || strings.ContainsAny(value, "\r\n\x00") {
		return errors.New("invalid API key account")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.readLocked()
	if err != nil {
		return err
	}
	if len(st.Accounts) >= 128 && st.Accounts[name].Name == "" {
		return errors.New("too many API key accounts")
	}
	st.Accounts[name] = apiKeyAccount{Name: name, Value: value}
	if st.Selected == "" {
		st.Selected = name
	}
	return s.writeLocked(st)
}

func (s *apiKeyAccountStore) list() ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.readLocked()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(st.Accounts))
	for name := range st.Accounts {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([]map[string]any, 0, len(names))
	for _, name := range names {
		rows = append(rows, map[string]any{"id": "personal/" + name, "name": name, "selected": st.Selected == name})
	}
	return rows, nil
}

func (s *apiKeyAccountStore) selected(name string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.readLocked()
	if err != nil {
		return "", "", err
	}
	if name == "" {
		name = st.Selected
	}
	if !validCredName(name) {
		return "", "", errAPIKeyAccountUnavailable
	}
	acc, ok := st.Accounts[name]
	if !ok {
		return "", "", errAPIKeyAccountUnavailable
	}
	return name, acc.Value, nil
}

func (s *apiKeyAccountStore) selectName(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.readLocked()
	if err != nil {
		return err
	}
	if _, ok := st.Accounts[name]; !ok {
		return errors.New("API key account unavailable")
	}
	st.Selected = name
	return s.writeLocked(st)
}

func (s *apiKeyAccountStore) remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.readLocked()
	if err != nil {
		return err
	}
	delete(st.Accounts, name)
	if st.Selected == name {
		st.Selected = ""
	}
	return s.writeLocked(st)
}

func (a *apiServer) apiKeyAccounts(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	p, ok := providers.Get(provider)
	if !ok || p.KeyAPI == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && fileAccessOverRelay(r) {
		http.Error(w, "credential changes are local-only", http.StatusForbidden)
		return
	}
	s := newAPIKeyAccountStore(a.dataDir, provider)
	switch r.Method {
	case http.MethodGet:
		rows, err := s.list()
		if err != nil {
			http.Error(w, "credential store unavailable", 500)
			return
		}
		writeJSON(w, 200, map[string]any{"provider": provider, "accounts": rows, "keyPresent": len(rows) > 0})
	case http.MethodPut:
		var req apiKeyAccount
		if !harnessDecode(w, r, &req) {
			return
		}
		if err := s.save(req.Name, req.Value); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	case http.MethodDelete:
		if err := s.remove(r.PathValue("name")); err != nil {
			http.Error(w, "credential store unavailable", 500)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	case http.MethodPost:
		if strings.HasSuffix(r.URL.Path, "/select") {
			var req struct {
				Name string `json:"name"`
			}
			if !harnessDecode(w, r, &req) {
				return
			}
			if err := s.selectName(req.Name); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		_, key, err := s.selected("")
		if err != nil {
			http.Error(w, "no selected API key", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		base := strings.TrimRight(p.KeyAPI.BaseURL, "/")
		if p.KeyAPI.Protocol == "anthropic" {
			base += "/v1"
		}
		upstream, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
		if err != nil {
			http.Error(w, "provider unavailable", 502)
			return
		}
		if p.KeyAPI.Protocol == "openai" {
			upstream.Header.Set("Authorization", "Bearer "+key)
		} else {
			upstream.Header.Set("x-api-key", key)
			upstream.Header.Set("anthropic-version", "2023-06-01")
		}
		resp, err := (&http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(upstream)
		if err != nil {
			http.Error(w, "provider unavailable", 502)
			return
		}
		defer resp.Body.Close()
		writeJSON(w, 200, map[string]any{"status": resp.StatusCode, "ok": resp.StatusCode >= 200 && resp.StatusCode < 300})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
