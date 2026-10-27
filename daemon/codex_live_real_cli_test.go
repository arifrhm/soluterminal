//go:build linux || windows

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func codexLiveTestDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "clt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err = codexLivePrivateDirectory(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCodexLiveClientTransportPreservesLiteralArguments(t *testing.T) {
	got := codexLiveClientArgs([]string{"--no-daemon", "-c", "--no-daemon", "--", "--no-daemon"}, "/tmp/socket")
	want := []string{"--remote", "unix:///tmp/socket", "-c", "--no-daemon", "--", "--no-daemon"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func codexLiveFixtureServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	return server
}

func TestCodexLiveServerPreservesLiteralPrompt(t *testing.T) {
	args := codexLiveServerArgs([]string{"--", "--config=approval_policy=never"}, "fixture.sock")
	for _, arg := range args {
		if strings.Contains(arg, "approval_policy") {
			t.Fatal("literal prompt changed engine policy")
		}
	}
}
