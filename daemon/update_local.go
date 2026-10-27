package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// POST /api/update/apply-local — the owner's "follow main" channel.
//
// scripts/follow-main.sh builds the latest green main on this machine and
// asks the running daemon to install it through the normal guarded update
// swap (Updater.ApplyLocal). Only this machine may do that: the request must
// come over loopback (never the cloud relay), carry X-Solu-Local-Build (a
// cross-site page cannot send it without a CORS preflight), and name a
// regular, owner-only file inside <data>/local-builds/. Running daemons still
// move forward to any NEWER release on their own; versions are stamped
// <release>.<commits since the bump> so they sort between releases.

var localBuildVersionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+){2,4}$`)

// localBuildMaxBytes bounds a local build (release daemons are ~50 MB).
const localBuildMaxBytes = 512 << 20

type applyLocalReq struct {
	Path           string `json:"path"`
	SHA256         string `json:"sha256"`
	Version        string `json:"version"`
	AllowDowngrade bool   `json:"allowDowngrade,omitempty"`
}

func localBuildsDir(dataDir string) string { return filepath.Join(dataDir, "local-builds") }

// validateLocalBuild resolves req.Path and checks it is a regular,
// owner-only file directly inside the local-builds directory.
func validateLocalBuild(dataDir string, req applyLocalReq) (string, error) {
	if !localBuildVersionPattern.MatchString(req.Version) {
		return "", errors.New("version must look like 0.0.1168.3")
	}
	if !regexp.MustCompile(`^[0-9a-fA-F]{64}$`).MatchString(req.SHA256) {
		return "", errors.New("sha256 must be 64 hex characters")
	}
	dir, err := filepath.EvalSymlinks(localBuildsDir(dataDir))
	if err != nil {
		return "", errors.New("no local-builds directory")
	}
	if !filepath.IsAbs(req.Path) {
		return "", errors.New("path must be absolute")
	}
	real, err := filepath.EvalSymlinks(req.Path)
	if err != nil {
		return "", err
	}
	if filepath.Dir(real) != dir {
		return "", errors.New("local builds must live directly in " + dir)
	}
	info, err := os.Lstat(real)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return "", errors.New("build is writable by other users")
	}
	if !fileOwnedByCurrentUser(info) {
		return "", errors.New("build is not owned by the daemon's user")
	}
	return real, nil
}

func (a *apiServer) updateApplyLocal(w http.ResponseWriter, r *http.Request) {
	if a.updater == nil {
		http.Error(w, "updater disabled", 503)
		return
	}
	if runtime.GOOS == "windows" {
		// follow-main builds the WSL/Linux daemon; Windows mode bits can't
		// express owner-only, so the channel is not offered there.
		http.Error(w, "follow-main is only available for the WSL/Linux daemon", http.StatusNotImplemented)
		return
	}
	if fileAccessOverRelay(r) || !isLoopback(r) || !safeLoopbackHost(r.Host) {
		http.Error(w, "local builds can only be installed from this machine", http.StatusForbidden)
		return
	}
	if r.Header.Get("X-Solu-Local-Build") != "1" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "missing X-Solu-Local-Build / JSON content type", http.StatusBadRequest)
		return
	}
	var req applyLocalReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), 400)
		return
	}
	path, err := validateLocalBuild(a.dataDir, req)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	err = a.updater.ApplyLocal(r.Context(), os.Exit, localUpdateCandidate{
		path: path, sha256: req.SHA256, version: req.Version, allowDowngrade: req.AllowDowngrade,
	})
	if err != nil {
		writeUpdateApplyError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"applying": true, "version": req.Version})
}
