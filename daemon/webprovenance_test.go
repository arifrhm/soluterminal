package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"
)

func TestParseWebProvenance(t *testing.T) {
	t.Parallel()
	valid := fstest.MapFS{
		"webdist/webdist.json": {Data: []byte(`{"marker":"solu-webdist v=1.2.3 src=abc123 sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","webVersion":"1.2.3","sourceSha":"abc123","digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)},
	}
	got := parseWebProvenance(valid, "webdist/webdist.json")
	if got.WebVersion != "1.2.3" || got.SourceSHA != "abc123" || len(got.Digest) != 64 {
		t.Fatalf("valid provenance = %+v", got)
	}

	for name, fsys := range map[string]fstest.MapFS{
		"missing":   {},
		"malformed": {"webdist/webdist.json": {Data: []byte(`{"webVersion":`)}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := parseWebProvenance(fsys, "webdist/webdist.json"); got != (WebProvenance{}) {
				t.Fatalf("provenance = %+v, want zero value", got)
			}
		})
	}
}

func TestHealthIncludesWebProvenance(t *testing.T) {
	saved := embeddedWebProvenance
	embeddedWebProvenance = WebProvenance{WebVersion: "1.2.3", SourceSHA: "abc123", Digest: "digest"}
	t.Cleanup(func() { embeddedWebProvenance = saved })

	a := &apiServer{mgr: NewManager(""), startT: time.Now(), ver: "1.2.3"}
	rec := httptest.NewRecorder()
	a.health(rec, httptest.NewRequest("GET", "/api/health", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["webVersion"] != "1.2.3" || body["webSourceSha"] != "abc123" {
		t.Fatalf("health provenance = webVersion:%v webSourceSha:%v", body["webVersion"], body["webSourceSha"])
	}
}
