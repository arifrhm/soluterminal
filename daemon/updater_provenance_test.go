package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

func provenanceUpdater(t *testing.T, body []byte, sig []byte, pub ed25519.PublicKey) *Updater {
	t.Helper()
	t.Setenv("SOLU_UPDATE_MANIFEST_PUBKEY", base64.StdEncoding.EncodeToString(pub))
	u := NewUpdater("0.0.1", "", "", nil, nil)
	u.manifestURL = "https://updates.example.test/update.json"
	u.httpClient = &http.Client{Transport: staticRoundTripper{
		u.manifestURL:          body,
		u.manifestURL + ".sig": []byte(base64.StdEncoding.EncodeToString(sig)),
	}}
	return u
}

func TestUpdaterSignedSourceProvenanceAndStatus(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"flavor":"public","version":"9.9.9","source":{"sha":"0123456789abcdef","builtAt":"2026-08-10T08:00:00Z","repo":"source","cutId":"rc_12345678","cutVersion":"9.9.9","waivers":[{"taskId":"wft_1","reason":"unrelated"}]},"platforms":{"linux-amd64":{"url":"https://example.test/solud","sha256":"abc"}}}`)
	u := provenanceUpdater(t, body, ed25519.Sign(priv, body), pub)
	if _, _, note, ok := u.LatestManifestSource(); ok || note == "" {
		t.Fatalf("source before a successful check = ok %v note %q, want a stated reason", ok, note)
	}
	status, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.SourceSha != "0123456789abcdef" || status.SourceProvenance != "exact" {
		t.Fatalf("status = %+v", status)
	}
	version, sha, note, ok := u.LatestManifestSource()
	if !ok || version != "9.9.9" || sha != status.SourceSha || note != "" {
		t.Fatalf("latest source = %q %q %q %v", version, sha, note, ok)
	}
}

func TestUpdaterLegacyManifestProvenanceBackwardCompatible(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	body := []byte(`{"flavor":"public","version":"9.9.9","platforms":{"linux-amd64":{"url":"https://example.test/solud","sha256":"abc"}}}`)
	u := provenanceUpdater(t, body, ed25519.Sign(priv, body), pub)
	status, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.SourceSha != "" || status.SourceProvenance != "" {
		t.Fatalf("legacy status = %+v", status)
	}
	_, _, note, ok := u.LatestManifestSource()
	if ok {
		t.Fatal("legacy manifest reported exact source")
	}
	if !strings.Contains(note, "legacy") {
		t.Fatalf("legacy note = %q, want it to name the legacy manifest", note)
	}
}

// An outage after a good fetch leaves the daemon anchoring on a manifest it can
// no longer refresh: still the best answer available, but possibly a release
// behind. Reporting it as a clean anchor is how a stale anchor stays invisible.
func TestLatestManifestSourceAnnotatesDegradedCachedManifest(t *testing.T) {
	u := NewUpdater("0.0.899", "", "", nil, nil)
	u.manifest = &Manifest{Version: "0.0.900", Source: &ManifestSource{Sha: " abc123 "}}

	version, sha, note, ok := u.LatestManifestSource()
	if !ok || version != "0.0.900" || sha != "abc123" || note != "" {
		t.Fatalf("healthy cached manifest = %q %q %q %v, want a clean anchor", version, sha, note, ok)
	}

	u.status.LastError = "manifest fetch: 503 Service Unavailable"
	version, sha, note, ok = u.LatestManifestSource()
	if !ok || version != "0.0.900" || sha != "abc123" {
		t.Fatalf("degraded cached manifest = %q %q %v, want it to still anchor", version, sha, ok)
	}
	if !strings.Contains(note, "503 Service Unavailable") {
		t.Fatalf("degraded note = %q, want the fetch error", note)
	}
}

func TestUpdaterSourceProvenanceTamperFailsSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	body := []byte(`{"flavor":"public","version":"9.9.9","source":{"sha":"good"},"platforms":{"linux-amd64":{"url":"https://example.test/solud","sha256":"abc"}}}`)
	tampered := []byte(strings.Replace(string(body), `"good"`, `"evil"`, 1))
	u := provenanceUpdater(t, tampered, ed25519.Sign(priv, body), pub)
	if _, err := u.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "signature verification failed") {
		t.Fatalf("tampered provenance error = %v", err)
	}
}
