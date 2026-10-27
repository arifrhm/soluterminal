package machineauth

import (
	"testing"
	"time"
)

func TestRevokeKeyMatchesOnlyItsToken(t *testing.T) {
	key := RevokeKey("machine-token")
	if !RevokeKeyMatches("machine-token", key) {
		t.Fatal("revoke key does not match its own token")
	}
	for _, tc := range []struct{ name, token, key string }{
		{"other token", "other-token", key},
		{"empty token", "", key},
		{"empty key", "machine-token", ""},
		{"not hex", "machine-token", "zz"},
		{"truncated", "machine-token", key[:32]},
	} {
		if RevokeKeyMatches(tc.token, tc.key) {
			t.Fatalf("%s: RevokeKeyMatches = true, want false", tc.name)
		}
	}
}

// The revoke key must not double as a request signature for any body.
func TestRevokeKeyIsNotARequestSignature(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	key := RevokeKey("machine-token")
	if _, err := Verify("1700000000", key, "machine-token", []byte(""), now, DefaultMaxSkew); err == nil {
		t.Fatal("revoke key verified as a request signature")
	}
}
