package main

import (
	"os"
	"strings"
	"testing"
)

// TestPreviewAcceptScriptScopesStaleBundleBannerToNonPreview pins the fix for a
// systemic preview-acceptance failure. A mounted preview deliberately runs a
// differently-versioned web bundle than the host daemon it borrows, so
// /api/health and /webdist.json can come from that live upstream and the
// "SoluTerminal updated to X — reloading did not pick it up" banner appears by
// design. Counting that banner failed the strict probe for every
// live-upstream preview (115 occurrences across 35 tasks). The probe must scope
// the check to non-preview documents, where a version mismatch is real.
func TestPreviewAcceptScriptScopesStaleBundleBannerToNonPreview(t *testing.T) {
	script := string(previewAcceptScript)
	if !strings.Contains(script, "staleBundleBanner = !previewMarker") {
		t.Fatal("accept-preview.mjs must gate the stale-bundle banner on !previewMarker")
	}
}

func TestMaterializePreviewAcceptScriptWritesExecutableAsset(t *testing.T) {
	path, err := materializePreviewAcceptScript(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("materialized acceptance script is empty")
	}
	if !strings.Contains(string(raw), "previewMarker") {
		t.Fatal("materialized acceptance script lost the preview-build marker check")
	}
}
