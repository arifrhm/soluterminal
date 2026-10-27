package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestUpdateStatusCarriesBuildProvenance(t *testing.T) {
	prevSha, prevDirty, prevChannel := buildSourceSha, buildDirty, buildChannel
	t.Cleanup(func() { buildSourceSha, buildDirty, buildChannel = prevSha, prevDirty, prevChannel })
	buildSourceSha, buildDirty, buildChannel = "1bfb3f8392e90e3f3d20774d169860ca860794db", "1", "local"

	a := &apiServer{ver: "0.0.1203"}
	rec := httptest.NewRecorder()
	a.updateStatus(rec, httptest.NewRequest("GET", "/api/update/status", nil))

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if body["current"] != "0.0.1203" {
		t.Fatalf("current = %v, want the daemon version kept alongside the stamp", body["current"])
	}
	build, ok := body["build"].(map[string]any)
	if !ok {
		t.Fatalf("build missing from %s", rec.Body.String())
	}
	if build["sourceSha"] != buildSourceSha || build["dirty"] != true || build["channel"] != "local" {
		t.Fatalf("build = %v", build)
	}
}

func TestBuildProvenanceDefaultsMarkUnstampedBuildsLocal(t *testing.T) {
	got := buildProvenance()
	if got["channel"] == "release" && buildSourceSha == "unknown" {
		t.Fatalf("an unstamped build must never claim the release channel: %v", got)
	}
}
