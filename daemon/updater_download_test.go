package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type downloadRoundTripper struct {
	status        int
	body          []byte
	contentLength int64
}

func (d downloadRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	contentLength := int64(-1)
	if d.contentLength != 0 {
		contentLength = d.contentLength
	}
	return &http.Response{
		StatusCode:    d.status,
		Body:          io.NopCloser(bytes.NewReader(d.body)),
		Header:        http.Header{},
		ContentLength: contentLength,
	}, nil
}

func TestUpdaterDownloadWritesVerifiedFile(t *testing.T) {
	t.Parallel()
	body := []byte("verified binary bytes")
	sum := sha256.Sum256(body)
	dst := filepath.Join(t.TempDir(), "solud")
	u := NewUpdater("0.0.1", "", "", nil, nil)
	u.httpClient = &http.Client{Transport: downloadRoundTripper{
		status: http.StatusOK,
		body:   body,
	}}

	if err := u.download(context.Background(), "https://updates.example.test/solud", hex.EncodeToString(sum[:]), dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("downloaded body = %q, want %q", got, body)
	}
	if _, err := os.Stat(dst + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp file still exists after successful download: %v", err)
	}
}

func TestUpdaterDownloadRejectsHashMismatchAndRemovesTemp(t *testing.T) {
	t.Parallel()
	dst := filepath.Join(t.TempDir(), "solud")
	u := NewUpdater("0.0.1", "", "", nil, nil)
	u.httpClient = &http.Client{Transport: downloadRoundTripper{
		status: http.StatusOK,
		body:   []byte("different bytes"),
	}}

	err := u.download(context.Background(), "https://updates.example.test/solud", strings.Repeat("0", 64), dst)
	if err == nil {
		t.Fatal("download succeeded with hash mismatch")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("error = %q, want sha256 mismatch", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("destination exists after hash mismatch: %v", err)
	}
	if _, err := os.Stat(dst + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp file exists after hash mismatch: %v", err)
	}
}

func TestUpdaterDownloadRejectsNonOKResponse(t *testing.T) {
	t.Parallel()
	dst := filepath.Join(t.TempDir(), "solud")
	u := NewUpdater("0.0.1", "", "", nil, nil)
	u.httpClient = &http.Client{Transport: downloadRoundTripper{
		status: http.StatusTeapot,
		body:   []byte("nope"),
	}}

	err := u.download(context.Background(), "https://updates.example.test/solud", strings.Repeat("0", 64), dst)
	if err == nil {
		t.Fatal("download succeeded on non-200 response")
	}
	if !strings.Contains(err.Error(), "HTTP 418") {
		t.Fatalf("error = %q, want HTTP 418", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("destination exists after non-200 response: %v", err)
	}
}

func TestUpdaterDownloadRejectsAnnouncedBodyOverCap(t *testing.T) {
	old := maxUpdateDownloadBytes
	maxUpdateDownloadBytes = 8
	defer func() { maxUpdateDownloadBytes = old }()

	dst := filepath.Join(t.TempDir(), "solud")
	u := NewUpdater("0.0.1", "", "", nil, nil)
	u.httpClient = &http.Client{Transport: downloadRoundTripper{
		status:        http.StatusOK,
		body:          []byte("small"),
		contentLength: 9,
	}}

	err := u.download(context.Background(), "https://updates.example.test/solud", strings.Repeat("0", 64), dst)
	if err == nil {
		t.Fatal("download succeeded with announced size over cap")
	}
	if !strings.Contains(err.Error(), "exceeds cap") {
		t.Fatalf("error = %q, want cap error", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("destination exists after cap rejection: %v", err)
	}
	if _, err := os.Stat(dst + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp exists after cap rejection: %v", err)
	}
}

func TestUpdaterDownloadRejectsStreamOverCapAndRemovesTemp(t *testing.T) {
	old := maxUpdateDownloadBytes
	maxUpdateDownloadBytes = 4
	defer func() { maxUpdateDownloadBytes = old }()

	dst := filepath.Join(t.TempDir(), "solud")
	u := NewUpdater("0.0.1", "", "", nil, nil)
	u.httpClient = &http.Client{Transport: downloadRoundTripper{
		status: http.StatusOK,
		body:   []byte("12345"),
	}}

	err := u.download(context.Background(), "https://updates.example.test/solud", strings.Repeat("0", 64), dst)
	if err == nil {
		t.Fatal("download succeeded with streamed size over cap")
	}
	if !strings.Contains(err.Error(), "exceeded cap") {
		t.Fatalf("error = %q, want exceeded cap", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("destination exists after cap rejection: %v", err)
	}
	if _, err := os.Stat(dst + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp exists after cap rejection: %v", err)
	}
}

// slowBody delivers one byte per Read after a fixed delay, so the whole
// transfer outlasts a short total timeout while every inter-byte gap stays
// below the idle window.
type slowBody struct {
	data  []byte
	pos   int
	delay time.Duration
}

func (s *slowBody) Read(p []byte) (int, error) {
	if s.pos >= len(s.data) {
		return 0, io.EOF
	}
	time.Sleep(s.delay)
	p[0] = s.data[s.pos]
	s.pos++
	return 1, nil
}

func (s *slowBody) Close() error { return nil }

type streamRoundTripper struct {
	status int
	body   io.ReadCloser
	length int64
}

func (s streamRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode:    s.status,
		Body:          s.body,
		Header:        http.Header{},
		ContentLength: s.length,
	}, nil
}

// TestUpdaterDownloadAllowsSlowButSteadyTransfer is the regression test for the
// 20s wall-clock cap: a body that takes longer than the manifest client's total
// timeout, but never stalls longer than the idle window, must still succeed.
func TestUpdaterDownloadAllowsSlowButSteadyTransfer(t *testing.T) {
	body := []byte("0123456789")
	sum := sha256.Sum256(body)
	dst := filepath.Join(t.TempDir(), "solud")
	u := NewUpdater("0.0.1", "", "", nil, nil)
	u.httpClient = &http.Client{
		Transport: streamRoundTripper{status: http.StatusOK, body: &slowBody{data: body, delay: 15 * time.Millisecond}, length: int64(len(body))},
		// Would cut the ~150ms transfer off in the old download path, which
		// used this client (and its Timeout) directly.
		Timeout: 60 * time.Millisecond,
	}
	u.downloadIdleTimeout = 50 * time.Millisecond
	u.downloadMaxDuration = 5 * time.Second

	if err := u.download(context.Background(), "https://updates.example.test/solud", hex.EncodeToString(sum[:]), dst); err != nil {
		t.Fatalf("slow-but-steady download failed: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("downloaded body = %q, want %q", got, body)
	}
}

type ctxBlockingBody struct{ ctx context.Context }

func (b *ctxBlockingBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *ctxBlockingBody) Close() error { return nil }

type ctxBodyRoundTripper struct{ length int64 }

func (c ctxBodyRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode:    http.StatusOK,
		Body:          &ctxBlockingBody{ctx: r.Context()},
		Header:        http.Header{},
		ContentLength: c.length,
	}, nil
}

// TestUpdaterDownloadStalledBodyFailsFast proves a genuinely stalled connection
// is still cut near the idle window (not left to hang for the absolute cap).
func TestUpdaterDownloadStalledBodyFailsFast(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "solud")
	u := NewUpdater("0.0.1", "", "", nil, nil)
	u.httpClient = &http.Client{Transport: ctxBodyRoundTripper{length: 100}}
	u.downloadIdleTimeout = 40 * time.Millisecond
	u.downloadMaxDuration = 5 * time.Second

	start := time.Now()
	err := u.download(context.Background(), "https://updates.example.test/solud", strings.Repeat("0", 64), dst)
	if err == nil {
		t.Fatal("stalled download succeeded")
	}
	if !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("error = %q, want stalled", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("stalled download took %s, want it cut near the %s idle window", elapsed, u.downloadIdleTimeout)
	}
	if _, err := os.Stat(dst + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp file exists after stall: %v", err)
	}
}
