package machineauth

import (
	"net/http"
	"testing"
	"time"
)

func TestSignAndVerifyHeaders(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	body := []byte(`{"ok":true}`)
	h := http.Header{}

	SignHeaders(h, "machine-token", body, now)

	res, err := VerifyHeaders(h, "machine-token", body, now)
	if err != nil {
		t.Fatalf("VerifyHeaders returned error: %v", err)
	}
	if res.Legacy {
		t.Fatalf("signed request was marked legacy")
	}
}

func TestVerifyLegacyHeaders(t *testing.T) {
	res, err := Verify("", "", "machine-token", []byte(`{}`), time.Unix(1_700_000_000, 0), DefaultMaxSkew)
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if !res.Legacy {
		t.Fatalf("missing signature headers should be marked legacy")
	}
}

func TestVerifyRejectsInvalidInputs(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	body := []byte(`{"ok":true}`)
	ts := "1700000000"
	goodSig := Signature("machine-token", ts, body)

	tests := []struct {
		name string
		ts   string
		sig  string
		body []byte
		want error
	}{
		{name: "partial timestamp", ts: ts, sig: "", body: body, want: ErrIncompleteHeaders},
		{name: "partial signature", ts: "", sig: goodSig, body: body, want: ErrIncompleteHeaders},
		{name: "bad timestamp", ts: "nope", sig: goodSig, body: body, want: ErrBadTimestamp},
		{name: "old timestamp", ts: "1699990000", sig: Signature("machine-token", "1699990000", body), body: body, want: ErrTimestampOutOfRange},
		{name: "bad hex", ts: ts, sig: "not-hex", body: body, want: ErrBadSignature},
		{name: "wrong token", ts: ts, sig: goodSig, body: body, want: ErrBadSignature},
		{name: "tampered body", ts: ts, sig: goodSig, body: []byte(`{"ok":false}`), want: ErrBadSignature},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := "machine-token"
			if tt.name == "wrong token" {
				token = "other-token"
			}
			_, err := Verify(tt.ts, tt.sig, token, tt.body, now, DefaultMaxSkew)
			if err != tt.want {
				t.Fatalf("Verify error = %v, want %v", err, tt.want)
			}
		})
	}
}
