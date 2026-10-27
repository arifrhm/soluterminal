package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
)

// scriptedRT returns a status code per successive request, replaying the
// last entry once the script is exhausted. On a 200 it serves body; on
// any other status it serves an empty body. Signature verification is
// disabled in these tests (defaultUpdateManifestPubKey cleared), so the
// .sig URL is never requested and every call is the manifest fetch.
type scriptedRT struct {
	statuses []int
	body     []byte
	calls    int
}

func (s *scriptedRT) RoundTrip(*http.Request) (*http.Response, error) {
	idx := s.calls
	s.calls++
	code := http.StatusOK
	if len(s.statuses) > 0 {
		if idx >= len(s.statuses) {
			idx = len(s.statuses) - 1
		}
		code = s.statuses[idx]
	}
	var body []byte
	if code == http.StatusOK {
		body = s.body
	}
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{},
	}, nil
}

func disableManifestSignature(t *testing.T) {
	t.Helper()
	orig := defaultUpdateManifestPubKey
	defaultUpdateManifestPubKey = ""
	t.Cleanup(func() { defaultUpdateManifestPubKey = orig })
	t.Setenv("SOLU_UPDATE_MANIFEST_PUBKEY", "")
}

func TestRetryableStatus(t *testing.T) {
	cases := []struct {
		code int
		want bool
	}{
		{500, true}, {502, true}, {503, true}, {429, true},
		{404, false}, {403, false}, {400, false}, {200, false},
	}
	for _, c := range cases {
		if got := retryableStatus(c.code); got != c.want {
			t.Errorf("retryableStatus(%d) = %v, want %v", c.code, got, c.want)
		}
	}
}

// A transient 5xx from the CDN must be swallowed for the first
// maxTransientFailures-1 polls, then surfaced, then cleared on recovery.
func TestCheckSuppressesTransientFailures(t *testing.T) {
	disableManifestSignature(t)

	manifest := []byte(`{"flavor":"public","version":"9.9.9","platforms":{"` + platformKeyCurrent() +
		`":{"url":"https://example.test/solud","sha256":"abc"}}}`)
	rt := &scriptedRT{statuses: []int{500, 500, 500, 200}, body: manifest}

	u := NewUpdater("0.0.1", "", "", nil, nil)
	u.manifestURL = "https://updates.example.test/update.json"
	u.httpClient = &http.Client{Transport: rt}

	// First maxTransientFailures-1 failures: error returned to caller, but
	// the UI-facing LastError stays empty (badge unchanged).
	for i := 1; i < maxTransientFailures; i++ {
		st, err := u.Check(context.Background())
		if err == nil {
			t.Fatalf("call %d: want transient error, got nil", i)
		}
		if st.LastError != "" {
			t.Fatalf("call %d: LastError = %q, want empty (suppressed)", i, st.LastError)
		}
	}

	// The maxTransientFailures-th consecutive failure surfaces.
	st, err := u.Check(context.Background())
	if err == nil {
		t.Fatalf("call %d: want surfaced error", maxTransientFailures)
	}
	if st.LastError == "" {
		t.Fatalf("call %d: LastError empty, want surfaced after threshold", maxTransientFailures)
	}

	// Recovery: a good poll clears the error, resets the streak, and the
	// update is detected normally.
	st, err = u.Check(context.Background())
	if err != nil {
		t.Fatalf("recovery call: unexpected error %v", err)
	}
	if st.LastError != "" {
		t.Fatalf("recovery call: LastError = %q, want cleared", st.LastError)
	}
	if !st.Available || st.Latest != "9.9.9" {
		t.Fatalf("recovery call: Available=%v Latest=%q, want true/9.9.9", st.Available, st.Latest)
	}
	if u.transientFailures != 0 {
		t.Fatalf("recovery call: transientFailures = %d, want 0", u.transientFailures)
	}
}

// A non-transient failure (404 manifest missing) must surface on the
// very first poll — it is not a CDN blip and won't self-heal.
func TestCheckSurfacesNonTransientImmediately(t *testing.T) {
	disableManifestSignature(t)

	rt := &scriptedRT{statuses: []int{404}}
	u := NewUpdater("0.0.1", "", "", nil, nil)
	u.manifestURL = "https://updates.example.test/update.json"
	u.httpClient = &http.Client{Transport: rt}

	st, err := u.Check(context.Background())
	if err == nil {
		t.Fatal("want error for 404 manifest")
	}
	if st.LastError == "" {
		t.Fatal("404 is non-transient and must surface immediately")
	}
}
