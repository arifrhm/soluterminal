package main

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Max non-image attachment size. Bigger than maxPasteImageBytes because PDFs
// and data dumps legitimately run larger than a screenshot, but still capped
// so a runaway upload can't OOM us or saturate the cloud-relay tunnel.
const maxPasteFileBytes = 25 * 1024 * 1024

// pastedFileExts is the allowlist of extensions we'll accept for the generic
// file-attach path. The point is to keep Claude's Read tool happy (it keys
// behaviour off the extension), NOT to run anything — we never execute these.
// We deny by omission: archives, binaries, and anything not here get a 415.
// The four raster image exts are included for completeness, though images
// normally go through pasteImage.
var pastedFileExts = map[string]bool{
	// Docs / data / config.
	".pdf": true, ".txt": true, ".md": true, ".markdown": true,
	".csv": true, ".tsv": true, ".json": true, ".jsonl": true,
	".log": true, ".yaml": true, ".yml": true, ".toml": true,
	".ini": true, ".xml": true, ".html": true, ".htm": true,
	".css": true, ".scss": true, ".sql": true,
	// Code.
	".py": true, ".js": true, ".jsx": true, ".ts": true, ".tsx": true,
	".go": true, ".rs": true, ".java": true, ".kt": true, ".kts": true,
	".c": true, ".cc": true, ".cpp": true, ".h": true, ".hpp": true,
	".sh": true, ".bash": true, ".rb": true, ".php": true,
	".swift": true, ".dart": true, ".gradle": true,
	// Images (parity with pasteImage; callers usually use pasteImage instead).
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
}

// pastedFileExt extracts a safe, lowercased extension from the client-supplied
// X-Filename header. The header is URL-encoded; we decode it, reduce to the
// basename (so "../../etc/passwd" can't escape — we only ever read the ext, but
// be defensive), and look up the allowlist. Returns "" for anything not allowed
// so the handler can reject with a 415.
func pastedFileExt(rawHeader string) string {
	name := rawHeader
	if dec, err := url.QueryUnescape(rawHeader); err == nil {
		name = dec
	}
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	ext := strings.ToLower(filepath.Ext(name))
	if pastedFileExts[ext] {
		return ext
	}
	return ""
}

// pasteFile accepts a raw file body and writes it to
// ~/.soluterminal/pastes/<name>, returning the absolute path so the web/mobile
// layer can inject `@<path>` into Claude Code's prompt — Claude then reads the
// file itself (PDF/code/csv) via its Read tool. The sibling of pasteImage for
// non-image attachments; kept separate so the proven image path stays untouched
// and the two allowlists don't bleed into each other. Scoped to a specific pane
// so the auth/ownership model matches every other /api/panes/{id}/* route.
func (a *apiServer) pasteFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := a.mgr.Get(id); !ok {
		http.Error(w, "pane not found", 404)
		return
	}

	// Extension comes from the original filename (X-Filename), not the
	// Content-Type: the client knows the real name, and Claude's Read tool
	// keys off the extension. We keep the on-disk basename random regardless.
	ext := pastedFileExt(r.Header.Get("X-Filename"))
	if ext == "" {
		http.Error(w, "unsupported file type", 415)
		return
	}

	// Cap the read so a runaway upload can't OOM us. http.MaxBytesReader
	// returns a "http: request body too large" error past the limit, which
	// surfaces as 400 below — acceptable for a v1.
	body := http.MaxBytesReader(w, r.Body, maxPasteFileBytes)
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
	// Reuse the "solu-paste-<rand><ext>" scheme — the random basename keeps the
	// path unguessable for a co-tenant; only the extension needs to be real.
	name, err := pastedImageName(ext)
	if err != nil {
		http.Error(w, "name: "+err.Error(), 500)
		return
	}
	path := filepath.Join(dir, name)
	// 0o600 — the file may carry sensitive content; no reason for other users
	// on the box to read it.
	if err := os.WriteFile(path, data, 0o600); err != nil {
		http.Error(w, "write: "+err.Error(), 500)
		return
	}

	writeJSON(w, 200, map[string]any{"path": path, "bytes": len(data)})
}
