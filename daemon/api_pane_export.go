package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (a *apiServer) exportPane(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, ok := a.mgr.Get(id)
	if !ok {
		http.Error(w, "not found", 404)
		return
	}
	raw := p.buffer.Snapshot()
	body := stripANSI(raw)
	snap := p.Snapshot()

	var header strings.Builder
	header.WriteString("# solu pane transcript\n")
	header.WriteString("# title: ")
	header.WriteString(snap.Title)
	header.WriteString("\n# cmd:   ")
	header.WriteString(strings.Join(snap.Cmd, " "))
	header.WriteString("\n# cwd:   ")
	header.WriteString(snap.Cwd)
	header.WriteString("\n# pane:  ")
	header.WriteString(snap.ID)
	header.WriteString("\n# exported: ")
	header.WriteString(time.Now().UTC().Format(time.RFC3339))
	header.WriteString("\n\n")

	slug := slugifyTitle(snap.Title)
	if slug == "" {
		slug = "pane"
	}
	filename := fmt.Sprintf("%s-%s.txt", slug, time.Now().UTC().Format("2006-01-02"))

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.WriteHeader(200)
	_, _ = w.Write([]byte(header.String()))
	_, _ = w.Write([]byte(body))
}

func slugifyTitle(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := b.String()
	return strings.TrimRight(out, "-")
}
