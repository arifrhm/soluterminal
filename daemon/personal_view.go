package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The personal view lets an operator or team install see the app exactly as a
// free-account user does, without signing out: the web hides every team and
// operator surface, and agent panes started while it is on get the public MCP
// tool set. It is a view, not a shutdown. Background engines keep running and
// panes that were already open keep their tools.
//
// The setting lives in the data dir so every window and the phone share it,
// and it can only be changed on the computer itself. Installs without team or
// operator features have nothing to hide, so for them it is always off.

const (
	personalViewFile           = "view.json"
	personalViewMaxRequestBody = 1 << 10
)

type personalViewRecord struct {
	Personal bool `json:"personal"`
	// Since is when the view was last turned on. Panes created before it keep
	// the full MCP tool set.
	Since time.Time `json:"since,omitempty"`
}

type personalView struct {
	mu     sync.Mutex
	path   string
	record personalViewRecord
	now    func() time.Time
}

func newPersonalView(dataDir string) *personalView {
	v := &personalView{path: filepath.Join(dataDir, personalViewFile), now: time.Now}
	if b, err := os.ReadFile(v.path); err == nil {
		var rec personalViewRecord
		if json.Unmarshal(b, &rec) == nil {
			v.record = rec
		}
	}
	return v
}

func (v *personalView) snapshot() personalViewRecord {
	if v == nil {
		return personalViewRecord{}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.record
}

// set stores the choice. Turning it on restarts the "since" clock; turning it
// off keeps the last time for diagnostics.
func (v *personalView) set(on bool) (personalViewRecord, error) {
	v.mu.Lock()
	rec := v.record
	if on && !rec.Personal {
		rec.Since = v.now().UTC()
	}
	rec.Personal = on
	b, err := json.Marshal(rec)
	if err == nil {
		err = writeFileAtomicSynced(v.path, b, 0o600)
	}
	if err == nil {
		v.record = rec
	}
	v.mu.Unlock()
	return rec, err
}

// personalViewAvailable reports whether this install has anything for the
// personal view to hide.
func (a *apiServer) personalViewAvailable() bool {
	if a == nil || a.entitlements == nil {
		return false
	}
	if on, _ := a.entitlements.operatorVerdict(); on {
		return true
	}
	team, _ := a.entitlements.verdict()
	return team
}

// personalViewOn reports whether the personal view is in effect.
func (a *apiServer) personalViewOn() bool {
	if a == nil || a.view == nil {
		return false
	}
	return a.view.snapshot().Personal && a.personalViewAvailable()
}

// personalViewForPane decides the MCP surface for one caller while the view
// is on. Panes created after the view was turned on, and callers that aren't
// SoluTerminal panes, get the public surface; panes that were already running
// keep their tools so in-flight agent work isn't cut off mid-task.
func (a *apiServer) personalViewForPane(paneID string) bool {
	if !a.personalViewOn() {
		return false
	}
	if paneID == "" || a.mgr == nil {
		return true
	}
	p, ok := a.mgr.Get(paneID)
	if !ok || p == nil {
		return true
	}
	since := a.view.snapshot().Since
	return since.IsZero() || !p.CreatedAt.Before(since)
}

type personalViewDTO struct {
	Personal  bool   `json:"personal"`
	Available bool   `json:"available"`
	Since     string `json:"since,omitempty"`
}

func (a *apiServer) personalViewSnapshot() personalViewDTO {
	rec := a.view.snapshot()
	dto := personalViewDTO{Available: a.personalViewAvailable()}
	dto.Personal = rec.Personal && dto.Available
	if dto.Personal && !rec.Since.IsZero() {
		dto.Since = rec.Since.Format(time.RFC3339)
	}
	return dto
}

func (a *apiServer) handleGetView(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.personalViewSnapshot())
}

func (a *apiServer) handlePutView(w http.ResponseWriter, r *http.Request) {
	if requestViaRelay(r) {
		http.Error(w, "this setting can only be changed on the computer itself", http.StatusForbidden)
		return
	}
	if a.view == nil {
		http.Error(w, "the personal view is unavailable", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Personal *bool `json:"personal"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, personalViewMaxRequestBody)).Decode(&body); err != nil || body.Personal == nil {
		http.Error(w, `body must set "personal" to true or false`, http.StatusBadRequest)
		return
	}
	if *body.Personal && !a.personalViewAvailable() {
		http.Error(w, "the personal view is only for team installs", http.StatusForbidden)
		return
	}
	if _, err := a.view.set(*body.Personal); err != nil {
		http.Error(w, "could not save the setting", http.StatusInternalServerError)
		return
	}
	log.Printf("view: personal=%t", *body.Personal)
	a.publishEntitlementsChanged()
	writeJSON(w, http.StatusOK, a.personalViewSnapshot())
}

// publishEntitlementsChanged tells every open window (and the phone) to
// re-read /api/entitlements, so a view switch or a new cloud verdict shows
// without a reload.
func (a *apiServer) publishEntitlementsChanged() {
	if a == nil || a.uiBus == nil {
		return
	}
	a.uiBus.publish(uiEvent{Type: "entitlements-changed", Ts: time.Now().UnixMilli()})
}
