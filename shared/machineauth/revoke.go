package machineauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// RevokePath is the cloud endpoint a computer calls to remove itself from its
// account when the user disconnects it.
const RevokePath = "/api/internal/machine/revoke"

// revokeKeyLabel separates the revoke proof from every other HMAC made with
// the machine token, so a revoke key can never pass as a request signature.
const revokeKeyLabel = "soluterminal/machine-revoke/v1"

// RevokeKey derives the proof a disconnected computer presents to revoke
// itself. Only the holder of the machine token can compute it, but the key
// itself authorises nothing except that revocation. That lets the daemon
// delete the token at once and still retry the revoke later when the cloud
// was unreachable, without keeping a working credential on disk.
func RevokeKey(token string) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(revokeKeyLabel))
	return hex.EncodeToString(mac.Sum(nil))
}

// RevokeKeyMatches reports, in constant time, whether key is the revoke key
// for token. An empty token or key never matches.
func RevokeKeyMatches(token, key string) bool {
	if token == "" || key == "" {
		return false
	}
	got, err := hex.DecodeString(key)
	if err != nil {
		return false
	}
	want, _ := hex.DecodeString(RevokeKey(token))
	return hmac.Equal(got, want)
}
