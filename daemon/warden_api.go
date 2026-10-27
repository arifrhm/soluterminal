package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// HTTP surface for the opt-in Warden. Deliberately ISOLATED from the
// schedules/reports rail: the warden owns its own settings, status, and runs
// endpoints so enabling or exercising it cannot perturb existing surfaces.
//
//	GET  /api/warden/settings   → current settings
//	PUT  /api/warden/settings   → update (enable/disable, model, delays, caps)
//	GET  /api/warden/status     → enabled + recent-run summaries
//	POST /api/warden/run-now    → fire an on-demand fleet brief immediately
//	GET  /api/warden/runs/{name}→ read one run log

func (a *apiServer) handleWardenSettings(w http.ResponseWriter, r *http.Request) {
	if a.wardenSettings == nil {
		http.Error(w, "warden unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, a.wardenSettings.Snapshot())
	case http.MethodPut:
		var req wardenSettingsPatch
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		updated, err := a.wardenSettings.Patch(req)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, errInvalidWardenSettings) {
				status = http.StatusBadRequest
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

type wardenStatusDTO struct {
	Enabled bool              `json:"enabled"`
	Runs    []wardenRunRecord `json:"runs"`
}

func (a *apiServer) handleWardenStatus(w http.ResponseWriter, r *http.Request) {
	if a.wardenSettings == nil {
		http.Error(w, "warden unavailable", http.StatusServiceUnavailable)
		return
	}
	dto := wardenStatusDTO{Enabled: a.wardenSettings.Enabled()}
	if a.warden != nil {
		dto.Runs = a.warden.RecentRuns()
	}
	writeJSON(w, http.StatusOK, dto)
}

func (a *apiServer) handleWardenRunNow(w http.ResponseWriter, r *http.Request) {
	if a.warden == nil {
		http.Error(w, "warden unavailable", http.StatusServiceUnavailable)
		return
	}
	if a.warden.RunNow(context.Background()) {
		writeJSON(w, http.StatusOK, map[string]any{"started": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"started": false, "reason": "a warden run is already in flight or the hourly cap is reached"})
}

// handleWardenRunLog serves one run log from <dataDir>/warden-runs. The name
// is path-sanitized to its base so it can never escape the run directory.
func (a *apiServer) handleWardenRunLog(w http.ResponseWriter, r *http.Request) {
	if a.warden == nil {
		http.Error(w, "warden unavailable", http.StatusServiceUnavailable)
		return
	}
	name := filepath.Base(r.PathValue("name"))
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		http.Error(w, "bad name", http.StatusBadRequest)
		return
	}
	b, err := os.ReadFile(filepath.Join(a.warden.runDir(), name))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(b)
}
