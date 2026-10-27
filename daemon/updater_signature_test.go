package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"testing"
)

type staticRoundTripper map[string][]byte

func (s staticRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	body, ok := s[r.URL.String()]
	if !ok {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(bytes.NewReader(nil)),
			Header:     http.Header{},
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{},
	}, nil
}

func TestFetchManifestVerifiesSignatureWhenPublicKeyConfigured(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"flavor":"public","version":"9.9.9","platforms":{"linux-amd64":{"url":"https://example.test/solud","sha256":"abc"}}}`)
	sig := ed25519.Sign(priv, body)

	t.Setenv("SOLU_UPDATE_MANIFEST_PUBKEY", base64.StdEncoding.EncodeToString(pub))
	u := NewUpdater("0.0.1", "", "", nil, nil)
	u.manifestURL = "https://updates.example.test/update.json"
	u.httpClient = &http.Client{Transport: staticRoundTripper{
		"https://updates.example.test/update.json":     body,
		"https://updates.example.test/update.json.sig": []byte(base64.StdEncoding.EncodeToString(sig)),
	}}

	m, err := u.fetchManifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "9.9.9" {
		t.Fatalf("version = %q, want 9.9.9", m.Version)
	}
}

func TestFetchManifestRejectsBadSignature(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"flavor":"public","version":"9.9.9","platforms":{"linux-amd64":{"url":"https://example.test/solud","sha256":"abc"}}}`)

	t.Setenv("SOLU_UPDATE_MANIFEST_PUBKEY", base64.StdEncoding.EncodeToString(pub))
	u := NewUpdater("0.0.1", "", "", nil, nil)
	u.manifestURL = "https://updates.example.test/update.json"
	u.httpClient = &http.Client{Transport: staticRoundTripper{
		"https://updates.example.test/update.json":     body,
		"https://updates.example.test/update.json.sig": []byte(base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))),
	}}

	if _, err := u.fetchManifest(context.Background()); err == nil {
		t.Fatal("fetchManifest succeeded with a bad signature")
	}
}

func TestFetchManifestAllowsUnsignedManifestWhenNoPublicKeyConfigured(t *testing.T) {
	_ = os.Unsetenv("SOLU_UPDATE_MANIFEST_PUBKEY")
	old := defaultUpdateManifestPubKey
	defaultUpdateManifestPubKey = ""
	defer func() { defaultUpdateManifestPubKey = old }()

	body := []byte(`{"flavor":"public","version":"9.9.9","platforms":{"linux-amd64":{"url":"https://example.test/solud","sha256":"abc"}}}`)
	u := NewUpdater("0.0.1", "", "", nil, nil)
	u.manifestURL = "https://updates.example.test/update.json"
	u.httpClient = &http.Client{Transport: staticRoundTripper{
		"https://updates.example.test/update.json": body,
	}}

	m, err := u.fetchManifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "9.9.9" {
		t.Fatalf("version = %q, want 9.9.9", m.Version)
	}
}

func TestUpdateManifestPublicKeyEmptyByDefault(t *testing.T) {
	_ = os.Unsetenv("SOLU_UPDATE_MANIFEST_PUBKEY")
	old := defaultUpdateManifestPubKey
	defaultUpdateManifestPubKey = ""
	defer func() { defaultUpdateManifestPubKey = old }()

	pub, err := updateManifestPublicKey()
	if err != nil {
		t.Fatal(err)
	}
	if pub != nil {
		t.Fatal("expected nil public key")
	}
}
