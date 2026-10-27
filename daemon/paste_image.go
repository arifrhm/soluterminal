package main

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Max image size. 10MiB covers phone screenshots / pasted designs with
// headroom; anything larger is almost certainly a mistake (pasted video
// frame, full-desktop capture) and we'd rather reject loud than burn
// tunnel bandwidth on the cloud-relay path.
const maxPasteImageBytes = 10 * 1024 * 1024

// pasteImageDir is ~/.soluterminal/pastes — same root we already use for
// the daemon's own state. Created lazily on first paste.
func pasteImageDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".soluterminal", "pastes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// pastedImageExt maps a browser-reported MIME type to a file extension.
// Returns "" for anything that isn't a supported raster image — the
// handler rejects on empty.
func pastedImageExt(ct string) string {
	// Some browsers append parameters (e.g. "image/png; charset=…"); we
	// only care about the primary type.
	ct = strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	switch ct {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	}
	return ""
}

// pastedImageName generates a random-ish "solu-paste-XXXX" basename.
// Short prefix so users skimming the pastes dir can see at a glance
// what put the file there; random suffix avoids collisions and makes
// the path unguessable for a co-tenant reading the dir listing.
func pastedImageName(ext string) (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "solu-paste-" + hex.EncodeToString(buf[:]) + ext, nil
}

// pasteImage accepts a raw image body and writes it to
// ~/.soluterminal/pastes/<name>. Returns the absolute path so the web
// layer can inject `@<path>` into Claude Code's prompt. Scoped to a
// specific pane so the auth/ownership model stays identical to every
// other /api/panes/{id}/* route, even though we don't actually need
// the pane ID to write the file.
func (a *apiServer) pasteImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := a.mgr.Get(id); !ok {
		http.Error(w, "pane not found", 404)
		return
	}

	ext := pastedImageExt(r.Header.Get("Content-Type"))
	if ext == "" {
		http.Error(w, "unsupported image type", 415)
		return
	}

	// Cap the read so a runaway upload can't OOM us. http.MaxBytesReader
	// returns a "http: request body too large" error past the limit, which
	// surfaces as 500 below — acceptable for a v1.
	body := http.MaxBytesReader(w, r.Body, maxPasteImageBytes)
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), 400)
		return
	}
	if len(data) == 0 {
		http.Error(w, "empty body", 400)
		return
	}

	dir, err := pasteImageDir()
	if err != nil {
		http.Error(w, "paste dir: "+err.Error(), 500)
		return
	}
	name, err := pastedImageName(ext)
	if err != nil {
		http.Error(w, "name: "+err.Error(), 500)
		return
	}
	path := filepath.Join(dir, name)
	// 0o600 — image may contain sensitive content (screenshot of private
	// work); no reason for other users on the box to read it.
	if err := os.WriteFile(path, data, 0o600); err != nil {
		http.Error(w, "write: "+err.Error(), 500)
		return
	}

	writeJSON(w, 200, map[string]any{"path": path, "bytes": len(data)})
}
