package main

import (
	"encoding/json"
	"io/fs"
)

type WebProvenance struct {
	WebVersion string `json:"webVersion"`
	SourceSHA  string `json:"sourceSha"`
	Digest     string `json:"digest"`
}

// embeddedWebProvenance is deliberately independent of the daemon's ldflags
// version. /webdist.json is also a public, no-store endpoint: the frontend
// probes it to learn what the shell-serving origin would serve after a reload.
var embeddedWebProvenance = parseWebProvenance(webFS, "webdist/webdist.json")

func parseWebProvenance(fsys fs.FS, name string) WebProvenance {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return WebProvenance{}
	}
	var provenance WebProvenance
	if err := json.Unmarshal(b, &provenance); err != nil {
		return WebProvenance{}
	}
	return provenance
}

func CurrentWebProvenance() WebProvenance {
	return embeddedWebProvenance
}
